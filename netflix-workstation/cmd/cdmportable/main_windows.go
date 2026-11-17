// cdmportable implements the existing native-host CLI without requiring WSL.
package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	a := os.Args[1:]
	if len(a) != 4 && len(a) != 5 && len(a) != 7 && len(a) != 8 {
		return errors.New("argumentos del motor portátil no válidos")
	}
	x, err := netflixlib.NewExchange()
	if err != nil {
		return err
	}
	x.Config = netflixlib.Config{Render: len(a) == 8 && a[7] == "--y4m-stdout", Job: len(a) >= 7, Certificate: len(a) >= 5 && a[4] != ""}
	x.Files = map[string]string{"cdm": a[0], "pssh": a[1]}
	x.Challenge, x.Response = a[2], a[3]
	if x.Config.Certificate {
		x.Files["certificate"] = a[4]
	}
	if x.Config.Job {
		x.Files["job"] = a[5]
		x.Output = a[6]
	}
	x.Events = os.Stdout
	x.Frames = os.Stdout
	if x.Config.Render {
		x.Events = os.Stderr
	}
	exe, _ := os.Executable()
	root := filepath.Join(filepath.Dir(exe), "runtime")
	for _, file := range []string{"qemu-system-x86_64.exe", "vmlinuz", "initrd.gz"} {
		if info, err := os.Stat(filepath.Join(root, file)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("falta el componente portátil %s", file)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	server := &http.Server{Handler: x, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	defer server.Close()
	go server.Serve(listener)
	port := listener.Addr().(*net.TCPAddr).Port
	memory := 1024
	if x.Config.Job {
		if info, err := os.Stat(a[5]); err == nil {
			memory += int(info.Size()>>20) * 2
		}
	}
	if memory > 4096 {
		return errors.New("la pista supera la capacidad del motor portátil")
	}
	// The guest has only a /32 host route, no default route or DNS configuration.
	args := []string{"-L", root, "-nodefaults", "-no-user-config", "-display", "none", "-monitor", "none", "-serial", "stdio", "-no-reboot", "-machine", "pc", "-accel", "whpx", "-accel", "tcg,thread=multi", "-cpu", "max", "-smp", "2", "-m", strconv.Itoa(memory), "-kernel", filepath.Join(root, "vmlinuz"), "-initrd", filepath.Join(root, "initrd.gz"), "-append", "console=ttyS0 quiet panic=-1 netflix.token=" + x.Token + " netflix.port=" + strconv.Itoa(port), "-netdev", "user,id=netflix,ipv6=off", "-device", "virtio-net-pci,netdev=netflix"}
	if accelerator := os.Getenv("NETFLIX_DOWNLOADER_VM_ACCEL"); accelerator != "" {
		if accelerator != "tcg" {
			return errors.New("el modo de diagnóstico debe ser tcg")
		}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-accel" && args[i+1] == "whpx" {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	// Assign this wrapper before starting QEMU: descendants inherit the job even
	// if the controlling worker is cancelled immediately after QEMU starts.
	if err = windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return err
	}
	// Keep this non-inheritable handle until process exit. Closing it earlier
	// would also terminate this wrapper, which is deliberately inside the job.
	command := exec.CommandContext(ctx, filepath.Join(root, "qemu-system-x86_64.exe"), args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	log := &tailWriter{}
	command.Stdout = log
	command.Stderr = log
	if err = command.Start(); err != nil {
		return fmt.Errorf("no se pudo iniciar el motor portátil: %w", err)
	}
	defer func() { command.Process.Kill() }()
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	boot := time.NewTimer(90 * time.Second)
	defer boot.Stop()
	select {
	case <-x.Started:
	case <-boot.C:
		return fmt.Errorf("no arrancó el motor portátil: %s", strings.ReplaceAll(log.String(), x.Token, "[redacted]"))
	case err := <-finished:
		return fmt.Errorf("falló el arranque portátil (%v): %s", err, strings.ReplaceAll(log.String(), x.Token, "[redacted]"))
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case result := <-x.Done:
		if result.Code != 0 {
			return fmt.Errorf("el motor portátil terminó con código %d: %s", result.Code, result.Error)
		}
		return nil
	case err := <-finished:
		select {
		case result := <-x.Done:
			if result.Code == 0 {
				return nil
			}
		default:
		}
		return fmt.Errorf("el motor portátil se cerró antes de completar el trabajo (%v): %s", err, strings.ReplaceAll(log.String(), x.Token, "[redacted]"))
	case <-ctx.Done():
		return errors.New("se agotó el tiempo del motor portátil")
	}
}

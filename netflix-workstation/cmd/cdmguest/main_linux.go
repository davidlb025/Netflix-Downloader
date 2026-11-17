// The guest receives no account credentials and has no general network access.
package main

import (
		netflixlib "local.netflix/netflix-downloader"
"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 100 * time.Second}}
var token string
var endpoint string

func request(method, path string, body io.Reader, session string) (*http.Response, error) {
	r, err := http.NewRequest(method, endpoint+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		r.Header.Set("X-CDM-Session", session)
	}
	response, err := client.Do(r)
	if err != nil {
		return nil, errors.New("no se pudo conectar con Windows")
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, fmt.Errorf("el transporte devolvió HTTP %d", response.StatusCode)
	}
	return response, nil
}
func post(path string, body io.Reader, session string) error {
	response, err := request("POST", path, body, session)
	if err == nil {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	return err
}
func fetch(path, destination string) error {
	response, err := request("GET", path, nil, "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, response.Body)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func main() {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	for _, value := range strings.Fields(string(cmdline)) {
		if strings.HasPrefix(value, "netflix.token=") {
			token = strings.TrimPrefix(value, "netflix.token=")
		}
		if strings.HasPrefix(value, "netflix.port=") {
			endpoint = "http://10.0.2.2:" + strings.TrimPrefix(value, "netflix.port=")
		}
	}
	err := run()
	result := netflixlib.PortableResult{}
	if err != nil {
		result.Code = 1
		result.Error = err.Error()
	}
	body, _ := json.Marshal(result)
	post("/result", bytes.NewReader(body), "")
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	for {
		time.Sleep(time.Hour)
	}
}

func run() error {
	var config netflixlib.Config
	var response *http.Response
	var err error
	for i := 0; i < 40; i++ {
		response, err = request("GET", "/config", nil, "")
		if err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	err = json.NewDecoder(response.Body).Decode(&config)
	response.Body.Close()
	if err != nil {
		return errors.New("configuración no válida")
	}
	const root = "/run/job"
	if err = os.MkdirAll(root, 0700); err != nil {
		return errors.New("no se pudo crear el directorio temporal")
	}
	for _, name := range []string{"cdm", "pssh"} {
		if err = fetch("/file/"+name, filepath.Join(root, name)); err != nil {
			return err
		}
	}
	args := []string{root + "/cdm", root + "/pssh", root + "/challenge", root + "/license"}
	if config.Certificate {
		if err = fetch("/file/certificate", root+"/certificate"); err != nil {
			return err
		}
		args = append(args, root+"/certificate")
	} else if config.Job {
		args = append(args, "")
	}
	if config.Job {
		if err = fetch("/file/job", root+"/job"); err != nil {
			return err
		}
		args = append(args, root+"/job", root+"/output")
	}
	if config.Render {
		args = append(args, "--y4m-stdout")
	}
	command := exec.Command("/bin/cdm-host", args...)
	var events io.ReadCloser
	var frames io.ReadCloser
	if config.Render {
		events, err = command.StderrPipe()
		if err == nil {
			frames, err = command.StdoutPipe()
		}
	} else {
		events, err = command.StdoutPipe()
		command.Stderr = os.Stderr
	}
	if err != nil {
		return errors.New("no se pudo preparar el host")
	}
	if err = command.Start(); err != nil {
		return errors.New("no se pudo iniciar el host")
	}
	defer command.Process.Kill()
	frameDone := make(chan error, 1)
	if config.Render {
		go func() { defer frames.Close(); frameDone <- post("/frames", frames, "") }()
	}
	scanner := bufio.NewScanner(events)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var event struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &event) != nil || event.Event == "" {
			continue
		}
		if event.Event == "challengeReady" {
			challenge, err := os.ReadFile(root + "/challenge")
			if err != nil {
				return errors.New("falta el desafío")
			}
			session, err := os.ReadFile(root + "/challenge.session")
			if err != nil {
				return errors.New("falta la sesión del módulo")
			}
			if err = post("/challenge", bytes.NewReader(challenge), base64.StdEncoding.EncodeToString(session)); err != nil {
				return err
			}
		}
		if err = post("/events", bytes.NewReader(line), ""); err != nil {
			return err
		}
		if event.Event == "challengeReady" {
			if err = fetch("/license", root+"/license"); err != nil {
				return err
			}
		}
	}
	if scanner.Err() != nil {
		return errors.New("se interrumpió el host")
	}
	if config.Render {
		if err = <-frameDone; err != nil {
			return err
		}
	}
	if err = command.Wait(); err != nil {
		return fmt.Errorf("el host CDM rechazó el trabajo: %s", err)
	}
	if config.Job {
		file, err := os.Open(root + "/output")
		if err != nil {
			return errors.New("falta el resultado")
		}
		defer file.Close()
		if err = post("/output", file, ""); err != nil {
			return err
		}
	}
	return nil
}

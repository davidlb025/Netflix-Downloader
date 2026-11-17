package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Every decoded I420 frame is included, with its timing and dimensions.
// This is an exact cryptographic comparison, never a perceptual/sample hash.
type VideoFingerprint struct {
	Algorithm string `json:"algoritmo"`
	SHA256    string `json:"sha256"`
	Frames    int64  `json:"fotogramas"`
	Width     int    `json:"ancho"`
	Height    int    `json:"alto"`
	TimeBase  string `json:"base_tiempo"`
	SAR       string `json:"aspecto_pixel"`
}

func parseFrameHashes(input io.Reader) (VideoFingerprint, error) {
	f := VideoFingerprint{Algorithm: "sha256-framehash-i420-timing-v1"}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 65536)
	hash := sha256.New()
	headers := false
	algorithm := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "#hash:") {
				algorithm = strings.TrimSpace(strings.TrimPrefix(line, "#hash:")) == "SHA256"
			}
			if strings.HasPrefix(line, "#tb 0:") {
				if headers {
					return f, errors.New("cambio de tiempos durante la comparación")
				}
				f.TimeBase = strings.TrimSpace(strings.TrimPrefix(line, "#tb 0:"))
			}
			if strings.HasPrefix(line, "#dimensions 0:") {
				if headers {
					return f, errors.New("cambio de tamaño durante la comparación")
				}
				if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "#dimensions 0:")), "%dx%d", &f.Width, &f.Height); err != nil {
					return f, err
				}
			}
			if strings.HasPrefix(line, "#sar 0:") {
				if headers {
					return f, errors.New("cambio de aspecto durante la comparación")
				}
				f.SAR = strings.TrimSpace(strings.TrimPrefix(line, "#sar 0:"))
			}
			continue
		}
		if !headers {
			if !algorithm || f.Width <= 0 || f.Height <= 0 || f.Width > 8192 || f.Height > 8192 || f.TimeBase == "" || f.SAR == "" {
				return f, errors.New("cabecera de fotogramas incompleta")
			}
			fmt.Fprintf(hash, "%s\n%dx%d\n%s\n%s\n", f.Algorithm, f.Width, f.Height, f.TimeBase, f.SAR)
			headers = true
		}
		fields := strings.Split(line, ",")
		if len(fields) != 6 {
			return f, errors.New("registro de fotograma no válido")
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		if fields[0] != "0" {
			return f, errors.New("se recibió una pista distinta del vídeo")
		}
		for i := 1; i < 5; i++ {
			n, err := strconv.ParseInt(fields[i], 10, 64)
			if err != nil || i >= 3 && n <= 0 {
				return f, errors.New("tiempos o tamaño de fotograma no válidos")
			}
			fields[i] = strconv.FormatInt(n, 10)
		}
		if size, _ := strconv.ParseInt(fields[4], 10, 64); size != int64(f.Width)*int64(f.Height)+2*int64((f.Width+1)/2)*int64((f.Height+1)/2) {
			return f, errors.New("el formato de píxeles no es I420")
		}
		digest, err := hex.DecodeString(fields[5])
		if err != nil || len(digest) != sha256.Size {
			return f, errors.New("huella de fotograma no válida")
		}
		fields[5] = hex.EncodeToString(digest)
		io.WriteString(hash, strings.Join(fields, ",")+"\n")
		f.Frames++
		if f.Frames > 10000000 {
			return f, errors.New("demasiados fotogramas")
		}
	}
	if err := scanner.Err(); err != nil {
		return f, err
	}
	if f.Frames == 0 {
		return f, errors.New("el vídeo no contiene fotogramas verificables")
	}
	f.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return f, nil
}

func fingerprintVideo(ctx context.Context, ffmpeg, path string, verifyAudio bool) (VideoFingerprint, error) {
	args := []string{"-hide_banner", "-v", "error", "-xerror", "-nostdin", "-copyts", "-i", path, "-map", "0:v:0", "-an", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-fps_mode", "passthrough", "-enc_time_base", "demux", "-f", "framehash", "-hash", "sha256", "pipe:1"}
	if verifyAudio {
		args = append(args, "-map", "0:a:0", "-vn", "-f", "null", "NUL")
	}
	command := exec.CommandContext(ctx, ffmpeg, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.StdoutPipe()
	if err != nil {
		return VideoFingerprint{}, err
	}
	if err = command.Start(); err != nil {
		return VideoFingerprint{}, err
	}
	f, parseErr := parseFrameHashes(output)
	if parseErr != nil {
		command.Process.Kill()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return f, ctx.Err()
	}
	if parseErr != nil {
		return f, parseErr
	}
	if waitErr != nil {
		return f, fmt.Errorf("no se pudo comprobar todo el vídeo/audio: %w: %.800s", waitErr, diagnostic.String())
	}
	return f, nil
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	buffer := make([]byte, 256*1024)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := file.Read(buffer)
		if n > 0 {
			h.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

type probeStream struct {
	Index     int               `json:"index"`
	CodecType string            `json:"codec_type"`
	CodecName string            `json:"codec_name"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Channels  int               `json:"channels"`
	StartTime string            `json:"start_time"`
	Tags      map[string]string `json:"tags"`
}
type probeInfo struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Name      string `json:"format_name"`
		Duration  string `json:"duration"`
		Size      string `json:"size"`
		StartTime string `json:"start_time"`
	} `json:"format"`
}

func probeMedia(ctx context.Context, ffmpeg, path string) (probeInfo, error) {
	var result probeInfo
	exe, err := exec.LookPath(ffmpeg)
	if err != nil {
		return result, err
	}
	probe := filepath.Join(filepath.Dir(exe), "ffprobe.exe")
	if !regularFile(probe) {
		return result, errors.New("falta ffprobe.exe junto a FFmpeg para exportar las pistas reales")
	}
	if !regularFile(path) {
		return result, errors.New("el archivo multimedia no es regular")
	}
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out, diagnostic bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return result, errors.New("FFprobe no pudo verificar las pistas del archivo")
	}
	if out.Len() > 2<<20 || json.Unmarshal(out.Bytes(), &result) != nil || len(result.Streams) == 0 {
		return result, errors.New("respuesta FFprobe no válida")
	}
	return result, nil
}
func (p probeInfo) duration() float64 { v, _ := strconv.ParseFloat(p.Format.Duration, 64); return v }
func mediaSize(path string) int64 {
	s, e := os.Stat(path)
	if e != nil {
		return 0
	}
	return s.Size()
}

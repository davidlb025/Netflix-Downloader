package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func artworkURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && strings.HasSuffix(u.Hostname(), ".nflxso.net")
}
func fetchArtwork(ctx context.Context, client *http.Client, raw string) ([]byte, string, error) {
	if !artworkURL(raw) {
		return nil, "", errors.New("la ilustración no procede del CDN público de Netflix")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, "", err
	}
	// No session cookies or authorization headers are sent to image hosts.
	response, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("no se pudo descargar la ilustración")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, "", errors.New("ilustración no disponible")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (12<<20)+1))
	if err != nil || len(data) > 12<<20 {
		return nil, "", errors.New("ilustración demasiado grande o incompleta")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 60_000_000 {
		return nil, "", errors.New("formato de imagen no admitido")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", errors.New("imagen incompleta")
	}
	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	return data, ext, nil
}
func saveArtwork(ctx context.Context, root, raw string) (string, error) {
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 4 || !artworkURL(r.URL.String()) {
			return errors.New("redirección de ilustración no permitida")
		}
		return nil
	}}
	data, ext, err := fetchArtwork(ctx, client, raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + ext
	path := filepath.Join(root, "artwork", name)
	if err = checkOutputPath(root, path); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if old, err := os.ReadFile(path); err == nil {
		if sha256.Sum256(old) != sum {
			return "", errors.New("conflicto con ilustración existente")
		}
		return "artwork/" + name, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ok = true
	return "artwork/" + name, nil
}

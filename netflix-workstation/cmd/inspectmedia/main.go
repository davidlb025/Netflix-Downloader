package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	session := flag.String("session", "", "Sesión DPAPI local")
	output := flag.String("out", "research/media-private", "Carpeta de pistas cifradas para la investigación")
	profile := flag.String("profile", "", "Perfil AVC concreto del manifiesto")
	prefix := flag.Bool("prefix", false, "Descargar solo el prefijo y primer fragmento cifrado")
	id := flag.Int("id", 80077368, "ID de reproducción")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cookies, err := netflixlib.ReadSession(*session)
	if err != nil {
		return err
	}
	client, err := netflixlib.NewForPlatform(ctx, cookies, "linux")
	if err != nil {
		return err
	}
	playback, err := client.Manifest(ctx, *id)
	if err != nil {
		return client.Redact(err)
	}
	video, err := playback.SelectVideoProfile(540, *profile)
	if err != nil {
		return err
	}
	audio, err := playback.SelectAudio("en")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*output, 0700); err != nil {
		return err
	}
	for i, track := range []netflixlib.NetflixTrack{video, audio} {
		if *prefix && i > 0 {
			break
		}
		path := filepath.Join(*output, fmt.Sprintf("track-%d.enc.mp4", i))
		if _, err = os.Stat(path); os.IsNotExist(err) {
			if *prefix {
				err = client.DownloadEncryptedProbe(ctx, track, path)
			} else {
				err = client.DownloadTrack(ctx, track, path, nil)
			}
			if err != nil {
				return err
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		info, _ := file.Stat()
		var boxes []map[string]any
		for offset := int64(0); offset < info.Size(); {
			var header [16]byte
			if _, err = file.ReadAt(header[:8], offset); err != nil {
				file.Close()
				return err
			}
			size := int64(binary.BigEndian.Uint32(header[:4]))
			headerSize := int64(8)
			if size == 1 {
				if _, err = file.ReadAt(header[8:], offset+8); err != nil {
					return err
				}
				size = int64(binary.BigEndian.Uint64(header[8:]))
				headerSize = 16
			}
			if size == 0 {
				size = info.Size() - offset
			}
			if size < headerSize || size > info.Size()-offset {
				file.Close()
				return io.ErrUnexpectedEOF
			}
			boxes = append(boxes, map[string]any{"type": string(header[4:8]), "offset": offset, "bytes": size})
			offset += size
		}
		file.Close()
		status, _ := json.Marshal(map[string]any{"track": i, "bytes": info.Size(), "height": track.Height, "profile": track.Profile, "boxes": boxes})
		fmt.Println(string(status))
	}
	return nil
}

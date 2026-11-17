package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// This operation downloads text files only. It never requests a content license,
// invokes a CDM, or reads/decrypts the saved video.
func (b *backend) fetchSubtitles(raw json.RawMessage) (bool, error) {
	var request struct{ Key string }
	if json.Unmarshal(raw, &request) != nil {
		return false, errors.New("selecciona un vídeo completado")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var job *Job
	for _, j := range b.jobs {
		if j.Key == request.Key {
			job = j
			break
		}
	}
	if job == nil || job.State != "complete" || job.Path == "" {
		return false, errors.New("selecciona un vídeo completado")
	}
	if job.SubtitleBusy {
		return false, errors.New("ya se están guardando sus subtítulos")
	}
	if info, err := os.Stat(job.Path); err != nil || !info.Mode().IsRegular() {
		return false, errors.New("el vídeo se ha movido o eliminado")
	}
	job.SubtitleBusy = true
	job.Detail = "Buscando los subtítulos elegidos…"
	mode, languages := job.SubtitleMode, append([]string(nil), job.SubtitleLanguages...)
	anchor := filepath.Join(filepath.Dir(job.Path), "Subtitulos", filepath.Base(job.Path))
	if job.ManifestPath != "" {
		anchor = filepath.Join(filepath.Dir(job.ManifestPath), "Subtitulos", filepath.Base(job.Path))
	}
	if job.SubtitleAnchor != "" {
		anchor = job.SubtitleAnchor
	}
	videos := append([]VideoFile(nil), job.VideoFiles...)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(b.ctx, 12*time.Minute)
		defer cancel()
		result, err := b.downloadSubtitlesSelected(ctx, job.ID, anchor, mode, languages)
		b.mu.Lock()
		defer b.mu.Unlock()
		job.SubtitleDirectory = filepath.Dir(anchor)
		if err != nil {
			job.Detail = "MP4 comprobado · Subtítulos: " + err.Error()
		} else {
			known := map[string]bool{}
			for _, path := range job.Subtitles {
				known[path] = true
			}
			for _, path := range result.Paths {
				if !known[path] {
					job.Subtitles = append(job.Subtitles, path)
					known[path] = true
				}
			}
			if len(videos) > 0 {
				for _, video := range videos {
					addSubtitles(job, result, video.Path)
				}
			} else {
				addSubtitles(job, result, job.Path)
			}
			job.SubtitleTotal = result.Total
			job.SubtitleFailed = result.Failed
			if result.Total == 0 {
				job.Detail = "MP4 comprobado · Netflix no devuelve pistas de subtítulos de texto disponibles"
			} else {
				job.Detail = fmt.Sprintf("MP4 comprobado · Subtítulos guardados: %d/%d", len(result.Paths), result.Total)
				if result.Failed > 0 {
					job.Detail += " · Algunas pistas fallaron; puedes volver a intentarlo"
				}
			}
		}
		if err == nil {
			if indexError := writeMediaIndex(job, true); indexError != nil {
				job.Detail += " · No se pudo actualizar pistas.json: " + indexError.Error()
			}
		}
		if err == nil && job.LibraryVersion == 2 {
			data, _ := json.Marshal(job)
			settings := b.settings
			b.mu.Unlock()
			var exported Job
			json.Unmarshal(data, &exported)
			if exportErr := b.exportLibrary(ctx, &exported, settings); exportErr != nil {
				exported.ExportWarnings = append(exported.ExportWarnings, exportErr.Error())
				exported.Detail += " · No se pudo actualizar la ficha: " + exportErr.Error()
			}
			b.mu.Lock()
			*job = exported
		}
		job.SubtitleBusy = false
		b.persist()
	}()
	return true, nil
}
func (b *backend) downloadSubtitles(ctx context.Context, id int, path string) (netflixlib.CaptionResult, error) {
	return b.downloadSubtitlesSelected(ctx, id, path, "all", nil)
}
func (b *backend) downloadSubtitlesSelected(ctx context.Context, id int, path, mode string, languages []string) (netflixlib.CaptionResult, error) {
	cookies, err := netflixlib.ReadSession(b.sessionPath())
	if err != nil {
		return netflixlib.CaptionResult{}, errors.New("guarda tu sesión en Ajustes")
	}
	client, err := netflixlib.NewForPlatform(ctx, cookies, "linux")
	if err != nil {
		return netflixlib.CaptionResult{}, errors.New("no se pudo consultar Netflix; revisa la sesión")
	}
	playback, err := client.Manifest(ctx, id)
	if err != nil {
		return netflixlib.CaptionResult{}, errors.New("Netflix no devolvió los subtítulos; revisa la sesión o inténtalo de nuevo")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return netflixlib.CaptionResult{}, errors.New("no se pudo crear la carpeta Subtitulos")
	}
	return netflixlib.SaveAll(ctx, netflixlib.Select(netflixlib.Tracks(playback.Value), mode, languages), path, nil), nil
}

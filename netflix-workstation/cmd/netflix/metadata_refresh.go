package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"encoding/json"
	"errors"
	"time"
)

func (b *backend) refreshMetadata(raw json.RawMessage) (bool, error) {
	var request struct{ Key string }
	if json.Unmarshal(raw, &request) != nil {
		return false, errors.New("selecciona una descarga")
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
	if job == nil || job.State != "complete" || job.LibraryVersion != 2 {
		return false, errors.New("esta operación requiere una descarga con el sistema de biblioteca 0.6")
	}
	if job.SubtitleBusy {
		return false, errors.New("ya se están actualizando sus archivos")
	}
	job.SubtitleBusy = true
	job.Detail = "Actualizando ficha e ilustraciones…"
	data, _ := json.Marshal(job)
	settings := b.settings
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(b.ctx, 4*time.Minute)
		defer cancel()
		var copy Job
		json.Unmarshal(data, &copy)
		if !identified(&copy) {
			// Unknown identities stay pending. Moving existing editions is an explicit
			// migration, not a side effect of refreshing a card.
			copy.ExportWarnings = []string{"Identidad pendiente: requiere una migración explícita; no se han movido los archivos"}
		} else {
			copy.ExportWarnings = nil
			if details, err := netflixlib.FetchDetails(ctx, copy.ID); err == nil && details.Details.Kind == copy.Metadata.Kind && details.Details.SeriesID == copy.Metadata.SeriesID && details.Details.SeasonID == copy.Metadata.SeasonID && details.Details.SeasonNumber == copy.Metadata.SeasonNumber && details.Details.EpisodeNumber == copy.Metadata.EpisodeNumber {
				copy.Metadata = details.Details
				if details.Series != nil {
					copy.SeriesMetadata = details.Series.Metadata
				}
			}
			if err := b.exportLibrary(ctx, &copy, settings); err != nil {
				copy.ExportWarnings = append(copy.ExportWarnings, err.Error())
			}
		}
		copy.SubtitleBusy = false
		copy.Detail = "Ficha MediaCenter actualizada"
		if len(copy.ExportWarnings) > 0 {
			copy.Detail = "Archivos conservados · Revisa la exportación: " + copy.ExportWarnings[0]
		}
		b.mu.Lock()
		*job = copy
		b.persist()
		b.mu.Unlock()
	}()
	return true, nil
}

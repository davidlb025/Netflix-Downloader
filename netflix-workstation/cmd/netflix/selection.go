package main

import (
		netflixlib "local.netflix/netflix-downloader"
"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SelectionRequest struct {
	ID                        int
	URL, Title, Audio, Series string
	Height                    int
	EpisodeIDs                []int
	AudioLanguages            []string
	SubtitleMode              string
	SubtitleLanguages         []string
}
type QueueResult struct {
	Queued  int `json:"queued"`
	Skipped int `json:"skipped"`
}

func (b *backend) enqueueSelection(raw json.RawMessage) (QueueResult, error) {
	var value SelectionRequest
	if json.Unmarshal(raw, &value) != nil {
		return QueueResult{}, errors.New("selección de episodios no válida")
	}
	value.URL = strings.TrimSpace(value.URL)
	if len(value.EpisodeIDs) == 0 && value.ID > 0 {
		value.EpisodeIDs = []int{value.ID}
	}
	if len(value.AudioLanguages) == 0 && value.Audio != "" {
		value.AudioLanguages = []string{value.Audio}
	}
	if value.SubtitleMode == "" {
		value.SubtitleMode = "all"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.analysis
	linkID, err := idFrom(value.URL)
	if err != nil || a.ID != linkID || a.Busy || a.Error != "" || a.Source != value.URL {
		return QueueResult{}, errors.New("el enlace ha cambiado; analiza el nuevo enlace antes de añadirlo")
	}
	if len(value.EpisodeIDs) == 0 || len(value.AudioLanguages) == 0 {
		return QueueResult{}, errors.New("selecciona al menos un episodio y un idioma de audio")
	}
	if !containsInt(a.Heights, value.Height) {
		return QueueResult{}, errors.New("elige una calidad disponible")
	}
	if value.SubtitleMode != "all" && value.SubtitleMode != "none" && value.SubtitleMode != "selected" {
		return QueueResult{}, errors.New("selección de subtítulos no válida")
	}
	if value.SubtitleMode == "selected" && len(value.SubtitleLanguages) == 0 {
		return QueueResult{}, errors.New("elige idiomas de subtítulos o selecciona Sin subtítulos")
	}
	for _, language := range value.SubtitleLanguages {
		if !containsString(a.SubtitleLanguages, language) {
			return QueueResult{}, errors.New("algún idioma de subtítulos ya no está disponible")
		}
	}
	type episode struct {
		title, series string
		season        int
		number        float64
		metadata      netflixlib.Details
	}
	allowed := map[int]episode{}
	if a.Kind == "series" && a.Series != nil {
		for _, season := range a.Series.Seasons {
			for _, e := range season.Episodes {
				allowed[e.ID] = episode{e.Title, a.Series.Title, season.Number, e.Number, e.Metadata}
			}
		}
	} else {
		title := strings.TrimSpace(value.Title)
		if title == "" {
			title = a.Title
		}
		series := strings.TrimSpace(value.Series)
		if series == "" {
			series = a.SeriesName
		}
		allowed[a.ID] = episode{title, series, a.Season, a.Episode, a.Metadata}
	}
	seenEpisodes, seenAudio := map[int]bool{}, map[string]bool{}
	for _, id := range value.EpisodeIDs {
		if _, ok := allowed[id]; !ok || seenEpisodes[id] {
			return QueueResult{}, errors.New("la selección contiene episodios ajenos a esta serie o repetidos")
		}
		seenEpisodes[id] = true
	}
	for _, language := range value.AudioLanguages {
		if !containsString(a.Audio, language) || seenAudio[language] {
			return QueueResult{}, errors.New("elige idiomas de audio disponibles y sin repetir")
		}
		seenAudio[language] = true
	}
	if len(value.EpisodeIDs)*len(value.AudioLanguages) > 128 {
		return QueueResult{}, errors.New("selecciona un máximo de 128 combinaciones de episodio y audio por vez")
	}
	if len(b.deps(b.settings)) > 0 {
		return QueueResult{}, errors.New("completa los requisitos del motor en Ajustes")
	}
	var pending []*Job
	result := QueueResult{}
	for _, id := range value.EpisodeIDs {
		e := allowed[id]
		j := &Job{LibraryVersion: 2, LibraryRoot: b.settings.Output, Metadata: e.metadata, SeriesMetadata: a.SeriesMetadata, ID: id, Title: e.title, Series: e.series, Season: e.season, Episode: e.number, Height: value.Height, Audio: value.AudioLanguages[0], AudioLanguages: append([]string(nil), value.AudioLanguages...), MultiAudio: len(value.AudioLanguages) > 1, SeriesFoldersByName: b.settings.SeriesFoldersByName, IndependentAudioMP4: b.settings.IndependentAudioMP4, State: "queued", Detail: "En cola", Created: time.Now().Format(time.RFC3339), SubtitleMode: value.SubtitleMode, SubtitleLanguages: append([]string(nil), value.SubtitleLanguages...)}
		var key [8]byte
		if _, err := rand.Read(key[:]); err != nil {
			return QueueResult{}, errors.New("no se pudo crear la descarga")
		}
		j.Key = hex.EncodeToString(key[:])
		if j.Metadata.Poster == "" {
			j.Metadata.Poster = a.SeriesMetadata.Poster
		}
		if j.Metadata.Backdrop == "" {
			j.Metadata.Backdrop = a.SeriesMetadata.Backdrop
		}
		if j.Metadata.Logo == "" {
			j.Metadata.Logo = a.SeriesMetadata.Logo
		}

		_, err := jobLayout(b.settings.Output, j)
		if err != nil {
			return QueueResult{}, err
		}
		exists := false
		for _, old := range b.jobs {
			if old.ID != id {
				continue
			}
			if (!terminal(old.State) || old.SubtitleBusy) && overlappingAudio(jobLanguages(old), value.AudioLanguages) {
				exists = true
				break
			}
			if old.State == "complete" && old.Height == value.Height && sameLanguages(jobLanguages(old), value.AudioLanguages) && withinRoot(b.settings.Output, old.Path) && jobAssetsExist(old) {
				exists = true
				break
			}
		}
		if exists {
			result.Skipped++
			continue
		}
		pending = append(pending, j)
	}
	if len(pending) > cap(b.pending)-len(b.pending) {
		return QueueResult{}, fmt.Errorf("la cola solo tiene sitio para %d descargas más", cap(b.pending)-len(b.pending))
	}
	all := append(append([]*Job(nil), b.jobs...), pending...)
	if err := atomicJSON(b.historyPath(), all); err != nil {
		return QueueResult{}, errors.New("no se pudo guardar la cola")
	}
	b.jobs = all
	for _, j := range pending {
		b.pending <- j
	}
	result.Queued = len(pending)
	return result, nil
}
func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func containsInt(values []int, value int) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

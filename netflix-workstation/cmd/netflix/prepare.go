package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"fmt"
	"sort"
	"time"
)

func (b *backend) analyze(id int, source string) {
	defer b.wg.Done()
	result := Analysis{ID: id, Source: source, Kind: "unknown", Title: fmt.Sprintf("Netflix %d", id)}
	ctx, cancel := context.WithTimeout(b.ctx, 90*time.Second)
	defer cancel()
	defer func() { b.mu.Lock(); b.analysis = result; b.mu.Unlock() }()
	metadataContext, stop := context.WithTimeout(ctx, 25*time.Second)
	details, metadataError := netflixlib.FetchDetails(metadataContext, id)
	stop()
	probeID := id
	if metadataError == nil {
		result.Metadata = details.Details
		result.Kind = details.Details.Kind
		result.Title = details.Details.Title
		result.SeriesName = details.Details.SeriesTitle
		result.Season = details.Details.SeasonNumber
		result.Episode = details.Details.EpisodeNumber
		if details.Series != nil {
			result.SeriesMetadata = details.Series.Metadata
			b.rememberCatalog(*details.Series)
			if result.Kind == "series" {
				result.Series = details.Series
				result.SeriesName = details.Series.Title
				for _, season := range details.Series.Seasons {
					if len(season.Episodes) > 0 {
						probeID = season.Episodes[0].ID
						break
					}
				}
			}
		}
	} else if cached, ok := b.cachedTitle(id); ok {
		result.Title = cached.Title
		result.SeriesName = cached.Series
		result.Season = cached.Season
		result.Episode = cached.Episode
		result.Metadata = cached.Metadata
		result.SeriesMetadata = cached.SeriesMetadata
		if cached.Metadata.Kind != "" {
			result.Kind = cached.Metadata.Kind
		}
	}
	cookies, err := netflixlib.ReadSession(b.sessionPath())
	if err != nil {
		result.Error = "Guarda tu sesión en Ajustes para consultar los idiomas disponibles."
		return
	}
	client, err := netflixlib.NewForPlatform(ctx, cookies, "linux")
	if err != nil {
		result.Error = err.Error()
		return
	}
	playback, err := client.Manifest(ctx, probeID)
	if err != nil {
		result.Error = client.Redact(err).Error()
		return
	}
	result.Heights = playback.DownloadHeights()
	if len(result.Heights) == 0 {
		result.Error = "Netflix no ofrece una calidad compatible con el motor actual para este contenido."
		return
	}
	result.Audio = playback.AudioLanguages()
	languages := map[string]bool{}
	for _, track := range netflixlib.Tracks(playback.Value) {
		languages[track.Language] = true
	}
	for language := range languages {
		result.SubtitleLanguages = append(result.SubtitleLanguages, language)
	}
	sort.Strings(result.SubtitleLanguages)
}

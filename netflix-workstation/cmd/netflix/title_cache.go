package main

import (
		netflixlib "local.netflix/netflix-downloader"
"encoding/json"
	"os"
	"path/filepath"
)

type titleMetadata struct {
	Metadata       netflixlib.Details
	SeriesMetadata netflixlib.Details
	ID             int
	Title, Series  string
	Season         int
	Episode        float64
}

func (b *backend) cachedTitle(id int) (titleMetadata, bool) {
	data, err := os.ReadFile(filepath.Join(b.home, "title-index.json"))
	if err != nil || len(data) > 4<<20 {
		return titleMetadata{}, false
	}
	var index map[int]titleMetadata
	if json.Unmarshal(data, &index) != nil {
		return titleMetadata{}, false
	}
	metadata, ok := index[id]
	return metadata, ok && metadata.ID == id && metadata.Title != ""
}
func (b *backend) rememberCatalog(series netflixlib.Series) {
	path := filepath.Join(b.home, "title-index.json")
	index := map[int]titleMetadata{}
	if data, err := os.ReadFile(path); err == nil && len(data) <= 4<<20 {
		json.Unmarshal(data, &index)
	}
	if index == nil {
		index = map[int]titleMetadata{}
	}
	for _, season := range series.Seasons {
		for _, episode := range season.Episodes {
			index[episode.ID] = titleMetadata{ID: episode.ID, Title: episode.Title, Series: series.Title, Season: season.Number, Episode: episode.Number, Metadata: episode.Metadata, SeriesMetadata: series.Metadata}
		}
	}
	if len(index) <= 10000 {
		atomicJSON(path, index)
	}
}

package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"encoding/json"
	"errors"
	"time"
)

type CatalogState struct {
	Busy   bool           `json:"busy"`
	Source string         `json:"source"`
	Error  string         `json:"error"`
	Series netflixlib.Series `json:"series"`
}

func (b *backend) discover(raw json.RawMessage) (bool, error) {
	var value struct{ URL string }
	if json.Unmarshal(raw, &value) != nil {
		return false, errors.New("pega la ficha de una serie")
	}
	id, err := idFrom(value.URL)
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	if b.catalog.Busy {
		b.mu.Unlock()
		return false, errors.New("ya se están consultando las temporadas")
	}
	b.catalog = CatalogState{Busy: true, Source: value.URL}
	b.mu.Unlock()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(b.ctx, 40*time.Second)
		defer cancel()
		series, err := netflixlib.Fetch(ctx, id)
		result := CatalogState{Source: value.URL, Series: series}
		if err != nil {
			result.Error = err.Error()
		}
		b.mu.Lock()
		b.catalog = result
		b.mu.Unlock()
	}()
	return true, nil
}


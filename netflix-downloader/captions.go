// Package captions saves clear text subtitle files. It rejects encrypted and
// binary resources and does not interact with a CDM or a license server.
package netflix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CaptionTrack struct{ Language, Kind, Format, URL, ID, Name string }
type SavedTrack struct {
	Path, Language, Kind, Format, ID, Name string
	Created                                bool
}

// Empty mode preserves the all-languages preference of older saved jobs.
func Select(tracks []CaptionTrack, mode string, languages []string) []CaptionTrack {
	if mode == "none" {
		return nil
	}
	if mode == "" || mode == "all" {
		return tracks
	}
	allowed := map[string]bool{}
	for _, language := range languages {
		allowed[language] = true
	}
	var selected []CaptionTrack
	for _, track := range tracks {
		if allowed[track.Language] {
			selected = append(selected, track)
		}
	}
	return selected
}

type CaptionResult struct {
	Paths  []string
	Tracks []SavedTrack
	Failed int
	Total  int
}

func Tracks(value any) []CaptionTrack {
	var items []any
	var find func(any, int)
	find = func(v any, depth int) {
		if depth > 16 || items != nil {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for _, key := range []string{"timedtexttracks", "timedtext_tracks"} {
				if a, ok := x[key].([]any); ok {
					items = a
					return
				}
			}
			for _, v := range x {
				find(v, depth+1)
			}
		case []any:
			for _, v := range x {
				find(v, depth+1)
			}
		}
	}
	find(value, 0)
	var result []CaptionTrack
	seen := map[string]bool{}
	for _, item := range items {
		x := obj(item)
		if flag(x["isNoneTrack"]) || flag(x["isEncrypted"]) || flag(x["encrypted"]) {
			continue
		}
		lang := str(x["language"])
		if lang == "" {
			continue
		}
		kind := "full"
		if flag(x["isForcedNarrative"]) {
			kind = "forced"
		} else if flag(x["isSDH"]) || strings.Contains(strings.ToLower(str(x["rawTrackType"])), "caption") {
			kind = "sdh"
		}
		downloadables := obj(x["ttDownloadables"])
		var profiles []string
		for profile := range downloadables {
			profiles = append(profiles, profile)
		}
		sort.Slice(profiles, func(i, j int) bool {
			return rank(profiles[i]) < rank(profiles[j]) || rank(profiles[i]) == rank(profiles[j]) && profiles[i] < profiles[j]
		})
		for _, profile := range profiles {
			format := ""
			if strings.Contains(profile, "vtt") {
				format = "vtt"
			} else if strings.Contains(profile, "dfxp") || strings.Contains(profile, "ttml") {
				format = "ttml"
			}
			if format == "" {
				continue
			}
			d := obj(downloadables[profile])
			if flag(d["isEncrypted"]) || flag(d["encrypted"]) {
				continue
			}
			address := firstURL(d["downloadUrls"])
			if address == "" {
				address = firstURL(d["urls"])
			}
			parsed, err := url.Parse(address)
			if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
				continue
			}
			id := str(x["new_track_id"])
			if id == "" {
				id = str(x["trackId"])
			}
			if id == "" {
				id = lang + "/" + kind + "/" + address
			}
			hash := sha256.Sum256([]byte(id))
			tag := hex.EncodeToString(hash[:4])
			key := lang + "/" + kind + "/" + tag
			if !seen[key] {
				name := str(x["languageDescription"])
				if name == "" || (kind == "forced" && (strings.EqualFold(name, "off") || strings.EqualFold(name, "desactivado"))) {
					name = Name(lang)
				}
				result = append(result, CaptionTrack{Language: lang, Kind: kind, Format: format, URL: address, ID: tag, Name: name})
				seen[key] = true
			}
			break
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Language+result[i].Kind+result[i].ID < result[j].Language+result[j].Kind+result[j].ID
	})
	return result
}
func rank(profile string) int {
	if strings.Contains(profile, "vtt") {
		return 0
	}
	return 1
}
func obj(v any) map[string]any { x, _ := v.(map[string]any); return x }
func str(v any) string         { x, _ := v.(string); return x }
func flag(v any) bool          { x, _ := v.(bool); return x }
func firstURL(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if address := str(x["url"]); address != "" {
			return address
		}
		var keys []string
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if address := firstURL(x[key]); address != "" {
				return address
			}
		}
	case []any:
		for _, v := range x {
			if address := firstURL(v); address != "" {
				return address
			}
		}
	}
	return ""
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func SaveAll(ctx context.Context, tracks []CaptionTrack, videoPath string, client *http.Client) CaptionResult {
	result := CaptionResult{Total: len(tracks)}
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 || req.URL.Scheme != "https" {
				return errors.New("redirección de subtítulos no permitida")
			}
			return nil
		}}
	}
	for _, track := range tracks {
		if ctx.Err() != nil {
			result.Failed += len(tracks) - len(result.Paths) - result.Failed
			break
		}
		stem := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
		tag := unsafeName.ReplaceAllString(track.Language+"."+track.Kind+"."+track.ID, "_")
		if len(tag) > 96 {
			tag = tag[:96]
		}
		path := stem + "." + tag + "." + track.Format
		created := false
		if err := saveTracked(ctx, client, track, path, &created); err != nil {
			result.Failed++
			continue
		}
		result.Paths = append(result.Paths, path)
		name := track.Name
		if name == "" {
			name = Name(track.Language)
		}
		result.Tracks = append(result.Tracks, SavedTrack{Path: path, Language: track.Language, Kind: track.Kind, Format: track.Format, ID: track.ID, Name: name, Created: created})
	}
	return result
}
func save(ctx context.Context, client *http.Client, track CaptionTrack, path string) error {
	return saveTracked(ctx, client, track, path, nil)
}
func saveTracked(ctx context.Context, client *http.Client, track CaptionTrack, path string, created *bool) error {
	address, err := url.Parse(track.URL)
	if err != nil || address.Scheme != "https" || address.Hostname() == "" || address.User != nil {
		return errors.New("URL de subtítulos no válida")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, track.URL, nil)
	if err != nil {
		return errors.New("no se pudo preparar el subtítulo")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("no se pudo descargar el subtítulo")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("subtítulo: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (12<<20)+1))
	if err != nil || len(data) > 12<<20 {
		return errors.New("el subtítulo supera el tamaño permitido o está incompleto")
	}
	clean := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	valid := track.Format == "vtt" && bytes.HasPrefix(clean, []byte("WEBVTT")) || track.Format == "ttml" && (bytes.Contains(clean, []byte("<tt ")) || bytes.Contains(clean, []byte("<tt>")) || bytes.Contains(clean, []byte("<tt:")))
	if !valid || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("el recurso no es un subtítulo de texto compatible")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			if previous, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(previous, data) {
				return nil
			}
		}
		return err
	}
	ok := false
	if created != nil {
		*created = true
	}
	defer func() {
		file.Close()
		if !ok {
			os.Remove(path)
			if created != nil {
				*created = false
			}
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

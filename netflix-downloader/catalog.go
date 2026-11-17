// Package catalog reads public title descriptions. It never sends session cookies
// or calls playback, license or DRM endpoints.
package netflix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Episode struct {
	ID       int     `json:"id"`
	Number   float64 `json:"number"`
	Title    string  `json:"title"`
	Seconds  int     `json:"seconds"`
	Metadata Details `json:"metadata"`
}
type Season struct {
	ID          int       `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Episodes    []Episode `json:"episodes"`
	NumberKnown bool      `json:"numberKnown"`
}
type Series struct {
	ID       int      `json:"id"`
	Title    string   `json:"title"`
	Seasons  []Season `json:"seasons"`
	Metadata Details  `json:"metadata"`
}

func Fetch(ctx context.Context, id int) (Series, error) {
	return fetch(ctx, id, false)
}

// FetchTitle also resolves an episode to its publicly listed containing show.
func FetchTitle(ctx context.Context, id int) (Series, error) { return fetch(ctx, id, true) }

func fetch(ctx context.Context, id int, containing bool) (Series, error) {
	if id < 100000 || id > 999999999999 {
		return Series{}, errors.New("ID de serie no válido")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://www.netflix.com/es/title/%d", id), nil)
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || (req.URL.Hostname() != "www.netflix.com" && req.URL.Hostname() != "netflix.com") {
			return errors.New("redirección de catálogo no permitida")
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		return Series{}, errors.New("no se pudo leer la ficha pública de Netflix; vuelve a intentarlo")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Series{}, fmt.Errorf("ficha pública: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 24<<20))
	if err != nil {
		return Series{}, errors.New("la ficha pública se interrumpió")
	}
	return parse(data, id, containing)
}

// javascriptJSON accepts the \xHH string escapes used by the public page, while
// preserving escaped backslashes. It does not execute JavaScript.
func javascriptJSON(data []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			if data[i+1] == 'x' && i+3 < len(data) {
				if _, err := strconv.ParseUint(string(data[i+2:i+4]), 16, 8); err == nil {
					out.WriteString("\\u00")
					out.Write(data[i+2 : i+4])
					i += 3
					continue
				}
			}
			out.WriteByte(data[i])
			i++
			out.WriteByte(data[i])
			continue
		}
		out.WriteByte(data[i])
	}
	return out.Bytes()
}
func CatalogParse(data []byte, id int) (Series, error) {
	return parse(data, id, false)
}
func parse(data []byte, id int, containing bool) (Series, error) {
	marker := []byte("netflix.reactContext")
	start := bytes.Index(data, marker)
	if start < 0 {
		return Series{}, errors.New("Netflix no ha publicado datos de temporadas en esta ficha")
	}
	rest := data[start+len(marker):]
	start = bytes.IndexByte(rest, '=')
	if start < 0 {
		return Series{}, errors.New("formato de catálogo no reconocido")
	}
	var root any
	if err := json.NewDecoder(bytes.NewReader(javascriptJSON(rest[start+1:]))).Decode(&root); err != nil {
		return Series{}, errors.New("el formato público de Netflix ha cambiado; no se han inventado episodios")
	}
	var found map[string]any
	var visit func(any, int)
	visit = func(v any, depth int) {
		if depth > 48 || found != nil {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			matches := n(x["videoId"]) == id
			if containing && !matches && x["seasons"] != nil {
				for _, season := range edges(x["seasons"]) {
					for _, episode := range edges(catalogObject(season)["episodes"]) {
						if n(catalogObject(episode)["videoId"]) == id {
							matches = true
						}
					}
				}
			}
			if matches && x["seasons"] != nil {
				found = x
				return
			}
			for _, child := range x {
				visit(child, depth+1)
			}
		case []any:
			for _, child := range x {
				visit(child, depth+1)
			}
		}
	}
	visit(root, 0)
	if found == nil {
		return Series{}, errors.New("este enlace no contiene temporadas; pega la ficha de la serie, no la de un episodio")
	}
	result := Series{ID: n(found["videoId"]), Title: s(found["title"]), Metadata: detailsNode(found)}
	result.Metadata.Kind = "series"
	seen := map[int]bool{}
	for _, item := range edges(found["seasons"]) {
		x := catalogObject(item)
		number, known := seasonNumber(x)
		season := Season{ID: n(x["videoId"]), Number: number, NumberKnown: known, Title: s(x["numberLabelV2"])}
		if season.Title == "" {
			if known {
				season.Title = fmt.Sprintf("Temporada %d", season.Number)
			} else {
				season.Title = "Temporada sin número"
			}
		}
		for _, item := range edges(x["episodes"]) {
			e := catalogObject(item)
			details := detailsNode(e)
			details.Kind = "episode"
			details.NumberingKnown = known && f(e["number"]) > 0
			details.Poster = result.Metadata.Poster
			details.Backdrop = result.Metadata.Backdrop
			details.Logo = result.Metadata.Logo
			details.Genres = append([]string(nil), result.Metadata.Genres...)
			details.SeriesID = result.ID
			details.SeriesTitle = result.Title
			details.SeasonID = season.ID
			details.SeasonNumber = season.Number
			details.EpisodeNumber = f(e["number"])
			episode := Episode{ID: n(e["videoId"]), Number: f(e["number"]), Title: s(e["title"]), Seconds: n(e["runtimeSec"]), Metadata: details}
			if episode.ID < 100000 || seen[episode.ID] {
				continue
			}
			seen[episode.ID] = true
			season.Episodes = append(season.Episodes, episode)
		}
		if season.ID > 0 {
			result.Seasons = append(result.Seasons, season)
		}
	}
	if len(result.Seasons) == 0 {
		return Series{}, errors.New("la ficha no contiene temporadas disponibles")
	}
	return result, nil
}

var seasonLabelNumber = regexp.MustCompile(`(?i)(?:temporada|season|serie|series)\s*(\d+)`)

func seasonNumber(value map[string]any) (int, bool) {
	for _, key := range []string{"number", "seasonNumber"} {
		if raw, ok := value[key].(float64); ok && raw >= 0 && raw <= 100000 {
			return int(raw), true
		}
	}
	label := strings.TrimSpace(s(value["numberLabelV2"]))
	lower := strings.ToLower(label)
	if strings.Contains(lower, "especial") || strings.Contains(lower, "special") {
		return 0, true
	}
	if match := seasonLabelNumber.FindStringSubmatch(label); len(match) == 2 {
		number, err := strconv.Atoi(match[1])
		return number, err == nil
	}
	return 0, false
}
func catalogObject(v any) map[string]any { x, _ := v.(map[string]any); return x }
func n(v any) int                 { x, _ := v.(float64); return int(x) }
func f(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		value, _ := x.Float64()
		return value
	case string:
		value, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return value
	default:
		return 0
	}
}
func s(v any) string { x, _ := v.(string); return strings.TrimSpace(x) }
func edges(v any) []any {
	var out []any
	items, _ := catalogObject(v)["edges"].([]any)
	for _, item := range items {
		if x := catalogObject(item)["node"]; x != nil {
			out = append(out, x)
		}
	}
	return out
}

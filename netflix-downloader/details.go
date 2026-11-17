package netflix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type Details struct {
	ID             int      `json:"id"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	OriginalTitle  string   `json:"originalTitle,omitempty"`
	Year           int      `json:"year,omitempty"`
	Seconds        int      `json:"seconds,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	Poster         string   `json:"poster,omitempty"`
	Backdrop       string   `json:"backdrop,omitempty"`
	Logo           string   `json:"logo,omitempty"`
	Thumbnail      string   `json:"thumbnail,omitempty"`
	SeriesID       int      `json:"seriesId,omitempty"`
	SeriesTitle    string   `json:"seriesTitle,omitempty"`
	SeasonID       int      `json:"seasonId,omitempty"`
	SeasonNumber   int      `json:"seasonNumber,omitempty"`
	NumberingKnown bool     `json:"numberingKnown"`
	EpisodeNumber  float64  `json:"episodeNumber,omitempty"`
	Source         string   `json:"source"`
	FetchedAt      string   `json:"fetchedAt"`
}
type TitleDetails struct {
	Details Details `json:"details"`
	Series  *Series `json:"series,omitempty"`
}

func FetchDetails(ctx context.Context, id int) (TitleDetails, error) {
	if id < 100000 || id > 999999999999 {
		return TitleDetails{}, errors.New("ID de ficha no válido")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://www.netflix.com/es/title/%d", id), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || r.URL.Scheme != "https" || (r.URL.Hostname() != "www.netflix.com" && r.URL.Hostname() != "netflix.com") {
			return errors.New("redirección de ficha no permitida")
		}
		return nil
	}}
	response, err := client.Do(req)
	if err != nil {
		return TitleDetails{}, errors.New("no se pudo consultar la ficha pública")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return TitleDetails{}, fmt.Errorf("ficha pública: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (24<<20)+1))
	if err != nil || len(data) > 24<<20 {
		return TitleDetails{}, errors.New("ficha pública incompleta o demasiado grande")
	}
	return ParseDetails(data, id)
}

func publicRoot(data []byte) (any, error) {
	start := bytes.Index(data, []byte("netflix.reactContext"))
	if start < 0 {
		return nil, errors.New("no se encontró el catálogo público")
	}
	rest := data[start+len("netflix.reactContext"):]
	start = bytes.IndexByte(rest, '=')
	if start < 0 {
		return nil, errors.New("formato de ficha no reconocido")
	}
	var root any
	err := json.NewDecoder(bytes.NewReader(javascriptJSON(rest[start+1:]))).Decode(&root)
	return root, err
}

func detailsNode(node map[string]any) Details {
	kind := map[string]string{"Show": "series", "Movie": "movie", "Episode": "episode"}[s(node["__typename"])]
	d := Details{ID: n(node["videoId"]), Kind: kind, Title: s(node["title"]), Description: s(node["shortSynopsis"]), OriginalTitle: s(node["originalTitle"]), Seconds: n(node["runtimeSec"])}
	for _, key := range []string{"releaseYear", "year", "startYear"} {
		if year := n(node[key]); year >= 1880 && year <= 9999 {
			d.Year = year
			break
		}
	}
	if d.Kind == "movie" && d.Year == 0 {
		d.Year = n(node["latestYear"])
	}
	for _, key := range []string{"coreGenres", "genres", "primaryGenres"} {
		for _, edge := range edges(node[key]) {
			name := s(catalogObject(edge)["name"])
			if name == "" {
				name = s(catalogObject(edge)["title"])
			}
			if name != "" && !has(d.Genres, name) {
				d.Genres = append(d.Genres, name)
			}
		}
		if len(d.Genres) > 0 {
			break
		}
	}
	for _, key := range []string{"boxshot600", "boxshot300", "boxart", "boxArt", "poster"} {
		if address := s(catalogObject(node[key])["url"]); address != "" {
			d.Poster = address
			break
		}
	}
	for _, key := range []string{"eclipseBillboardRedux1920", "billboardOrStoryArt1280", "billboardOrStoryArt960"} {
		if address := s(catalogObject(node[key])["url"]); address != "" {
			d.Backdrop = address
			break
		}
	}
	d.Logo = s(catalogObject(node["logoStackCropped350"])["url"])
	d.Thumbnail = s(catalogObject(node["merchStill300"])["url"])
	return d
}

func ParseDetails(data []byte, id int) (TitleDetails, error) {
	root, _ := publicRoot(data)
	var exact map[string]any
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 48 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			kind := s(x["__typename"])
			if n(x["videoId"]) == id && (kind == "Show" || kind == "Movie" || kind == "Episode") {
				if exact == nil || len(x) > len(exact) {
					exact = x
				}
			}
			for _, child := range x {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range x {
				walk(child, depth+1)
			}
		}
	}
	walk(root, 0)
	result := TitleDetails{}
	if exact != nil {
		result.Details = detailsNode(exact)
	}
	if series, err := parse(data, id, true); err == nil {
		result.Series = &series
		if series.ID == id {
			result.Details = series.Metadata
		} else {
			for _, season := range series.Seasons {
				for _, episode := range season.Episodes {
					if episode.ID == id {
						d := episode.Metadata
						d.SeriesID = series.ID
						d.SeriesTitle = series.Title
						d.SeasonID = season.ID
						d.SeasonNumber = season.Number
						d.NumberingKnown = episode.Metadata.NumberingKnown
						d.EpisodeNumber = episode.Number
						d.Poster = series.Metadata.Poster
						d.Backdrop = series.Metadata.Backdrop
						d.Logo = series.Metadata.Logo
						d.Genres = append([]string(nil), series.Metadata.Genres...)
						result.Details = d
					}
				}
			}
		}
	}
	mergeLD(data, id, &result.Details)
	if result.Details.ID != id || result.Details.Kind == "" || result.Details.Title == "" {
		return TitleDetails{}, errors.New("la ficha no confirma si es película, serie o episodio")
	}
	result.Details.Source = fmt.Sprintf("https://www.netflix.com/title/%d", id)
	result.Details.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	if result.Series != nil {
		if result.Series.ID == id {
			result.Series.Metadata = result.Details
		} else {
			mergeLD(data, result.Series.ID, &result.Series.Metadata)
		}
	}
	if result.Series != nil {
		s := result.Series.Metadata
		s.Source = fmt.Sprintf("https://www.netflix.com/title/%d", result.Series.ID)
		s.FetchedAt = result.Details.FetchedAt
		result.Series.Metadata = s
		for i := range result.Series.Seasons {
			for k := range result.Series.Seasons[i].Episodes {
				e := &result.Series.Seasons[i].Episodes[k]
				e.Metadata.Poster = s.Poster
				e.Metadata.Backdrop = s.Backdrop
				e.Metadata.Logo = s.Logo
				e.Metadata.Source = fmt.Sprintf("https://www.netflix.com/title/%d", e.ID)
				e.Metadata.FetchedAt = s.FetchedAt
				if e.ID == id {
					result.Details.Poster = s.Poster
					result.Details.Backdrop = s.Backdrop
					result.Details.Logo = s.Logo
				}
			}
		}
	}
	return result, nil
}

var ldScripts = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)

func mergeLD(data []byte, id int, d *Details) {
	for _, match := range ldScripts.FindAllSubmatch(data, -1) {
		var value any
		if json.Unmarshal(match[1], &value) != nil {
			continue
		}
		var visit func(any, int)
		visit = func(v any, depth int) {
			if depth > 12 {
				return
			}
			switch x := v.(type) {
			case map[string]any:
				typ := s(x["@type"])
				if typ == "Movie" || typ == "TVSeries" || typ == "TVEpisode" {
					address := s(x["url"])
					if address == "" {
						address = s(x["@id"])
					}
					u, err := url.Parse(address)
					if address != "" && (err != nil || !matchesLDID(u, id)) {
						return
					}
					if address == "" && (d.ID != id || s(x["name"]) != d.Title) {
						return
					}
					if d.ID == 0 {
						d.ID = id
					}
					if d.Kind == "" {
						d.Kind = map[string]string{"Movie": "movie", "TVSeries": "series", "TVEpisode": "episode"}[typ]
					}
					if d.Title == "" {
						d.Title = s(x["name"])
					}
					if d.Description == "" {
						d.Description = s(x["description"])
					}
					if d.Poster == "" {
						d.Poster = s(x["image"])
						if d.Poster == "" {
							d.Poster = s(catalogObject(x["image"])["url"])
						}
					}
					if d.Year == 0 {
						for _, k := range []string{"datePublished", "dateCreated", "startDate"} {
							date := s(x[k])
							if len(date) >= 4 {
								var year int
								if _, err := fmt.Sscanf(date[:4], "%d", &year); err == nil && year >= 1880 && year <= 9999 {
									d.Year = year
									break
								}
							}
						}
					}
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
		visit(value, 0)
	}
}
func has(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func matchesLDID(u *url.URL, id int) bool {
	if u.User != nil || u.Port() != "" || u.Scheme != "https" || (u.Hostname() != "www.netflix.com" && u.Hostname() != "netflix.com") {
		return false
	}
	return u.Path == fmt.Sprintf("/title/%d", id) || u.Path == fmt.Sprintf("/es/title/%d", id) || u.Path == fmt.Sprintf("/watch/%d", id)
}

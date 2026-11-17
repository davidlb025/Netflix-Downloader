package main

import (
		netflixlib "local.netflix/netflix-downloader"
"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type VideoFile struct {
	Path        string           `json:"path"`
	SHA256      string           `json:"sha256"`
	Fingerprint VideoFingerprint `json:"fingerprint"`
}
type AudioFile struct {
	Path     string `json:"path"`
	Video    string `json:"video"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Embedded bool   `json:"embedded"`
}
type SubtitleFile struct {
	Path     string `json:"path"`
	Video    string `json:"video"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Kind     string `json:"kind"`
	Format   string `json:"format"`
}
type VideoLink struct {
	Video       string           `json:"video"`
	Name        string           `json:"nombre"`
	Fingerprint VideoFingerprint `json:"comparacion"`
}
type AudioLink struct {
	Audio    string `json:"pista_audio"`
	Video    string `json:"video"`
	Name     string `json:"nombre"`
	Language string `json:"idioma"`
	Embedded bool   `json:"integrada_en_video"`
}
type SubtitleLink struct {
	Subtitle string `json:"subtitulo"`
	Video    string `json:"video"`
	Name     string `json:"nombre"`
	Language string `json:"idioma"`
	Kind     string `json:"tipo"`
	Format   string `json:"formato"`
}
type MediaIndex struct {
	Application string         `json:"aplicacion"`
	Version     int            `json:"version"`
	EpisodeID   int            `json:"episodio_id"`
	Name        string         `json:"nombre"`
	Complete    bool           `json:"completo"`
	Videos      []VideoLink    `json:"videos"`
	Audio       []AudioLink    `json:"pistas_audio"`
	Subtitles   []SubtitleLink `json:"subtitulos"`
}

func regularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
func samePath(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
func withinRoot(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func jobLanguages(j *Job) []string {
	if len(j.AudioLanguages) > 0 {
		return j.AudioLanguages
	}
	return []string{j.Audio}
}
func sameLanguages(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, "\x00") == strings.Join(y, "\x00")
}
func overlappingAudio(a, b []string) bool {
	for _, v := range a {
		if containsString(b, v) {
			return true
		}
	}
	return false
}
func jobAssetsExist(j *Job) bool {
	if !regularFile(j.Path) {
		return false
	}
	for _, v := range j.VideoFiles {
		if !regularFile(v.Path) {
			return false
		}
	}
	for _, a := range j.AudioFiles {
		if !regularFile(a.Path) {
			return false
		}
	}
	return !j.MultiAudio || len(j.AudioFiles) == len(jobLanguages(j)) && regularFile(j.ManifestPath)
}

func indexRelative(root, path string) (string, error) {
	if !withinRoot(root, path) {
		return "", errors.New("una pista está fuera de la carpeta del episodio")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return "", errors.New("ruta de pista no válida")
	}
	return filepath.ToSlash(rel), nil
}
func checkMediaIndex(path string, id int) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if !regularFile(path) {
		return errors.New("el catálogo de pistas no es un archivo regular")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8<<20 {
		return errors.New("no se pudo leer el catálogo de pistas")
	}
	var old MediaIndex
	if json.Unmarshal(data, &old) != nil || old.Application != "Netflix Downloader" || old.Version != 1 || old.EpisodeID != id {
		return errors.New("ya existe un JSON ajeno a esta descarga; no se ha sobrescrito")
	}
	return nil
}

// The caller holds b.mu. All paths in this portable catalog are relative to it.
func writeMediaIndex(j *Job, complete bool) error {
	if j.ManifestPath == "" {
		return nil
	}
	if err := checkMediaIndex(j.ManifestPath, j.ID); err != nil {
		return err
	}
	root := filepath.Dir(j.ManifestPath)
	doc := MediaIndex{Application: "Netflix Downloader", Version: 1, EpisodeID: j.ID, Name: j.Title, Complete: complete, Videos: []VideoLink{}, Audio: []AudioLink{}, Subtitles: []SubtitleLink{}}
	for _, v := range j.VideoFiles {
		rel, err := indexRelative(root, v.Path)
		if err != nil {
			return err
		}
		doc.Videos = append(doc.Videos, VideoLink{Video: rel, Name: j.Title, Fingerprint: v.Fingerprint})
	}
	for _, a := range j.AudioFiles {
		path, err := indexRelative(root, a.Path)
		if err != nil {
			return err
		}
		video, err := indexRelative(root, a.Video)
		if err != nil {
			return err
		}
		doc.Audio = append(doc.Audio, AudioLink{Audio: path, Video: video, Name: a.Name, Language: a.Language, Embedded: a.Embedded})
	}
	for _, s := range j.SubtitleTracks {
		path, err := indexRelative(root, s.Path)
		if err != nil {
			return err
		}
		video, err := indexRelative(root, s.Video)
		if err != nil {
			return err
		}
		doc.Subtitles = append(doc.Subtitles, SubtitleLink{Subtitle: path, Video: video, Name: s.Name, Language: s.Language, Kind: s.Kind, Format: s.Format})
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	return atomicJSON(j.ManifestPath, doc)
}

func addSubtitles(j *Job, result netflixlib.CaptionResult, video string) {
	for _, path := range result.Paths {
		if !containsString(j.Subtitles, path) {
			j.Subtitles = append(j.Subtitles, path)
		}
	}
	for _, track := range result.Tracks {
		name := track.Name
		if name == "" {
			name = netflixlib.Name(track.Language)
		}
		switch track.Kind {
		case "forced":
			name += " · Forzados"
		case "sdh":
			name += " · SDH"
		default:
			name += " · Completos"
		}
		found := false
		for _, old := range j.SubtitleTracks {
			if samePath(old.Path, track.Path) && samePath(old.Video, video) {
				found = true
				break
			}
		}
		if !found {
			j.SubtitleTracks = append(j.SubtitleTracks, SubtitleFile{Path: track.Path, Video: video, Name: name, Language: track.Language, Kind: track.Kind, Format: track.Format})
		}
	}
}

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type outputLayout struct{ Directory, Video, Audio, Manifest, Subtitles, SubtitleAnchor string }

var invalidComponent = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func fileComponent(value, fallback string) string {
	value = strings.Trim(invalidComponent.ReplaceAllString(value, "_"), " .")
	if value == "" || value == "." || value == ".." {
		value = fallback
	}
	if len([]rune(value)) > 100 {
		value = string([]rune(value)[:100])
		value = strings.TrimRight(value, " .")
	}
	base := strings.ToUpper(strings.Split(value, ".")[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(base) {
		value = "_" + value
	}
	return value
}
func jobLayout(root string, j *Job) (outputLayout, error) {
	if !filepath.IsAbs(root) {
		return outputLayout{}, errors.New("la carpeta de salida debe ser absoluta")
	}
	if j.LibraryVersion == 2 {
		if j.LibraryRoot != "" {
			root = j.LibraryRoot
		}
		return libraryLayout(root, j)
	}
	series := fileComponent(j.Series, "Sin serie")
	title := j.Title
	if j.Season > 0 && j.Episode > 0 {
		title = episodeCode(j.Season, j.Episode) + " - " + title
	}
	title = fileComponent(title, fmt.Sprintf("Episodio %d", j.ID))
	language := fileComponent(j.Audio, "und")
	name := title + "_" + language
	directory := filepath.Join(root, series, name)
	if j.MultiAudio {
		directory = filepath.Join(root, series, title+"_multiaudio"+j.BundleSuffix)
	}
	rel, err := filepath.Rel(root, directory)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return outputLayout{}, errors.New("la ruta de salida no está dentro de la carpeta elegida")
	}
	subs := filepath.Join(directory, "Subtitulos")
	video := filepath.Join(directory, name+".mp4")
	anchor := filepath.Join(subs, name+".mp4")
	if j.MultiAudio {
		video = filepath.Join(directory, "Videos", name+".mp4")
		anchor = filepath.Join(subs, title+".mp4")
	}
	return outputLayout{Directory: directory, Video: video, Audio: filepath.Join(directory, "Audios", name+".m4a"), Manifest: filepath.Join(directory, "pistas.json"), Subtitles: subs, SubtitleAnchor: anchor}, nil
}
func publishVideo(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return errors.New("el episodio con ese audio ya existe; no se ha sobrescrito")
	}
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(target)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
func (b *backend) historyPath() string { return filepath.Join(b.home, "history.json") }

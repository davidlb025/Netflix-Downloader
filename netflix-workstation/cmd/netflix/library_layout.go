package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var editionKey = regexp.MustCompile(`^[a-f0-9]{16}$`)

func episodeNumberText(number float64) string {
	text := strconv.FormatFloat(number, 'f', -1, 64)
	integer, fraction, hasFraction := strings.Cut(text, ".")
	if len(integer) < 2 {
		integer = "0" + integer
	}
	if hasFraction {
		return integer + "." + fraction
	}
	return integer
}

func episodeCode(season int, episode float64) string {
	return fmt.Sprintf("S%02dE%s", season, episodeNumberText(episode))
}

func identified(j *Job) bool {
	d := j.Metadata
	return d.ID == j.ID && (d.Kind == "movie" || d.Kind == "episode" && d.SeriesID >= 100000 && d.SeasonID >= 100000 && d.NumberingKnown && d.EpisodeNumber > 0)
}
func entityDirectory(root string, j *Job) string {
	d := j.Metadata
	if identified(j) {
		if d.Kind == "movie" {
			return filepath.Join(root, "movies", fmt.Sprintf("Netflix-%d", j.ID))
		}
		if j.SeriesFoldersByName {
			series := strings.TrimSpace(j.Series)
			if series == "" {
				series = strings.TrimSpace(j.SeriesMetadata.Title)
			}
			if series == "" {
				series = strings.TrimSpace(d.SeriesTitle)
			}
			series = fileComponent(series, fmt.Sprintf("Serie %d", d.SeriesID))
			season := d.SeasonNumber
			if season == 0 {
				season = j.Season
			}
			episode := d.EpisodeNumber
			if episode == 0 {
				episode = j.Episode
			}
			seasonName := fmt.Sprintf("S%02d", season)
			episodeName := episodeCode(season, episode)
			return filepath.Join(root, "series", series, seasonName, episodeName)
		}
		return filepath.Join(root, "series", fmt.Sprintf("Netflix-%d", d.SeriesID), fmt.Sprintf("Season-Netflix-%d", d.SeasonID), fmt.Sprintf("Netflix-%d", j.ID))
	}
	return filepath.Join(root, ".pending", fmt.Sprintf("Netflix-%d", j.ID))
}
func libraryLayout(root string, j *Job) (outputLayout, error) {
	if !editionKey.MatchString(j.Key) {
		return outputLayout{}, errors.New("identificador de edición inválido")
	}
	name := fileComponent(j.Title, fmt.Sprintf("Netflix %d", j.ID))
	if j.Metadata.Kind == "episode" && j.Metadata.NumberingKnown {
		name = episodeCode(j.Metadata.SeasonNumber, j.Metadata.EpisodeNumber) + " - " + name
	}
	stem := name + "_" + fileComponent(j.Audio, "und")
	edition := j.Key
	if !j.LibraryPublished {
		edition = ".incoming-" + edition
	}
	dir := filepath.Join(entityDirectory(root, j), "editions", edition)
	if err := checkOutputPath(root, dir); err != nil {
		return outputLayout{}, err
	}
	subs := filepath.Join(dir, "Subtitulos")
	return outputLayout{Directory: dir, Video: filepath.Join(dir, "Videos", stem+".mp4"), Audio: filepath.Join(dir, "Audios", stem+".m4a"), Manifest: filepath.Join(dir, "pistas.json"), Subtitles: subs, SubtitleAnchor: filepath.Join(subs, name+".mp4")}, nil
}

// Reject junctions as well as symlinks in the selected output tree.
func checkOutputPath(root, path string) error {
	if !withinRoot(root, path) {
		return errors.New("ruta fuera de la biblioteca")
	}
	current := filepath.Clean(root)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("la biblioteca no admite enlaces")
			}
			if win, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && win.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return errors.New("la biblioteca no admite uniones ni puntos de análisis")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if samePath(current, path) {
			return nil
		}
		rel, err := filepath.Rel(current, path)
		if err != nil {
			return err
		}
		next := strings.Split(rel, string(filepath.Separator))[0]
		if next == "" || next == "." || next == ".." {
			return errors.New("ruta de biblioteca inválida")
		}
		current = filepath.Join(current, next)
	}
}

func rebaseJob(j *Job, old, new string) {
	move := func(path string) string {
		if path == "" || !withinRoot(old, path) {
			return path
		}
		rel, _ := filepath.Rel(old, path)
		return filepath.Join(new, rel)
	}
	j.Path = move(j.Path)
	j.ManifestPath = move(j.ManifestPath)
	j.SubtitleDirectory = move(j.SubtitleDirectory)
	j.SubtitleAnchor = move(j.SubtitleAnchor)
	for i := range j.VideoFiles {
		j.VideoFiles[i].Path = move(j.VideoFiles[i].Path)
	}
	for i := range j.AudioFiles {
		j.AudioFiles[i].Path = move(j.AudioFiles[i].Path)
		j.AudioFiles[i].Video = move(j.AudioFiles[i].Video)
	}
	for i := range j.SubtitleTracks {
		j.SubtitleTracks[i].Path = move(j.SubtitleTracks[i].Path)
		j.SubtitleTracks[i].Video = move(j.SubtitleTracks[i].Video)
	}
	for i := range j.Subtitles {
		j.Subtitles[i] = move(j.Subtitles[i])
	}
	for i := range j.ExportFiles {
		j.ExportFiles[i] = move(j.ExportFiles[i])
	}
}

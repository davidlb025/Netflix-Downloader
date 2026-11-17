package main

import (
		netflixlib "local.netflix/netflix-downloader"
"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func runMediaCopy(ctx context.Context, ffmpeg string, args ...string) error {
	if len(args) == 0 {
		return errors.New("falta la ruta de la pista")
	}
	if _, err := os.Lstat(args[len(args)-1]); !os.IsNotExist(err) {
		return errors.New("el archivo temporal de la pista ya existe o no se puede comprobar")
	}
	command := exec.CommandContext(ctx, ffmpeg, append([]string{"-hide_banner", "-v", "error", "-xerror", "-nostdin", "-n"}, args...)...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("no se pudo separar la pista: %w: %.600s", err, diagnostic.String())
	}
	return nil
}

func inspectVideoMetadata(ctx context.Context, ffmpeg, path string, height int) (VideoFingerprint, error) {
	probe, err := probeMedia(ctx, ffmpeg, path)
	if err != nil {
		return VideoFingerprint{}, err
	}
	fingerprint := VideoFingerprint{Algorithm: "metadata-only"}
	hasVideo, hasAudio := false, false
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			if !hasVideo {
				hasVideo = true
				fingerprint.Width = stream.Width
				fingerprint.Height = stream.Height
				if stream.Height != height {
					return VideoFingerprint{}, fmt.Errorf("el motor exportó %dp en lugar de los %dp elegidos", stream.Height, height)
				}
			}
		case "audio":
			hasAudio = true
		}
	}
	if !hasVideo || !hasAudio || probe.duration() <= 0 {
		return VideoFingerprint{}, errors.New("el MP4 debe contener vídeo, audio y una duración válida")
	}
	return fingerprint, nil
}

func (b *backend) finalizeVariant(ctx context.Context, j *Job, settings Settings, staged string, height int, audio string, tracks []netflixlib.CaptionTrack, status func(string, string, int)) (resultErr error) {
	variant := *j
	variant.Audio = audio
	layout, err := jobLayout(settings.Output, &variant)
	if err != nil {
		return err
	}
	if err = checkMediaIndex(layout.Manifest, j.ID); err != nil {
		return err
	}
	compareFrames := j.MultiAudio && !j.IndependentAudioMP4
	var fingerprint VideoFingerprint
	if compareFrames {
		status("verifying", "Comparando los fotogramas para compartir el vídeo…", 92)
		fingerprint, err = fingerprintVideo(ctx, settings.FFmpeg, staged, true)
		if err != nil {
			return err
		}
		if fingerprint.Height != height {
			return fmt.Errorf("el motor exportó %dp en lugar de los %dp elegidos; no se ha publicado con una calidad falsa", fingerprint.Height, height)
		}
	} else {
		status("verifying", "Validando las pistas y la calidad del MP4…", 92)
		fingerprint, err = inspectVideoMetadata(ctx, settings.FFmpeg, staged, height)
		if err != nil {
			return err
		}
	}
	var shared *VideoFile
	if compareFrames {
		for _, candidate := range j.VideoFiles {
			if candidate.Fingerprint != fingerprint || candidate.SHA256 == "" || !regularFile(candidate.Path) {
				continue
			}
			current, err := fileSHA256(ctx, candidate.Path)
			if err != nil {
				return err
			}
			if current == candidate.SHA256 {
				copy := candidate
				shared = &copy
				break
			}
		}
	}
	videoTarget := layout.Video
	audioTarget := layout.Video
	var newlyPublished []string
	var saved netflixlib.CaptionResult
	defer func() {
		if resultErr != nil {
			for _, path := range newlyPublished {
				os.Remove(path)
			}
			for _, track := range saved.Tracks {
				if track.Created {
					os.Remove(track.Path)
				}
			}
		}
	}()
	if compareFrames {
		audioTarget = layout.Audio
		copyDir, err := os.MkdirTemp(b.home, "separate-")
		if err != nil {
			return err
		}
		if !withinRoot(b.home, copyDir) {
			return errors.New("ruta temporal no válida")
		}
		defer os.RemoveAll(copyDir)
		audioStaged := filepath.Join(copyDir, "audio-export.m4a")
		if err = runMediaCopy(ctx, settings.FFmpeg, "-i", staged, "-map", "0:a:0", "-vn", "-c:a", "copy", "-metadata:s:a:0", "language="+netflixlib.AudioLanguageTag(audio), "-metadata:s:a:0", "title="+netflixlib.Name(audio), "-movflags", "+faststart", audioStaged); err != nil {
			return err
		}
		if shared != nil {
			videoTarget = shared.Path
			status("verifying", "Vídeo idéntico: guardando solo la pista de audio…", 95)
		} else {
			videoStaged := filepath.Join(copyDir, "video-export.mp4")
			if err = runMediaCopy(ctx, settings.FFmpeg, "-i", staged, "-map", "0:v:0", "-an", "-c:v", "copy", "-movflags", "+faststart", videoStaged); err != nil {
				return err
			}
			if err = publishVideo(videoStaged, videoTarget); err != nil {
				return err
			}
			newlyPublished = append(newlyPublished, videoTarget)
		}
		if err = publishVideo(audioStaged, audioTarget); err != nil {
			return err
		}
		newlyPublished = append(newlyPublished, audioTarget)
	} else if j.MultiAudio && j.IndependentAudioMP4 {
		copyDir, err := os.MkdirTemp(b.home, "independent-audio-")
		if err != nil {
			return err
		}
		if !withinRoot(b.home, copyDir) {
			os.RemoveAll(copyDir)
			return errors.New("ruta temporal no válida")
		}
		defer os.RemoveAll(copyDir)
		videoStaged := filepath.Join(copyDir, "complete-audio.mp4")
		if err = runMediaCopy(ctx, settings.FFmpeg, "-i", staged, "-map", "0:v:0", "-map", "0:a:0", "-c:v", "copy", "-c:a", "copy", "-metadata:s:a:0", "language="+netflixlib.AudioLanguageTag(audio), "-metadata:s:a:0", "title="+netflixlib.Name(audio), "-movflags", "+faststart", videoStaged); err != nil {
			return err
		}
		if _, err = inspectVideoMetadata(ctx, settings.FFmpeg, videoStaged, height); err != nil {
			return err
		}
		if err = publishVideo(videoStaged, videoTarget); err != nil {
			return err
		}
		newlyPublished = append(newlyPublished, videoTarget)
	} else {
		if err = publishVideo(staged, videoTarget); err != nil {
			return err
		}
		newlyPublished = append(newlyPublished, videoTarget)
	}
	var video VideoFile
	if shared != nil {
		video = *shared
	} else {
		sum := ""
		if compareFrames {
			sum, err = fileSHA256(ctx, videoTarget)
			if err != nil {
				return err
			}
		}
		video = VideoFile{Path: videoTarget, SHA256: sum, Fingerprint: fingerprint}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.MkdirAll(layout.Subtitles, 0755); err != nil {
		return err
	}
	if len(tracks) > 0 {
		status("subtitles", "Guardando subtítulos y sus nombres…", 98)
	}
	saved = netflixlib.SaveAll(ctx, tracks, layout.SubtitleAnchor, nil)
	if err = ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := *j
	// Fresh slices let a failed JSON write restore the previous valid bundle.
	j.VideoFiles = append([]VideoFile(nil), j.VideoFiles...)
	j.AudioFiles = append([]AudioFile(nil), j.AudioFiles...)
	j.Subtitles = append([]string(nil), j.Subtitles...)
	j.SubtitleTracks = append([]SubtitleFile(nil), j.SubtitleTracks...)
	if shared == nil {
		j.VideoFiles = append(j.VideoFiles, video)
	}
	j.AudioFiles = append(j.AudioFiles, AudioFile{Path: audioTarget, Video: videoTarget, Name: netflixlib.Name(audio), Language: audio, Embedded: !j.MultiAudio || j.IndependentAudioMP4})
	if j.Path == "" {
		j.Path = videoTarget
		j.Height = height
	} else if height > j.Height {
		j.Height = height
	}
	j.ManifestPath = layout.Manifest
	j.SubtitleDirectory = layout.Subtitles
	j.SubtitleAnchor = layout.SubtitleAnchor
	addSubtitles(j, saved, videoTarget)
	j.SubtitleFailed += saved.Failed
	j.SubtitleTotal = len(j.Subtitles) + j.SubtitleFailed
	j.Bytes = 0
	seen := map[string]bool{}
	for _, path := range allJobPaths(j, false) {
		if seen[path] {
			continue
		}
		seen[path] = true
		if info, err := os.Stat(path); err == nil {
			j.Bytes += info.Size()
		}
	}
	if err = writeMediaIndex(j, false); err != nil {
		*j = previous
		return err
	}
	b.persist()
	return nil
}

func allJobPaths(j *Job, includeIndex bool) []string {
	paths := append([]string{j.Path}, j.Subtitles...)
	for _, v := range j.VideoFiles {
		paths = append(paths, v.Path)
	}
	for _, a := range j.AudioFiles {
		paths = append(paths, a.Path)
	}
	if includeIndex {
		paths = append(paths, j.ExportFiles...)
	}
	if includeIndex && j.ManifestPath != "" {
		paths = append(paths, j.ManifestPath)
	}
	return paths
}

func (b *backend) download(ctx context.Context, j *Job, settings Settings) error {
	languages := append([]string(nil), jobLanguages(j)...)
	maxHeight := j.Height
	for i, audio := range languages {
		if err := ctx.Err(); err != nil {
			return err
		}
		status := func(state, detail string, progress int) {
			if len(languages) > 1 {
				detail = fmt.Sprintf("%s · Idioma %d/%d · %s", netflixlib.Name(audio), i+1, len(languages), detail)
			}
			b.update(j, state, detail, (i*100+progress)/len(languages))
		}
		if err := b.downloadVariant(ctx, j, settings, audio, maxHeight, status); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	err := writeMediaIndex(j, true)
	b.mu.Unlock()
	if err != nil {
		return errors.New("las pistas se guardaron, pero no se pudo completar pistas.json: " + err.Error())
	}
	if j.LibraryVersion == 2 {
		b.update(j, "verifying", "Preparando fichas, ilustraciones y catálogo para MediaCenter…", 99)
		b.mu.Lock()
		data, _ := json.Marshal(j)
		b.mu.Unlock()
		var exported Job
		if err := json.Unmarshal(data, &exported); err != nil {
			return err
		}
		exportErr := b.exportLibrary(ctx, &exported, settings)
		if exportErr != nil {
			exported.ExportWarnings = append(exported.ExportWarnings, "Exportación pendiente: "+exportErr.Error())
		}
		b.mu.Lock()
		*j = exported
		b.persist()
		b.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	detail := "MP4 comprobado · H.264 / AAC · pistas.json"
	if j.MultiAudio && j.IndependentAudioMP4 {
		detail = fmt.Sprintf("%d MP4 completos por idioma · pistas.json", len(j.VideoFiles))
	} else if j.MultiAudio {
		detail = fmt.Sprintf("%d vídeo(s) · %d pistas de audio · pistas.json", len(j.VideoFiles), len(j.AudioFiles))
	}
	if len(j.Subtitles) > 0 {
		detail += fmt.Sprintf(" · %d subtítulos", len(j.Subtitles))
	}
	if j.SubtitleFailed > 0 {
		detail += " · Algunos subtítulos no se pudieron guardar"
	}
	if j.CatalogPath != "" {
		detail += " · Ficha MediaCenter"
	}
	if len(j.ExportWarnings) > 0 {
		detail += " · " + strings.Join(j.ExportWarnings, "; ")
	}
	b.update(j, "complete", detail, 100)
	return nil
}

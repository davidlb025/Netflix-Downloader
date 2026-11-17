package main

import (
		netflixlib "local.netflix/netflix-downloader"
"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type objectJSON = map[string]any

func listJSON(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func mapJSON(v any) objectJSON {
	m, _ := v.(map[string]any)
	if m == nil {
		return objectJSON{}
	}
	return m
}
func textJSON(v any) string                { s, _ := v.(string); return s }
func contentID(kind string, id int) string { return fmt.Sprintf("nf_%s_%d", kind, id) }

func ensureCanonicalLocation(root, kind, id, target string) error {
	count := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("biblioteca demasiado grande para comprobar su identidad; requiere inventario indexado")
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "editions" || entry.Name() == "artwork") {
				return filepath.SkipDir
			}
			if err := checkOutputPath(root, path); err != nil {
				return err
			}
			return nil
		}
		if entry.Name() != kind+".json" || samePath(path, target) {
			return nil
		}
		if !regularFile(path) {
			return errors.New("una ficha de biblioteca no es un archivo regular")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 4<<20 {
			return errors.New("no se pudo comprobar una ficha de biblioteca")
		}
		var doc objectJSON
		if json.Unmarshal(data, &doc) == nil && textJSON(doc["id"]) == id {
			return errors.New("la identidad ya existe en otra ubicación; requiere migración explícita")
		}
		return nil
	})
}
func coreMetadata(d netflixlib.Details, kind string, id int, title string) objectJSON {
	if title == "" {
		title = fmt.Sprintf("Netflix %d", id)
	}
	m := objectJSON{"schemaVersion": 2, "id": contentID(kind, id), "type": kind, "title": title, "description": d.Description, "originalTitle": d.OriginalTitle, "genres": append([]string{}, d.Genres...), "origins": []any{objectJSON{"id": "netflix", "name": "Netflix"}}, "defaultOriginId": "netflix", "artwork": objectJSON{}}
	if d.Year >= 1880 && d.Year <= 9999 {
		m["year"] = d.Year
	}
	return m
}
func readCatalog(path, kind, id string) (objectJSON, error) {
	if !regularFile(path) {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.New("la ficha existente no es un archivo regular")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("no se pudo leer la ficha existente")
	}
	var m objectJSON
	if json.Unmarshal(data, &m) != nil || m["schemaVersion"] != float64(2) || textJSON(m["id"]) != id || textJSON(m["type"]) != kind {
		return nil, errors.New("conflicto de identidad o esquema en la ficha existente; se conserva")
	}
	return m, nil
}

var bcp47 = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)

func trackLanguage(v string) string {
	if !bcp47.MatchString(v) {
		return "und"
	}
	return v
}
func trackID(prefix, path, video string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(path) + "\x00" + filepath.ToSlash(video)))
	return prefix + hex.EncodeToString(sum[:8])
}

// Keep original TTML as source evidence and add a text-only WebVTT derivative.
// Multiple video associations for the same file are preserved.
func normalizeSubtitles(j *Job) {
	tracks := append([]SubtitleFile(nil), j.SubtitleTracks...)
	for _, track := range tracks {
		if strings.ToLower(filepath.Ext(track.Path)) != ".ttml" && strings.ToLower(filepath.Ext(track.Path)) != ".dfxp" {
			continue
		}
		data, err := os.ReadFile(track.Path)
		if err != nil {
			j.ExportWarnings = append(j.ExportWarnings, "No se pudo leer un TTML para convertirlo")
			continue
		}
		vtt, err := netflixlib.TTMLToVTT(data)
		if err != nil {
			j.ExportWarnings = append(j.ExportWarnings, "TTML conservado sin exportar: "+err.Error())
			continue
		}
		path := strings.TrimSuffix(track.Path, filepath.Ext(track.Path)) + ".vtt"
		if old, err := os.ReadFile(path); err == nil {
			if string(old) != string(vtt) {
				j.ExportWarnings = append(j.ExportWarnings, "Se conserva un WebVTT existente con contenido diferente")
				continue
			}
		} else {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				j.ExportWarnings = append(j.ExportWarnings, "No se pudo guardar WebVTT")
				continue
			}
			_, err = f.Write(vtt)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				os.Remove(path)
				continue
			}
		}
		track.Path = path
		track.Format = "vtt"
		found := false
		for _, old := range j.SubtitleTracks {
			if samePath(old.Path, path) && samePath(old.Video, track.Video) {
				found = true
				break
			}
		}
		if !found {
			j.SubtitleTracks = append(j.SubtitleTracks, track)
		}
		if !containsString(j.Subtitles, path) {
			j.Subtitles = append(j.Subtitles, path)
		}
	}
}

func buildPlayable(ctx context.Context, j *Job, root, ffmpeg string) (objectJSON, error) {
	d := j.Metadata
	m := coreMetadata(d, d.Kind, j.ID, j.Title)
	versions, audio, subtitles := []any{}, []any{}, []any{}
	prefix := "d" + j.Key + "_"
	videoIDs := map[string]string{}
	probes := map[string]probeInfo{}
	defaultAudio := ""
	for i, v := range j.VideoFiles {
		probe, err := probeMedia(ctx, ffmpeg, v.Path)
		if err != nil {
			return nil, err
		}
		probes[v.Path] = probe
		if probe.duration() <= 0 || probe.duration() > 604800 {
			return nil, errors.New("duración multimedia no válida")
		}
		id := fmt.Sprintf("%sv%d", prefix, i)
		videoIDs[v.Path] = id
		rel, err := indexRelative(root, v.Path)
		if err != nil {
			return nil, err
		}
		quality := objectJSON{}
		embedded := []any{}
		for _, s := range probe.Streams {
			if s.CodecType == "video" {
				quality = objectJSON{"label": fmt.Sprintf("%dp", s.Height), "resolution": fmt.Sprintf("%dx%d", s.Width, s.Height), "videoCodec": s.CodecName, "hdr": false}
			}
			if s.CodecType == "audio" {
				language, name := j.Audio, ""
				for _, a := range j.AudioFiles {
					if a.Embedded && samePath(a.Path, v.Path) {
						language = a.Language
						name = a.Name
						break
					}
				}
				track := fmt.Sprintf("%sv%da%d", prefix, i, s.Index)
				embedded = append(embedded, objectJSON{"id": track, "language": trackLanguage(language), "label": name, "type": "embedded", "streamIndex": s.Index, "codec": s.CodecName, "format": s.CodecName, "channels": s.Channels})
				if i == 0 && defaultAudio == "" {
					defaultAudio = track
				}
			}
		}
		if len(quality) == 0 {
			return nil, errors.New("la versión no contiene vídeo")
		}
		versions = append(versions, objectJSON{"id": id, "originId": "netflix", "file": rel, "container": strings.Split(probe.Format.Name, ",")[0], "quality": quality, "tracks": objectJSON{"audio": embedded, "subtitles": []any{}}, "sizeBytes": mediaSize(v.Path), "durationSeconds": probe.duration()})
	}
	if len(versions) == 0 {
		return nil, errors.New("no hay versiones verificadas que exportar")
	}
	for _, a := range j.AudioFiles {
		if a.Embedded {
			continue
		}
		version := videoIDs[a.Video]
		if version == "" {
			return nil, errors.New("audio sin versión compatible")
		}
		p, err := probeMedia(ctx, ffmpeg, a.Path)
		if err != nil {
			return nil, err
		}
		v := probes[a.Video]
		startA, _ := strconv.ParseFloat(p.Format.StartTime, 64)
		startV, _ := strconv.ParseFloat(v.Format.StartTime, 64)
		if p.duration() <= 0 || math.Abs(startA-startV) > .1 || math.Abs(p.duration()-v.duration()) > .3 {
			return nil, errors.New("el audio externo necesita revisar su sincronización antes de publicarlo")
		}
		codec := ""
		for _, s := range p.Streams {
			if s.CodecType == "audio" {
				codec = s.CodecName
				break
			}
		}
		if codec == "" {
			return nil, errors.New("la pista externa no contiene audio")
		}
		rel, err := indexRelative(root, a.Path)
		if err != nil {
			return nil, err
		}
		id := trackID(prefix+"a", rel, version)
		audio = append(audio, objectJSON{"id": id, "language": trackLanguage(a.Language), "label": a.Name, "file": rel, "format": strings.TrimPrefix(filepath.Ext(a.Path), "."), "codec": codec, "type": "external", "compatibleVersions": []any{version}, "syncOffsetMs": 0})
		if version == videoIDs[j.VideoFiles[0].Path] && defaultAudio == "" {
			defaultAudio = id
		}
	}
	for _, s := range j.SubtitleTracks {
		ext := strings.ToLower(filepath.Ext(s.Path))
		if ext != ".vtt" && ext != ".srt" && ext != ".ass" && ext != ".ssa" {
			continue
		}
		version := videoIDs[s.Video]
		if version == "" {
			return nil, errors.New("subtítulo sin versión compatible")
		}
		rel, err := indexRelative(root, s.Path)
		if err != nil {
			return nil, err
		}
		format := strings.TrimPrefix(ext, ".")
		if format == "vtt" {
			format = "webvtt"
		}
		found := false
		for _, old := range subtitles {
			o := mapJSON(old)
			if textJSON(o["file"]) == rel {
				o["compatibleVersions"] = append(listJSON(o["compatibleVersions"]), version)
				found = true
				break
			}
		}
		if !found {
			subtitles = append(subtitles, objectJSON{"id": trackID(prefix+"s", rel, ""), "language": trackLanguage(s.Language), "label": s.Name, "file": rel, "format": format, "codec": format, "type": "external", "forced": s.Kind == "forced", "sdh": s.Kind == "sdh", "compatibleVersions": []any{version}, "syncOffsetMs": 0})
		}
	}
	m["versions"] = versions
	m["externalTracks"] = objectJSON{"audio": audio, "subtitles": subtitles}
	m["durationSeconds"] = probes[j.VideoFiles[0].Path].duration()
	defaults := objectJSON{"versionId": videoIDs[j.VideoFiles[0].Path]}
	if defaultAudio != "" {
		defaults["audioTrackId"] = defaultAudio
	}
	m["defaults"] = defaults
	if d.Kind == "episode" {
		m["seriesId"] = contentID("series", d.SeriesID)
		m["seasonId"] = contentID("season", d.SeasonID)
		m["seasonNumber"] = d.SeasonNumber
		m["episodeNumber"] = d.EpisodeNumber
	}
	return m, nil
}

func mergePlayable(old, fresh objectJSON, key string) (objectJSON, error) {
	if old == nil {
		return fresh, nil
	}
	// Editorial fields and existing defaults belong to the library owner.
	for _, k := range []string{"seriesId", "seasonId", "seasonNumber", "episodeNumber"} {
		if fresh[k] != nil && fmt.Sprint(old[k]) != fmt.Sprint(fresh[k]) {
			return nil, errors.New("la ficha pertenece a otra temporada o numeración")
		}
	}
	prefix := "d" + key + "_"
	filter := func(items []any) []any {
		out := []any{}
		for _, item := range items {
			if !strings.HasPrefix(textJSON(mapJSON(item)["id"]), prefix) {
				out = append(out, item)
			}
		}
		return out
	}
	versions := append(filter(listJSON(old["versions"])), listJSON(fresh["versions"])...)
	if len(versions) > 100 {
		return nil, errors.New("la ficha supera el máximo de versiones de MediaCenter")
	}
	old["versions"] = versions
	external := mapJSON(old["externalTracks"])
	newExternal := mapJSON(fresh["externalTracks"])
	for _, k := range []string{"audio", "subtitles"} {
		items := append(filter(listJSON(external[k])), listJSON(newExternal[k])...)
		if len(items) > 100 {
			return nil, errors.New("la ficha supera el máximo de pistas de MediaCenter")
		}
		external[k] = items
	}
	old["externalTracks"] = external
	origins := listJSON(old["origins"])
	found := false
	for _, o := range origins {
		if textJSON(mapJSON(o)["id"]) == "netflix" {
			found = true
		}
	}
	if !found {
		origins = append(origins, objectJSON{"id": "netflix", "name": "Netflix"})
	}
	old["origins"] = origins
	defaults := mapJSON(old["defaults"])
	valid := false
	for _, v := range versions {
		if textJSON(mapJSON(v)["id"]) == textJSON(defaults["versionId"]) {
			valid = true
		}
	}
	if !valid {
		old["defaults"] = fresh["defaults"]
	}
	return old, nil
}

func writeReference(path string, fresh objectJSON, field string, ref objectJSON) error {
	root := filepath.Dir(path)
	if err := checkOutputPath(root, path); err != nil {
		return err
	}
	old, err := readCatalog(path, textJSON(fresh["type"]), textJSON(fresh["id"]))
	if err != nil {
		return err
	}
	if old == nil {
		old = fresh
	}
	refs := listJSON(old[field])
	found := false
	for _, entry := range refs {
		r := mapJSON(entry)
		if textJSON(r["id"]) == textJSON(ref["id"]) {
			if fmt.Sprint(r["number"]) != fmt.Sprint(ref["number"]) || r["metadata"] != ref["metadata"] {
				return errors.New("la referencia existente tiene otra numeración o ruta")
			}
			found = true
		} else if fmt.Sprint(r["number"]) == fmt.Sprint(ref["number"]) {
			return errors.New("hay dos entidades distintas con el mismo número en la biblioteca")
		}
	}
	if !found {
		refs = append(refs, ref)
	}
	old[field] = refs
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	return atomicJSON(path, old)
}

func attachArtwork(ctx context.Context, j *Job, root string, m objectJSON) {
	art := objectJSON{}
	if j.Metadata.Poster == "" {
		j.ExportWarnings = append(j.ExportWarnings, "Netflix no publicó una portada identificada para esta ficha")
	}
	for _, a := range []struct{ key, url string }{{"poster", j.Metadata.Poster}, {"backdrop", j.Metadata.Backdrop}, {"logo", j.Metadata.Logo}} {
		if a.url == "" {
			continue
		}
		rel, err := saveArtwork(ctx, root, a.url)
		if err != nil {
			j.ExportWarnings = append(j.ExportWarnings, "Ilustración "+a.key+": "+err.Error())
			continue
		}
		art[a.key] = rel
	}
	m["artwork"] = art
	if j.Metadata.Kind == "episode" && j.Metadata.Thumbnail != "" {
		rel, err := saveArtwork(ctx, root, j.Metadata.Thumbnail)
		if err == nil {
			m["thumbnail"] = rel
		} else {
			j.ExportWarnings = append(j.ExportWarnings, "Miniatura no disponible")
		}
	}
}
func copyArtwork(source, target string, art objectJSON) error {
	for _, item := range art {
		rel := textJSON(item)
		if rel == "" {
			continue
		}
		src := filepath.Join(source, filepath.FromSlash(rel))
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if !withinRoot(source, src) || !withinRoot(target, dst) {
			return errors.New("ruta de ilustración inválida")
		}
		if err := checkOutputPath(target, dst); err != nil {
			return err
		}
		if regularFile(dst) {
			continue
		}
		if err := publishVideo(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// All output is in the user's selected download root. This code never opens a
// MediaCenter service, database or configuration for writing.
func (b *backend) exportLibrary(ctx context.Context, j *Job, settings Settings) error {
	b.libraryMu.Lock()
	defer b.libraryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.LibraryVersion != 2 {
		return nil
	}
	if j.LibraryRoot != "" {
		settings.Output = j.LibraryRoot
	}
	normalizeSubtitles(j)
	root := entityDirectory(settings.Output, j)
	if err := checkOutputPath(settings.Output, root); err != nil {
		return err
	}
	if identified(j) {
		if err := ensureCanonicalLocation(settings.Output, j.Metadata.Kind, contentID(j.Metadata.Kind, j.ID), filepath.Join(root, j.Metadata.Kind+".json")); err != nil {
			return err
		}
		if j.Metadata.Kind == "episode" {
			seasonRoot := filepath.Dir(root)
			seriesRoot := filepath.Dir(seasonRoot)
			if err := ensureCanonicalLocation(settings.Output, "season", contentID("season", j.Metadata.SeasonID), filepath.Join(seasonRoot, "season.json")); err != nil {
				return err
			}
			if err := ensureCanonicalLocation(settings.Output, "series", contentID("series", j.Metadata.SeriesID), filepath.Join(seriesRoot, "series.json")); err != nil {
				return err
			}
		}
	}
	if !identified(j) {
		j.ExportWarnings = append(j.ExportWarnings, "Identidad o numeración sin confirmar: descarga conservada en .pending; no se inventó una ficha")
	}
	stage := filepath.Dir(j.ManifestPath)
	provenance := objectJSON{"schemaVersion": 1, "application": "Netflix Downloader", "provider": "netflix", "downloadId": j.Key, "title": j.Metadata, "series": j.SeriesMetadata, "exportedAt": time.Now().UTC().Format(time.RFC3339), "warnings": append([]string{}, j.ExportWarnings...)}
	metadataPath := filepath.Join(stage, "metadata.json")
	if err := atomicJSON(metadataPath, provenance); err != nil {
		return err
	}
	if !containsString(j.ExportFiles, metadataPath) {
		j.ExportFiles = append(j.ExportFiles, metadataPath)
	}
	// Files stay in a scanner-hidden edition until all selected languages finish.
	if !j.LibraryPublished {
		final := filepath.Join(filepath.Dir(stage), j.Key)
		if _, err := os.Lstat(final); !os.IsNotExist(err) {
			return errors.New("la edición final ya existe; se conserva la descarga preparada")
		}
		if err := os.Rename(stage, final); err != nil {
			return err
		}
		rebaseJob(j, stage, final)
		j.LibraryPublished = true
	}
	if err := writeMediaIndex(j, true); err != nil {
		return err
	}
	if !identified(j) {
		return nil
	}
	fresh, err := buildPlayable(ctx, j, root, settings.FFmpeg)
	if err != nil {
		return err
	}
	attachArtwork(ctx, j, root, fresh)
	path := filepath.Join(root, j.Metadata.Kind+".json")
	old, err := readCatalog(path, j.Metadata.Kind, contentID(j.Metadata.Kind, j.ID))
	if err != nil {
		return err
	}
	doc, err := mergePlayable(old, fresh, j.Key)
	if err != nil {
		return err
	}
	// The playable card is committed only after every referenced asset exists.
	if err := atomicJSON(path, doc); err != nil {
		return err
	}
	j.CatalogPath = path
	if j.Metadata.Kind == "episode" {
		d := j.Metadata
		seasonRoot := filepath.Dir(root)
		seriesRoot := filepath.Dir(seasonRoot)
		art := mapJSON(doc["artwork"])
		if err := copyArtwork(root, seasonRoot, art); err != nil {
			return err
		}
		if err := copyArtwork(root, seriesRoot, art); err != nil {
			return err
		}
		season := coreMetadata(netflixlib.Details{}, "season", d.SeasonID, fmt.Sprintf("Temporada %d", d.SeasonNumber))
		season["seriesId"] = contentID("series", d.SeriesID)
		season["number"] = d.SeasonNumber
		season["artwork"] = art
		if poster := textJSON(art["poster"]); poster != "" {
			season["poster"] = poster
		}
		rel, _ := indexRelative(seasonRoot, path)
		if err := writeReference(filepath.Join(seasonRoot, "season.json"), season, "episodes", objectJSON{"id": contentID("episode", j.ID), "number": d.EpisodeNumber, "metadata": rel}); err != nil {
			return err
		}
		s := j.SeriesMetadata
		if s.Title == "" {
			s.Title = d.SeriesTitle
		}
		series := coreMetadata(s, "series", d.SeriesID, s.Title)
		series["artwork"] = art
		rel, _ = indexRelative(seriesRoot, filepath.Join(seasonRoot, "season.json"))
		if err := writeReference(filepath.Join(seriesRoot, "series.json"), series, "seasons", objectJSON{"id": contentID("season", d.SeasonID), "number": d.SeasonNumber, "metadata": rel}); err != nil {
			return err
		}
	}
	// Rewrite provenance after artwork/normalization so partial failures are explicit.
	provenance["warnings"] = append([]string{}, j.ExportWarnings...)
	return atomicJSON(filepath.Join(filepath.Dir(j.ManifestPath), "metadata.json"), provenance)
}

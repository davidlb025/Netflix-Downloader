package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type catalogEdit struct {
	Path   string
	Before []byte
	After  objectJSON
}

func ownedExportFile(j *Job, path string) bool {
	if !samePath(path, filepath.Join(filepath.Dir(j.ManifestPath), "metadata.json")) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4<<20 {
		return false
	}
	var doc objectJSON
	return json.Unmarshal(data, &doc) == nil && doc["application"] == "Netflix Downloader" && doc["downloadId"] == j.Key
}

// Build all metadata edits before invoking Windows Recycle Bin. Shared artwork
// and parent cards are retained; only the selected edition's references change.
func planCatalogRemoval(jobs []*Job, keys []string) ([]catalogEdit, []string, error) {
	wanted := map[string]bool{}
	for _, k := range keys {
		wanted[k] = true
	}
	edits := map[string]*catalogEdit{}
	order := []string{}
	remove := []string{}
	load := func(path, kind, id string) (objectJSON, error) {
		if edit := edits[path]; edit != nil {
			return edit.After, nil
		}
		doc, err := readCatalog(path, kind, id)
		if err != nil || doc == nil {
			return doc, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		edits[path] = &catalogEdit{path, data, doc}
		order = append(order, path)
		return doc, nil
	}
	for _, j := range jobs {
		if !wanted[j.Key] || j.CatalogPath == "" {
			continue
		}
		root := filepath.Dir(j.CatalogPath)
		if !withinRoot(root, j.ManifestPath) || !samePath(j.CatalogPath, filepath.Join(root, j.Metadata.Kind+".json")) {
			return nil, nil, errors.New("ruta de ficha gestionada no válida")
		}
		if err := checkOutputPath(root, j.CatalogPath); err != nil {
			return nil, nil, err
		}
		doc, err := load(j.CatalogPath, j.Metadata.Kind, contentID(j.Metadata.Kind, j.ID))
		if err != nil {
			return nil, nil, err
		}
		if doc == nil {
			continue
		}
		prefix := "d" + j.Key + "_"
		keep := func(items []any) []any {
			result := []any{}
			for _, v := range items {
				if !strings.HasPrefix(textJSON(mapJSON(v)["id"]), prefix) {
					result = append(result, v)
				}
			}
			return result
		}
		versions := keep(listJSON(doc["versions"]))
		doc["versions"] = versions
		external := mapJSON(doc["externalTracks"])
		external["audio"] = keep(listJSON(external["audio"]))
		external["subtitles"] = keep(listJSON(external["subtitles"]))
		doc["externalTracks"] = external
		defaults := mapJSON(doc["defaults"])
		if strings.HasPrefix(textJSON(defaults["versionId"]), prefix) {
			defaults = objectJSON{}
			if len(versions) > 0 {
				v := mapJSON(versions[0])
				id := textJSON(v["id"])
				defaults["versionId"] = id
				if tracks := listJSON(mapJSON(v["tracks"])["audio"]); len(tracks) > 0 {
					defaults["audioTrackId"] = mapJSON(tracks[0])["id"]
				} else {
					for _, a := range listJSON(external["audio"]) {
						for _, compatible := range listJSON(mapJSON(a)["compatibleVersions"]) {
							if compatible == id {
								defaults["audioTrackId"] = mapJSON(a)["id"]
								break
							}
						}
					}
				}
			}
		} else {
			for _, field := range []string{"audioTrackId", "subtitleTrackId"} {
				if strings.HasPrefix(textJSON(defaults[field]), prefix) {
					delete(defaults, field)
				}
			}
		}
		doc["defaults"] = defaults
		if len(versions) == 0 {
			remove = append(remove, j.CatalogPath)
			if j.Metadata.Kind == "episode" {
				path := filepath.Join(filepath.Dir(root), "season.json")
				parent, err := load(path, "season", contentID("season", j.Metadata.SeasonID))
				if err != nil {
					return nil, nil, err
				}
				if parent != nil {
					refs := []any{}
					for _, ref := range listJSON(parent["episodes"]) {
						if textJSON(mapJSON(ref)["id"]) != contentID("episode", j.ID) {
							refs = append(refs, ref)
						}
					}
					parent["episodes"] = refs
				}
			}
		}
	}
	result := []catalogEdit{}
	for _, path := range order {
		if !containsString(remove, path) {
			result = append(result, *edits[path])
		}
	}
	return result, remove, nil
}

func applyCatalogEdits(edits []catalogEdit) error {
	for i, edit := range edits {
		current, err := os.ReadFile(edit.Path)
		if err != nil || string(current) != string(edit.Before) {
			rollbackCatalogEdits(edits[:i])
			return errors.New("la ficha cambió durante el borrado; se conserva")
		}
		if err := atomicJSON(edit.Path, edit.After); err != nil {
			rollbackCatalogEdits(edits[:i])
			return err
		}
	}
	return nil
}
func rollbackCatalogEdits(edits []catalogEdit) {
	for i := len(edits) - 1; i >= 0; i-- {
		edit := edits[i]
		expected, _ := json.MarshalIndent(edit.After, "", "  ")
		current, err := os.ReadFile(edit.Path)
		if err == nil && string(current) == string(expected) {
			var before objectJSON
			if json.Unmarshal(edit.Before, &before) == nil {
				atomicJSON(edit.Path, before)
			}
		}
	}
}

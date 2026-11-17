package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

type ManageRequest struct {
	Keys []string
	Mode string
}
type ManageResult struct {
	Entries int `json:"entries"`
	Files   int `json:"files"`
}

func terminal(state string) bool {
	return state == "complete" || state == "error" || state == "cancelled"
}

// Only exact paths recorded by the application can be removed. Directories,
// links, wildcard patterns and active jobs are never accepted.
func managedPaths(jobs []*Job, keys []string) ([]string, error) {
	if len(keys) == 0 || len(keys) > 1000 {
		return nil, errors.New("selecciona al menos una entrada")
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		if key == "" || wanted[key] {
			return nil, errors.New("selección no válida")
		}
		wanted[key] = true
	}
	seen := map[string]bool{}
	var result []string
	for _, job := range jobs {
		if !wanted[job.Key] {
			continue
		}
		delete(wanted, job.Key)
		if !terminal(job.State) || job.SubtitleBusy {
			return nil, errors.New("cancela y espera a que termine la tarea antes de eliminarla")
		}
		paths := allJobPaths(job, true)
		for _, path := range paths {
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) || strings.ContainsAny(path, "*?\x00") {
				return nil, errors.New("ruta de archivo no válida")
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".mp4" && ext != ".m4a" && ext != ".vtt" && ext != ".ttml" && ext != ".srt" && ext != ".dfxp" && !(ext == ".json" && (samePath(path, job.ManifestPath) || ownedExportFile(job, path))) {
				return nil, errors.New("el archivo no pertenece a un formato gestionado")
			}
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, errors.New("no se pudo comprobar uno de los archivos")
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("no se eliminan carpetas ni enlaces")
			}
			if ext == ".json" && samePath(path, job.ManifestPath) {
				if err := checkMediaIndex(path, job.ID); err != nil {
					return nil, err
				}
			}
			key := strings.ToLower(filepath.Clean(path))
			if !seen[key] {
				seen[key] = true
				result = append(result, path)
			}
		}
	}
	if len(wanted) > 0 {
		return nil, errors.New("alguna entrada ya no existe; actualiza la selección")
	}
	return result, nil
}

func (b *backend) manage(raw json.RawMessage) (ManageResult, error) {
	var request ManageRequest
	if json.Unmarshal(raw, &request) != nil || (request.Mode != "history" && request.Mode != "trash") {
		return ManageResult{}, errors.New("elige quitar del historial o enviar a la Papelera")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Preflight all requested entries before any file operation.
	var paths []string
	var err error
	if request.Mode == "trash" {
		paths, err = managedPaths(b.jobs, request.Keys)
	} else {
		selected := map[string]bool{}
		for _, key := range request.Keys {
			selected[key] = true
		}
		if len(selected) == 0 || len(selected) != len(request.Keys) {
			return ManageResult{}, errors.New("selección no válida")
		}
		for _, j := range b.jobs {
			if selected[j.Key] {
				if !terminal(j.State) || j.SubtitleBusy {
					return ManageResult{}, errors.New("no se pueden quitar tareas activas")
				}
				delete(selected, j.Key)
			}
		}
		if len(selected) > 0 {
			err = errors.New("alguna entrada ya no existe")
		}
	}
	if err != nil {
		return ManageResult{}, err
	}
	if request.Mode == "trash" {
		b.libraryMu.Lock()
		defer b.libraryMu.Unlock()
		edits, cards, err := planCatalogRemoval(b.jobs, request.Keys)
		if err != nil {
			return ManageResult{}, err
		}
		if err = applyCatalogEdits(edits); err != nil {
			return ManageResult{}, err
		}
		for _, card := range cards {
			if !containsString(paths, card) {
				paths = append(paths, card)
			}
		}
		if err = recycleFiles(paths); err != nil {
			rollbackCatalogEdits(edits)
			return ManageResult{}, err
		}
	}
	selected := map[string]bool{}
	for _, key := range request.Keys {
		selected[key] = true
	}
	kept := make([]*Job, 0, len(b.jobs))
	for _, j := range b.jobs {
		if !selected[j.Key] {
			kept = append(kept, j)
		}
	}
	if err := atomicJSON(filepath.Join(b.home, "history.json"), kept); err != nil {
		return ManageResult{}, errors.New("no se pudo guardar el historial actualizado; revisa los permisos de la carpeta")
	}
	b.jobs = kept
	return ManageResult{Entries: len(request.Keys), Files: len(paths)}, nil
}

func recycleFiles(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := syscall.NewLazyDLL("ole32.dll")
	initialized, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if initialized == 0 || initialized == 1 {
		defer ole.NewProc("CoUninitialize").Call()
	}
	var list []uint16
	for _, path := range paths {
		s, err := syscall.UTF16FromString(path)
		if err != nil {
			return err
		}
		list = append(list, s...)
	}
	list = append(list, 0)
	type operation struct {
		Window   uintptr
		Function uint32
		From, To *uint16
		Flags    uint16
		Aborted  int32
		Mappings uintptr
		Title    *uint16
	}
	// ALLOWUNDO + WANTNUKEWARNING: Windows must warn rather than silently
	// falling back to permanent deletion when the destination cannot recycle.
	op := operation{Function: 3, From: &list[0], Flags: 0x0040 | 0x4000 | 0x0010 | 0x0004 | 0x0400 | 0x0080 | 0x1000 | 0x2000}
	result, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("SHFileOperationW").Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(list)
	if op.Aborted != 0 {
		return errors.New("Windows ha cancelado la operación; se conserva el historial")
	}
	if result != 0 {
		return fmt.Errorf("Windows no pudo enviar todos los archivos a la Papelera (código %d); se conserva el historial", result)
	}
	return nil
}

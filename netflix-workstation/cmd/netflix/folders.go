package main

import (
	"errors"
	"os"
	"path/filepath"
)

func openFolder(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("la carpeta debe tener una ruta absoluta")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("la carpeta se ha movido o eliminado")
	}
	// ShellExecute opens visible Explorer windows and handles paths with spaces.
	// A hidden explorer.exe process could leave the requested folder invisible.
	return openPath(filepath.Clean(path))
}

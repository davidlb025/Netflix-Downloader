package main

import (
		netflixlib "local.netflix/netflix-downloader"
_ "embed"
	"encoding/json"
	webview "github.com/jchv/go-webview2"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed browser_toolbar.html
var browserToolbarHTML string

type browserToolbar struct {
	window                                uintptr
	view                                  webview.WebView
	home, lastRequest, sessionStamp, note string
	session, errorNote                    bool
}

func newBrowserToolbar(view webview.WebView, home, address string) (*browserToolbar, error) {
	t := &browserToolbar{window: uintptr(view.Window()), view: view, home: home}
	t.refreshSession()
	if err := webview.AddToolbar(view, browserToolbarHTML, 64, t.command); err != nil {
		return nil, err
	}
	syscall.NewLazyDLL("user32.dll").NewProc("SetTimer").Call(t.window, 777, 1000, 0)
	return t, nil
}
func (t *browserToolbar) refreshSession() {
	path := filepath.Join(t.home, "session.dpapi")
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	stamp := info.ModTime().Format(time.RFC3339Nano)
	if stamp == t.sessionStamp {
		return
	}
	t.sessionStamp = stamp
	cookies, err := netflixlib.ReadSession(path)
	if err != nil {
		t.note = "No se pudo leer la sesión guardada"
		t.errorNote = true
		return
	}
	for _, name := range []string{"NetflixId", "SecureNetflixId"} {
		if value := cookies[name]; value != "" {
			if err := webview.SetCookie(t.view, name, value, ".netflix.com", "/"); err != nil {
				t.note = "No se pudo cargar la sesión en el navegador"
				t.errorNote = true
				return
			}
		}
	}
	t.session = true
	t.note = "Sesión guardada cargada"
	t.errorNote = false
	atomicJSON(filepath.Join(t.home, "browser-session-status.json"), map[string]bool{"imported": true})
}
func (t *browserToolbar) command(raw string) {
	var value struct{ Action, URL string }
	if json.Unmarshal([]byte(raw), &value) != nil {
		return
	}
	t.note = ""
	t.errorNote = false
	switch value.Action {
	case "back", "forward", "reload":
		webview.History(t.view, value.Action)
	case "navigate":
		address, err := websiteURL(value.URL)
		if err != nil {
			t.note = err.Error()
			t.errorNote = true
		} else {
			t.view.Navigate(address)
		}
	case "use":
		if err := t.capture(value.URL); err != nil {
			t.note = err.Error()
			t.errorNote = true
		} else {
			t.note = "Enlace enviado a la aplicación"
		}
	default:
		return
	}
	t.update()
}
func (t *browserToolbar) update() {
	source, _ := webview.Source(t.view)
	state, _ := json.Marshal(map[string]any{"address": source, "note": t.note, "error": t.errorNote, "session": t.session})
	webview.EvalToolbar(t.view, "window.updateBrowser && window.updateBrowser("+string(state)+")")
}
func (t *browserToolbar) message(msg, wp uintptr) bool {
	if msg != 0x113 || wp != 777 {
		return false
	}
	t.refreshSession()
	t.update()
	data, err := os.ReadFile(filepath.Join(t.home, "browser-request.json"))
	if err != nil || len(data) > 4096 {
		return true
	}
	var request browserRequest
	if json.Unmarshal(data, &request) == nil && request.At != t.lastRequest {
		t.lastRequest = request.At
		if address, err := websiteURL(request.URL); err == nil {
			if source, _ := webview.Source(t.view); source != address {
				t.view.Navigate(address)
			}
		}
	}
	return true
}
func (t *browserToolbar) capture(typed string) error {
	if typed != "" {
		_, err := captureBrowserLink(t.home, typed)
		return err
	}
	source, err := webview.Source(t.view)
	if err != nil {
		return err
	}
	_, err = captureBrowserLink(t.home, source)
	return err
}
func (t *browserToolbar) close() {
	syscall.NewLazyDLL("user32.dll").NewProc("KillTimer").Call(t.window, 777)
}

package main

import (
	"encoding/json"
	"errors"
	webview "github.com/jchv/go-webview2"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type BrowserCapture struct {
	URL      string `json:"url"`
	ID       int    `json:"id"`
	At       string `json:"at"`
	Consumed bool   `json:"consumed"`
}
type browserRequest struct {
	URL string `json:"url"`
	At  string `json:"at"`
}

func websiteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.netflix.com" && u.Hostname() != "netflix.com") {
		return "", errors.New("el navegador acepta enlaces HTTPS de netflix.com")
	}
	return u.String(), nil
}
func browserHome(base string) string {
	if path := os.Getenv("NETFLIX_DOWNLOADER_DATA_DIR"); path != "" {
		return path
	}
	local := filepath.Join(base, "local-profile")
	if info, err := os.Stat(local); err == nil && info.IsDir() {
		return local
	}
	return filepath.Join(os.Getenv("APPDATA"), "Netflix Downloader")
}
func readBrowserCapture(home string) BrowserCapture {
	var capture BrowserCapture
	data, err := os.ReadFile(filepath.Join(home, "browser-link.json"))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &capture) != nil {
		return BrowserCapture{}
	}
	if id, err := idFrom(capture.URL); err != nil || id != capture.ID {
		return BrowserCapture{}
	}
	if _, err := websiteURL(capture.URL); err != nil {
		return BrowserCapture{}
	}
	var used struct {
		At string `json:"at"`
	}
	if data, err := os.ReadFile(filepath.Join(home, "browser-link-consumed.json")); err == nil && len(data) < 1024 {
		json.Unmarshal(data, &used)
		capture.Consumed = used.At == capture.At && capture.At != ""
	}
	return capture
}

func captureBrowserLink(home, raw string) (BrowserCapture, error) {
	address, err := websiteURL(raw)
	if err != nil {
		return BrowserCapture{}, err
	}
	id, err := idFrom(address)
	if err != nil {
		return BrowserCapture{}, errors.New("abre una serie o película, o pega su enlace en la barra, antes de usarlo")
	}
	kind := "title"
	u, _ := url.Parse(address)
	if strings.Contains(u.Path, "/watch/") {
		kind = "watch"
	}
	capture := BrowserCapture{URL: "https://www.netflix.com/" + kind + "/" + strconv.Itoa(id), ID: id, At: time.Now().Format(time.RFC3339Nano)}
	if err = atomicJSON(filepath.Join(home, "browser-link.json"), capture); err != nil {
		return BrowserCapture{}, errors.New("no se pudo enviar el enlace a la aplicación")
	}
	return capture, nil
}

func consumeBrowserLink(home, at string) error {
	if len(at) > 100 || at == "" {
		return errors.New("identificador de enlace no válido")
	}
	return atomicJSON(filepath.Join(home, "browser-link-consumed.json"), map[string]string{"at": at})
}
func (b *backend) openBrowser(raw string) error {
	address, err := websiteURL(raw)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := atomicJSON(filepath.Join(b.home, "browser-request.json"), browserRequest{URL: address, At: time.Now().Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	if b.browserProcess != nil {
		return nil
	}
	cmd := hidden(executable, "--browser", address)
	if err := cmd.Start(); err != nil {
		return err
	}
	b.browserProcess = cmd
	go func() {
		cmd.Wait()
		b.mu.Lock()
		if b.browserProcess == cmd {
			b.browserProcess = nil
		}
		b.mu.Unlock()
	}()
	return nil
}

// The website runs in a separate process and profile, without the application's
// native bridge, cookies, filesystem commands or session decryption functions.
func runBrowser(base, raw string) error {
	address, err := websiteURL(raw)
	if err != nil {
		return err
	}
	home := browserHome(base)
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if file, err := os.OpenFile(filepath.Join(home, "browser.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		defer file.Close()
		log.SetOutput(file)
	}
	var toolbar *browserToolbar
	w := webview.NewWithOptions(webview.WebViewOptions{AutoFocus: false, DefaultPermissions: true, TopInset: 64, MessageHandler: func(window, msg, wp, lp uintptr) bool {
		if toolbar != nil {
			return toolbar.message(msg, wp)
		}
		return false
	}, DataPath: filepath.Join(home, "website-browser"), WindowOptions: webview.WindowOptions{Title: "Netflix Downloader · Navegador", Width: 1220, Height: 820, Center: true}})
	if w == nil {
		return errors.New("no se pudo abrir el navegador WebView2")
	}
	defer w.Destroy()
	w.SetSize(880, 620, webview.HintMin)
	toolbar, err = newBrowserToolbar(w, home, address)
	if err != nil {
		return err
	}
	defer toolbar.close()
	w.Navigate(address)
	syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow").Call(uintptr(w.Window()), 1)
	w.Run()
	return nil
}

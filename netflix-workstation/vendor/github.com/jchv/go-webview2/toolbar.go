package webview2

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jchv/go-webview2/pkg/edge"
	"strings"
)

// AddToolbar creates a separate trusted local controller above the website.
// Its message handler and document are never exposed to the remote controller.
func AddToolbar(view WebView, html string, height int32, message func(string)) error {
	w, ok := view.(*webview)
	if !ok {
		return errors.New("unsupported webview")
	}
	if w.toolbar != nil {
		return errors.New("toolbar already exists")
	}
	c := edge.NewChromium()
	c.DataPath = w.dataPath
	c.ViewportHeight = height
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(nonce[:])
	html = strings.ReplaceAll(html, "__LOCAL_TOOLBAR_TOKEN__", token)
	c.MessageSourceCallback = func(source, value string) {
		if source != "about:blank" || len(value) > 16384 {
			return
		}
		var envelope struct{ Token string }
		if json.Unmarshal([]byte(value), &envelope) != nil || subtle.ConstantTimeCompare([]byte(envelope.Token), []byte(token)) != 1 {
			return
		}
		message(value)
	}

	if !c.Embed(w.hwnd) {
		return errors.New("toolbar initialization failed")
	}
	w.toolbar = c
	c.Resize()
	if err := c.Show(); err != nil {
		return err
	}
	if settings, err := c.GetSettings(); err == nil {
		settings.PutAreDefaultContextMenusEnabled(false)
		settings.PutAreDevToolsEnabled(false)
	}
	// NavigateToString has the documented about:blank message origin. The
	// controller is isolated from the website and each document has a nonce.
	c.NavigateToString(html)
	return nil
}
func EvalToolbar(view WebView, script string) {
	if w, ok := view.(*webview); ok && w.toolbar != nil {
		w.toolbar.Eval(script)
	}
}

func SetCookie(view WebView, name, value, domain, path string) error {
	w, ok := view.(*webview)
	if !ok {
		return errors.New("unsupported webview")
	}
	c, ok := w.browser.(*edge.Chromium)
	if !ok {
		return errors.New("unsupported browser")
	}
	if !strings.HasPrefix(domain, ".") {
		return errors.New("cookie domain required")
	}
	return c.SetSessionCookie(name, value, domain, path)
}

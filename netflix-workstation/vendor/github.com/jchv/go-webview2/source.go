package webview2

import (
	"errors"
	"github.com/jchv/go-webview2/pkg/edge"
)

// Source exposes only the current document URL. No browser session is exported.
func Source(view WebView) (string, error) {
	w, ok := view.(*webview)
	if !ok {
		return "", errors.New("unsupported webview")
	}
	browser, ok := w.browser.(*edge.Chromium)
	if !ok {
		return "", errors.New("unsupported browser")
	}
	return browser.Source()
}

func History(view WebView, action string) error {
	w, ok := view.(*webview)
	if !ok {
		return errors.New("unsupported webview")
	}
	browser, ok := w.browser.(*edge.Chromium)
	if !ok {
		return errors.New("unsupported browser")
	}
	return browser.History(action)
}

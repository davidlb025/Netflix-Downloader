package edge

import (
	"errors"
	"golang.org/x/sys/windows"
	"unsafe"
)

func (e *Chromium) History(action string) error {
	var result uintptr
	switch action {
	case "back":
		result, _, _ = e.webview.vtbl.GoBack.Call(uintptr(unsafe.Pointer(e.webview)))
	case "forward":
		result, _, _ = e.webview.vtbl.GoForward.Call(uintptr(unsafe.Pointer(e.webview)))
	case "reload":
		result, _, _ = e.webview.vtbl.Reload.Call(uintptr(unsafe.Pointer(e.webview)))
	default:
		return errors.New("unsupported navigation")
	}
	if result != 0 {
		return errors.New("navigation unavailable")
	}
	return nil
}

// Source returns the actual top-level document URL from WebView2.
func (e *Chromium) Source() (string, error) {
	var value *uint16
	result, _, err := e.webview.vtbl.GetSource.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(&value)))
	if result != 0 {
		return "", err
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(value))
	return windows.UTF16PtrToString(value), nil
}

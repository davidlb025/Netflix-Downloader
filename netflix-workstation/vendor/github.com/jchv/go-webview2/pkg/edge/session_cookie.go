package edge

import (
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

type cookieManagerVtbl struct {
	_IUnknownVtbl
	CreateCookie, CopyCookie, GetCookies, AddOrUpdateCookie, DeleteCookie, DeleteCookies, DeleteCookiesWithDomainAndPath, DeleteAllCookies ComProc
}
type cookieManager struct{ vtbl *cookieManagerVtbl }
type cookieVtbl struct {
	_IUnknownVtbl
	GetName, GetValue, PutValue, GetDomain, GetPath, GetExpires, PutExpires, GetIsHttpOnly, PutIsHttpOnly, GetSameSite, PutSameSite, GetIsSecure, PutIsSecure, GetIsSession ComProc
}
type sessionCookie struct{ vtbl *cookieVtbl }

// Uses Microsoft WebView2's supported cookie API; values never enter JavaScript.
func (e *Chromium) SetSessionCookie(name, value, domain, path string) error {
	args := make([]*uint16, 4)
	for i, text := range []string{name, value, domain, path} {
		ptr, err := windows.UTF16PtrFromString(text)
		if err != nil {
			return errors.New("invalid session cookie")
		}
		args[i] = ptr
	}
	iid := NewGUID("9E8F0CF8-E670-4B5E-B2BC-73E061E3184C")
	var version *ICoreWebView2_2
	result, _, _ := e.webview.vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&version)))
	if result != 0 || version == nil {
		return errors.New("WebView2 cookie API unavailable")
	}
	defer version.vtbl.Release.Call(uintptr(unsafe.Pointer(version)))
	var manager *cookieManager
	result, _, _ = version.vtbl.GetCookieManager.Call(uintptr(unsafe.Pointer(version)), uintptr(unsafe.Pointer(&manager)))
	if result != 0 || manager == nil {
		return errors.New("cookie manager unavailable")
	}
	defer manager.vtbl.Release.Call(uintptr(unsafe.Pointer(manager)))
	var cookie *sessionCookie
	result, _, _ = manager.vtbl.CreateCookie.Call(uintptr(unsafe.Pointer(manager)), uintptr(unsafe.Pointer(args[0])), uintptr(unsafe.Pointer(args[1])), uintptr(unsafe.Pointer(args[2])), uintptr(unsafe.Pointer(args[3])), uintptr(unsafe.Pointer(&cookie)))
	runtime.KeepAlive(args)
	if result != 0 || cookie == nil {
		return errors.New("session cookie creation failed")
	}
	defer cookie.vtbl.Release.Call(uintptr(unsafe.Pointer(cookie)))
	result, _, _ = cookie.vtbl.PutIsHttpOnly.Call(uintptr(unsafe.Pointer(cookie)), 1)
	if result != 0 {
		return errors.New("cookie protection failed")
	}
	result, _, _ = cookie.vtbl.PutIsSecure.Call(uintptr(unsafe.Pointer(cookie)), 1)
	if result != 0 {
		return errors.New("cookie security failed")
	}
	result, _, _ = manager.vtbl.AddOrUpdateCookie.Call(uintptr(unsafe.Pointer(manager)), uintptr(unsafe.Pointer(cookie)))
	if result != 0 {
		return errors.New("session cookie update failed")
	}
	return nil
}

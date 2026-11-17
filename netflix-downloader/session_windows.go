package netflix

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

type dataBlob struct {
	Size uint32
	Data *byte
}

// ReadSession reads only a session file explicitly supplied by the local user.
// It does not inspect installed browsers or send credentials to a third party.
func ReadSession(path string) (map[string]string, error) {
	protected, err := os.ReadFile(path)
	if err != nil || len(protected) == 0 || len(protected) > 4<<20 {
		return nil, errors.New("no se pudo leer el archivo local de sesión")
	}
	if decoded, e := base64.StdEncoding.DecodeString(string(protected)); e == nil {
		protected = decoded
	}
	input := dataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var output dataBlob
	decrypt := syscall.NewLazyDLL("crypt32.dll").NewProc("CryptUnprotectData")
	ok, _, _ := decrypt.Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	runtime.KeepAlive(protected)
	if ok == 0 || output.Data == nil {
		return nil, errors.New("Windows no pudo abrir la sesión para el usuario actual")
	}
	defer syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree").Call(uintptr(unsafe.Pointer(output.Data)))
	plain := unsafe.Slice(output.Data, output.Size)
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var cookies map[string]string
	if json.Unmarshal(plain, &cookies) != nil || cookies["NetflixId"] == "" || cookies["SecureNetflixId"] == "" {
		return nil, errors.New("la sesión no contiene las dos cookies de Netflix")
	}
	return cookies, nil
}

// SaveSession encrypts only credentials entered into this application's local
// interface. Windows binds the resulting file to the current user account.
func SaveSession(path string, cookies map[string]string) error {
	if cookies["NetflixId"] == "" || cookies["SecureNetflixId"] == "" {
		return errors.New("introduce las dos cookies")
	}
	plain, err := json.Marshal(cookies)
	if err != nil {
		return err
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	input := dataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var output dataBlob
	ok, _, _ := syscall.NewLazyDLL("crypt32.dll").NewProc("CryptProtectData").Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	runtime.KeepAlive(plain)
	if ok == 0 || output.Data == nil {
		return errors.New("Windows no pudo proteger la sesión")
	}
	defer syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree").Call(uintptr(unsafe.Pointer(output.Data)))
	return os.WriteFile(path, unsafe.Slice(output.Data, output.Size), 0600)
}

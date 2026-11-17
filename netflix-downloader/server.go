// Package portable transports the existing CDM host through a disposable QEMU
// guest. Only explicitly selected files cross the loopback connection.
package netflix

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Render      bool `json:"render"`
	Job         bool `json:"job"`
	Certificate bool `json:"certificate"`
}

type PortableResult struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

type Exchange struct {
	Token                                                         string
	Files                                                         map[string]string
	Challenge, Response, Output                                   string
	Config                                                        Config
	Events, Frames                                                io.Writer
	Done                                                          chan PortableResult
	Started                                                       chan struct{}
	start                                                         sync.Once
	mu                                                            sync.Mutex
	challengeWritten, outputWritten, framesWritten, resultWritten bool
}

func NewExchange() (*Exchange, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	return &Exchange{Token: hex.EncodeToString(token[:]), Done: make(chan PortableResult, 1), Started: make(chan struct{})}, nil
}

func (x *Exchange) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+x.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.Method == "GET" && r.URL.Path == "/config":
		x.start.Do(func() { close(x.Started) })
		json.NewEncoder(w).Encode(x.Config)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/file/"):
		path, ok := x.Files[strings.TrimPrefix(r.URL.Path, "/file/")]
		if !ok || path == "" {
			http.NotFound(w, r)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			http.Error(w, "input unavailable", 500)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, "invalid input", 500)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
		io.Copy(w, file)
	case r.Method == "POST" && r.URL.Path == "/challenge":
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.challengeWritten {
			http.Error(w, "challenge already received", 409)
			return
		}
		session, err := base64.StdEncoding.DecodeString(r.Header.Get("X-CDM-Session"))
		if err != nil || len(session) == 0 || len(session) > 1024 {
			http.Error(w, "invalid session", 400)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil || len(body) == 0 {
			http.Error(w, "invalid challenge", 400)
			return
		}
		if err = os.WriteFile(x.Challenge+".session", session, 0600); err == nil {
			err = writeNew(x.Challenge, strings.NewReader(string(body)), 16<<20)
		}
		if err != nil {
			http.Error(w, "cannot publish challenge", 500)
			return
		}
		x.challengeWritten = true
	case r.Method == "GET" && r.URL.Path == "/license":
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			data, err := os.ReadFile(x.Response)
			if err == nil && len(data) > 0 && len(data) <= 16<<20 {
				w.Write(data)
				return
			}
			select {
			case <-ctx.Done():
				http.Error(w, "license unavailable", 504)
				return
			case <-ticker.C:
			}
		}
	case r.Method == "POST" && r.URL.Path == "/events":
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || !json.Valid(body) {
			http.Error(w, "invalid event", 400)
			return
		}
		var event struct {
			Event string `json:"event"`
		}
		json.Unmarshal(body, &event)
		if event.Event == "" {
			http.Error(w, "invalid event", 400)
			return
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if event.Event == "challengeReady" && !x.challengeWritten {
			http.Error(w, "challenge missing", 409)
			return
		}
		if _, err = x.Events.Write(append(body, '\n')); err != nil {
			http.Error(w, "event transport closed", 500)
		}
	case r.Method == "POST" && r.URL.Path == "/frames" && x.Config.Render:
		x.mu.Lock()
		if x.framesWritten {
			x.mu.Unlock()
			http.Error(w, "stream already received", 409)
			return
		}
		x.framesWritten = true
		x.mu.Unlock()
		if _, err := io.Copy(x.Frames, r.Body); err != nil {
			http.Error(w, "video transport closed", 500)
		}
	case r.Method == "POST" && r.URL.Path == "/output" && x.Config.Job:
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.outputWritten {
			http.Error(w, "output already received", 409)
			return
		}
		if err := writeNew(x.Output, r.Body, 1<<30); err != nil {
			http.Error(w, "cannot publish output", 500)
			return
		}
		x.outputWritten = true
	case r.Method == "POST" && r.URL.Path == "/result":
		var result PortableResult
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&result) != nil || result.Code < 0 || result.Code > 255 {
			http.Error(w, "invalid result", 400)
			return
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.resultWritten {
			http.Error(w, "result already received", 409)
			return
		}
		x.resultWritten = true
		if result.Code == 0 && x.Config.Job && !x.outputWritten {
			result = PortableResult{Code: 1, Error: "guest output missing"}
		}
		x.Done <- result
	default:
		http.NotFound(w, r)
	}
}

func writeNew(path string, input io.Reader, limit int64) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	n, err := io.Copy(file, io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("file too large")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

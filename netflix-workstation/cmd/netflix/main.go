package main

import (
		netflixlib "local.netflix/netflix-downloader"
"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
)

//go:embed ui.html
var ui string

//go:embed features.js
var featureScript string

//go:embed downloader-logo.svg
var downloaderLogo string

const appVersion = "v1.0"

type Settings struct {
	Output              string `json:"output"`
	CDM                 string `json:"cdm"`
	FFmpeg              string `json:"ffmpeg"`
	SeriesFoldersByName bool   `json:"seriesFoldersByName"`
	IndependentAudioMP4 bool   `json:"independentAudioMP4"`
}
type Job struct {
	LibraryRoot         string          `json:"libraryRoot,omitempty"`
	LibraryPublished    bool            `json:"libraryPublished,omitempty"`
	LibraryVersion      int             `json:"libraryVersion,omitempty"`
	Metadata            netflixlib.Details `json:"metadata,omitempty"`
	SeriesMetadata      netflixlib.Details `json:"seriesMetadata,omitempty"`
	CatalogPath         string          `json:"catalogPath,omitempty"`
	ExportFiles         []string        `json:"exportFiles,omitempty"`
	ExportWarnings      []string        `json:"exportWarnings,omitempty"`
	MultiAudio          bool            `json:"multiAudio,omitempty"`
	AudioLanguages      []string        `json:"audioLanguages,omitempty"`
	SeriesFoldersByName bool            `json:"seriesFoldersByName,omitempty"`
	IndependentAudioMP4 bool            `json:"independentAudioMP4,omitempty"`
	BundleSuffix        string          `json:"bundleSuffix,omitempty"`
	ManifestPath        string          `json:"manifestPath,omitempty"`
	VideoFiles          []VideoFile     `json:"videoFiles,omitempty"`
	AudioFiles          []AudioFile     `json:"audioFiles,omitempty"`
	SubtitleTracks      []SubtitleFile  `json:"subtitleTracks,omitempty"`
	Series              string          `json:"series,omitempty"`
	Season              int             `json:"season,omitempty"`
	Episode             float64         `json:"episode,omitempty"`
	SubtitleMode        string          `json:"subtitleMode,omitempty"`
	SubtitleLanguages   []string        `json:"subtitleLanguages,omitempty"`
	SubtitleDirectory   string          `json:"subtitleDirectory,omitempty"`
	SubtitleAnchor      string          `json:"subtitleAnchor,omitempty"`
	Key                 string          `json:"key"`
	ID                  int             `json:"id"`
	Title               string          `json:"title"`
	Height              int             `json:"height"`
	Audio               string          `json:"audio"`
	State               string          `json:"state"`
	Detail              string          `json:"detail"`
	Progress            int             `json:"progress"`
	Path                string          `json:"path"`
	Bytes               int64           `json:"bytes"`
	Created             string          `json:"created"`
	Subtitles           []string        `json:"subtitles,omitempty"`
	SubtitleTotal       int             `json:"subtitleTotal,omitempty"`
	SubtitleFailed      int             `json:"subtitleFailed,omitempty"`
	SubtitleBusy        bool            `json:"subtitleBusy,omitempty"`
}
type Analysis struct {
	Metadata          netflixlib.Details `json:"metadata"`
	SeriesMetadata    netflixlib.Details `json:"seriesMetadata"`
	Kind              string          `json:"kind"`
	SeriesName        string          `json:"seriesName"`
	Series            *netflixlib.Series `json:"series,omitempty"`
	Season            int             `json:"season,omitempty"`
	Episode           float64         `json:"episode,omitempty"`
	SubtitleLanguages []string        `json:"subtitleLanguages"`
	Source            string          `json:"source"`
	Busy              bool            `json:"busy"`
	ID                int             `json:"id"`
	Title             string          `json:"title"`
	Heights           []int           `json:"heights"`
	Audio             []string        `json:"audio"`
	Error             string          `json:"error"`
}
type Snapshot struct {
	Settings Settings       `json:"settings"`
	Jobs     []Job          `json:"jobs"`
	Analysis Analysis       `json:"analysis"`
	Session  bool           `json:"session"`
	Ready    bool           `json:"ready"`
	Problems []string       `json:"problems"`
	Catalog  CatalogState   `json:"catalog"`
	Browser  BrowserCapture `json:"browser"`
}
type backend struct {
	libraryMu      sync.Mutex
	mu             sync.Mutex
	settings       Settings
	jobs           []*Job
	analysis       Analysis
	catalog        CatalogState
	home, base     string
	pending        chan *Job
	cancels        map[string]context.CancelFunc
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	bridgeSeen     sync.Once
	browserProcess *exec.Cmd
}

func main() {
	runtime.LockOSThread()
	executable, _ := os.Executable()
	base := filepath.Dir(executable)
	if len(os.Args) > 1 && os.Args[1] == "--browser" {
		address := "https://www.netflix.com/browse"
		if len(os.Args) > 2 {
			address = os.Args[2]
		}
		if err := runBrowser(base, address); err != nil {
			showError(err.Error())
		}
		return
	}
	home := os.Getenv("NETFLIX_DOWNLOADER_DATA_DIR")
	if home == "" {
		localProfile := filepath.Join(base, "local-profile")
		if info, err := os.Stat(localProfile); err == nil && info.IsDir() {
			home = localProfile
		} else {
			home = filepath.Join(os.Getenv("APPDATA"), "Netflix Downloader")
		}
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		showError(err.Error())
		return
	}
	if file, err := os.OpenFile(filepath.Join(home, "application.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		defer file.Close()
		log.SetOutput(file)
	}
	log.Print("starting interface")
	ctx, cancel := context.WithCancel(context.Background())
	b := &backend{home: home, base: base, pending: make(chan *Job, 128), cancels: map[string]context.CancelFunc{}, ctx: ctx, cancel: cancel}
	b.settings = Settings{Output: filepath.Join(os.Getenv("USERPROFILE"), "Videos", "Netflix Downloader"), CDM: filepath.Join(base, "assets", "cdm", "libwidevinecdm.so"), FFmpeg: defaultFFmpeg(base), SeriesFoldersByName: true}
	if value, err := os.ReadFile(filepath.Join(home, "settings.json")); err == nil {
		json.Unmarshal(value, &b.settings)
	}
	if value, err := os.ReadFile(filepath.Join(home, "history.json")); err == nil {
		json.Unmarshal(value, &b.jobs)
		for _, j := range b.jobs {
			j.SubtitleBusy = false
			if j.State != "complete" && j.State != "error" && j.State != "cancelled" {
				j.State = "cancelled"
				j.Detail = "Interrumpida al cerrar la aplicación. Vuelve a añadir el enlace."
			}
		}
	}
	b.wg.Add(1)
	go b.work()
	defer b.shutdown()
	width, height := uint(1256), uint(790)
	if os.Getenv("NETFLIX_DOWNLOADER_COMPACT") == "1" {
		width, height = 880, 620
	}
	w := webview.NewWithOptions(webview.WebViewOptions{Debug: false, AutoFocus: true, DataPath: filepath.Join(home, "browser"), WindowOptions: webview.WindowOptions{Title: "Netflix Downloader", Width: width, Height: height, Center: true}})
	log.Print("webview initialized")
	if w == nil {
		showError("No se pudo iniciar WebView2. Instala el runtime de Microsoft WebView2.")
		return
	}
	defer w.Destroy()
	w.SetSize(880, 620, webview.HintMin)
	if os.Getenv("NETFLIX_DOWNLOADER_COMPACT") == "1" {
		w.SetSize(880, 620, webview.HintFixed)
	}
	if err := w.Bind("native", b.request); err != nil {
		showError(err.Error())
		return
	}
	pageHTML := strings.Replace(ui, "<!--DOWNLOADER_LOGO_SYMBOL-->", inlineDownloaderLogo(), 1)
	pageHTML = strings.Replace(pageHTML, "<!--APP_VERSION-->", appVersion, 1)
	pageHTML = strings.Replace(pageHTML, "<!--FEATURE_SCRIPT-->", "<script>"+featureScript+"</script>", 1)
	w.Navigate("data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(pageHTML)))
	log.Print("local interface submitted")
	syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow").Call(uintptr(w.Window()), 1)
	w.Run()
}
func inlineDownloaderLogo() string {
	const closingTag = "</svg>"
	start := strings.Index(downloaderLogo, ">")
	end := strings.LastIndex(downloaderLogo, closingTag)
	if start < 0 || end <= start {
		return ""
	}
	paths := strings.TrimSpace(downloaderLogo[start+1 : end])
	return `<symbol id="i-brand" viewBox="0 0 32 32">` + paths + `</symbol>`
}
func showError(message string) {
	text, _ := syscall.UTF16PtrFromString(message)
	title, _ := syscall.UTF16PtrFromString("Netflix Downloader")
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}

func (b *backend) sessionPath() string { return filepath.Join(b.home, "session.dpapi") }
func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nd-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func (b *backend) persist() { atomicJSON(filepath.Join(b.home, "history.json"), b.jobs) }
func (b *backend) update(j *Job, state, detail string, progress int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j.State = state
	j.Detail = detail
	j.Progress = progress
	if state == "complete" || state == "error" || state == "cancelled" {
		b.persist()
	}
}
func idFrom(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if regexp.MustCompile(`^\d{6,12}$`).MatchString(raw) {
		n, e := strconv.Atoi(raw)
		return n, e
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.netflix.com" && u.Hostname() != "netflix.com") {
		return 0, errors.New("pega un enlace https://www.netflix.com/watch/… o su ID")
	}
	if value := u.Query().Get("jbv"); value != "" {
		return idFrom(value)
	}
	m := regexp.MustCompile(`/(?:watch|title)/(\d{6,12})(?:/|$)`).FindStringSubmatch(u.Path)
	if len(m) != 2 {
		return 0, errors.New("el enlace no contiene un ID de reproducción")
	}
	return strconv.Atoi(m[1])
}
func (b *backend) deps(settings Settings) []string {
	var out []string
	if _, err := os.Stat(settings.CDM); err != nil {
		out = append(out, "Indica el CDM oficial compatible en Ajustes.")
	}
	if ffmpeg, err := exec.LookPath(settings.FFmpeg); err != nil {
		out = append(out, "No se encuentra FFmpeg. Revisa su ruta en Ajustes.")
	} else if !regularFile(filepath.Join(filepath.Dir(ffmpeg), "ffprobe.exe")) {
		out = append(out, "No se encuentra ffprobe.exe junto a FFmpeg.")
	}
	for _, component := range []string{"cdm-portable.exe", "runtime/qemu-system-x86_64.exe", "runtime/vmlinuz", "runtime/initrd.gz"} {
		if _, err := os.Stat(filepath.Join(b.base, "assets", component)); err != nil {
			out = append(out, "Falta un componente del motor portátil: "+component)
		}
	}
	if _, err := os.Stat(filepath.Join(b.base, "assets", "drm-probe.exe")); err != nil {
		out = append(out, "Falta assets/drm-probe.exe junto al programa.")
	}
	return out
}

func defaultFFmpeg(base string) string {
	bundled := filepath.Join(base, "assets", "ffmpeg.exe")
	if regularFile(bundled) {
		return bundled
	}
	return "ffmpeg"
}

func (b *backend) request(action string, raw json.RawMessage) (any, error) {
	b.bridgeSeen.Do(func() { log.Print("local interface connected") })
	switch action {
	case "snapshot":
		b.mu.Lock()
		defer b.mu.Unlock()
		jobs := make([]Job, 0, len(b.jobs))
		for _, j := range b.jobs {
			jobs = append(jobs, *j)
		}
		_, err := os.Stat(b.sessionPath())
		problems := b.deps(b.settings)
		return Snapshot{Settings: b.settings, Jobs: jobs, Analysis: b.analysis, Session: err == nil, Ready: len(problems) == 0, Problems: problems, Catalog: b.catalog, Browser: readBrowserCapture(b.home)}, nil
	case "catalog":
		return b.discover(raw)
	case "manage":
		return b.manage(raw)
	case "metadata":
		return b.refreshMetadata(raw)
	case "subtitles":
		return b.fetchSubtitles(raw)
	case "browser":
		var value struct{ URL string }
		json.Unmarshal(raw, &value)
		if value.URL == "" {
			value.URL = "https://www.netflix.com/browse"
		}
		return true, b.openBrowser(value.URL)
	case "browser-link":
		capture := readBrowserCapture(b.home)
		if capture.URL == "" {
			return nil, errors.New("todavía no se ha enviado un enlace desde el navegador")
		}
		return capture, nil
	case "browser-link-ack":
		var value struct{ At string }
		if json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("enlace no válido")
		}
		return true, consumeBrowserLink(b.home, value.At)
	case "session":
		var value struct{ Normal, Secure string }
		if json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("datos no válidos")
		}
		cookies := map[string]string{"NetflixId": strings.TrimSpace(value.Normal), "SecureNetflixId": strings.TrimSpace(value.Secure)}
		if err := netflixlib.SaveSession(b.sessionPath(), cookies); err != nil {
			return nil, err
		}
		return true, nil
	case "settings":
		var settings Settings
		if json.Unmarshal(raw, &settings) != nil {
			return nil, errors.New("ajustes no válidos")
		}
		if !filepath.IsAbs(settings.Output) || settings.CDM == "" || settings.FFmpeg == "" {
			return nil, errors.New("completa las rutas de salida, CDM y FFmpeg")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if err := atomicJSON(filepath.Join(b.home, "settings.json"), settings); err != nil {
			return nil, err
		}
		b.settings = settings
		return true, nil
	case "analyze":
		var value struct{ URL string }
		json.Unmarshal(raw, &value)
		value.URL = strings.TrimSpace(value.URL)
		id, err := idFrom(value.URL)
		b.mu.Lock()
		if b.analysis.Busy {
			b.mu.Unlock()
			return nil, errors.New("ya se está analizando un enlace")
		}
		if err != nil {
			b.analysis = Analysis{Source: value.URL, Error: err.Error()}
			b.mu.Unlock()
			return nil, err
		}
		b.analysis = Analysis{Busy: true, ID: id, Source: value.URL}
		b.mu.Unlock()
		b.wg.Add(1)
		go b.analyze(id, value.URL)
		return true, nil
	case "enqueue":
		return b.enqueueSelection(raw)
	case "cancel":
		var value struct{ Key string }
		json.Unmarshal(raw, &value)
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, j := range b.jobs {
			if j.Key == value.Key {
				if cancel := b.cancels[j.Key]; cancel != nil {
					cancel()
				}
				if j.State == "queued" {
					j.State = "cancelled"
					j.Detail = "Cancelada"
					b.persist()
				}
				return true, nil
			}
		}
		return nil, errors.New("tarea no encontrada")
	case "open":
		var value struct {
			Key    string
			Folder bool
			Asset  string
		}
		json.Unmarshal(raw, &value)
		b.mu.Lock()
		var path string
		var folder string
		for _, j := range b.jobs {
			if j.Key == value.Key && j.State == "complete" {
				path = j.Path
				folder = filepath.Dir(path)
				if j.ManifestPath != "" {
					folder = filepath.Dir(j.ManifestPath)
				}
				if value.Asset == "audios" && j.MultiAudio {
					if j.IndependentAudioMP4 {
						folder = filepath.Join(folder, "Videos")
					} else {
						folder = filepath.Join(folder, "Audios")
					}
					value.Folder = true
				}
				break
			}
		}
		b.mu.Unlock()
		if path == "" {
			return nil, errors.New("el archivo aún no está listo")
		}
		if value.Folder {
			return true, openFolder(folder)
		}
		if _, err := os.Stat(path); err != nil {
			return nil, errors.New("el archivo se ha movido o eliminado")
		}
		return true, openPath(path)
	case "folder":
		b.mu.Lock()
		path := b.settings.Output
		b.mu.Unlock()
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, err
		}
		return true, openFolder(path)
	case "netflix":
		return true, b.openBrowser("https://www.netflix.com/browse")
	default:
		return nil, errors.New("acción desconocida")
	}
}

func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
func openPath(path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := syscall.NewLazyDLL("ole32.dll")
	initialized, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if initialized == 0 || initialized == 1 {
		defer ole.NewProc("CoUninitialize").Call()
	}
	operation, _ := syscall.UTF16PtrFromString("open")
	name, _ := syscall.UTF16PtrFromString(path)
	result, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(operation)), uintptr(unsafe.Pointer(name)), 0, 0, 1)
	if result <= 32 {
		return errors.New("Windows no pudo abrir el destino")
	}
	return nil
}
func (b *backend) work() {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case j := <-b.pending:
			b.runJob(j)
		}
	}
}
func (b *backend) runJob(j *Job) {
	b.mu.Lock()
	if j.State == "cancelled" {
		b.mu.Unlock()
		return
	}
	settings := b.settings
	ctx, cancel := context.WithCancel(b.ctx)
	b.cancels[j.Key] = cancel
	b.mu.Unlock()
	defer cancel()
	defer func() { b.mu.Lock(); delete(b.cancels, j.Key); b.mu.Unlock() }()
	err := b.download(ctx, j, settings)
	if err != nil {
		if ctx.Err() != nil {
			b.update(j, "cancelled", "Cancelada", 0)
		} else {
			b.update(j, "error", err.Error(), 0)
		}
	}
}
func (b *backend) downloadVariant(ctx context.Context, j *Job, settings Settings, selectedAudio string, maxHeight int, status func(string, string, int)) error {
	status("downloading", "Consultando las pistas disponibles…", 0)
	cookies, err := netflixlib.ReadSession(b.sessionPath())
	if err != nil {
		return err
	}
	client, err := netflixlib.NewForPlatform(ctx, cookies, "linux")
	if err != nil {
		return err
	}
	playback, err := client.Manifest(ctx, j.ID)
	if err != nil {
		return client.Redact(err)
	}
	video, err := playback.DownloadVideo(maxHeight)
	if err != nil {
		return err
	}
	audio, err := playback.SelectAudio(selectedAudio)
	if err != nil {
		return err
	}
	folder, err := os.MkdirTemp(b.home, "transfer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(folder)
	for i, track := range []netflixlib.NetflixTrack{video, audio} {
		last := time.Time{}
		name := []string{"vídeo", "audio"}[i]
		if err = client.DownloadTrack(ctx, track, filepath.Join(folder, fmt.Sprintf("track-%d.enc.mp4", i)), func(n int64) {
			if time.Since(last) > 300*time.Millisecond {
				status("downloading", fmt.Sprintf("Descargando %s · %.1f MB", name, float64(n)/1048576), i*15)
				last = time.Now()
			}
		}); err != nil {
			return err
		}
	}
	if err = os.MkdirAll(settings.Output, 0755); err != nil {
		return err
	}
	destination := filepath.Join(folder, "export.mp4")
	worker := hidden(filepath.Join(b.base, "assets", "drm-probe.exe"), "-provider", "netflix", "-session", b.sessionPath(), "-id", strconv.Itoa(j.ID), "-host", "linux", "-cdm", settings.CDM, "-helper", filepath.Join(b.base, "assets", "cdm-portable.exe"), "-decrypt-media", folder, "-bind-manifest", "-render-video", destination, "-ffmpeg", settings.FFmpeg, "-audio-language", selectedAudio, "-controlled")
	worker.Dir = b.base
	input, err := worker.StdinPipe()
	if err != nil {
		return err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	worker.Stdout = writer
	worker.Stderr = writer
	if err = worker.Start(); err != nil {
		input.Close()
		return err
	}
	writer.Close()
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			input.Close()
		case <-finished:
		}
	}()
	defer close(finished)
	status("rendering", "Preparando la licencia y el vídeo…", 30)
	lastError := ""
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		var event struct {
			Event       string `json:"event"`
			Done, Total int
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			if len(line) < 2000 {
				lastError = line
			}
			continue
		}
		switch event.Event {
		case "licenseResult":
			status("rendering", "Exportando vídeo H.264 y audio AAC…", 32)
		case "decodeProgress":
			if event.Total > 0 {
				status("rendering", fmt.Sprintf("Exportando vídeo · %d / %d fotogramas", event.Done, event.Total), 32+event.Done*58/event.Total)
			}
		}
		if event.Event == "videoExported" {
			status("verifying", "Comprobando el archivo completo…", 92)
		}
	}
	waitError := worker.Wait()
	input.Close()
	if scanner.Err() != nil {
		return scanner.Err()
	}
	if waitError != nil {
		if lastError == "" {
			lastError = "el motor no pudo completar el vídeo; revisa la sesión y el CDM"
		}
		return errors.New(lastError)
	}
	return b.finalizeVariant(ctx, j, settings, destination, video.Height, selectedAudio, netflixlib.Select(netflixlib.Tracks(playback.Value), j.SubtitleMode, j.SubtitleLanguages), status)
}
func (b *backend) shutdown() {
	b.cancel()
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

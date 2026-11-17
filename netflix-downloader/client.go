package netflix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const manifestsEndpoint = "https://www.netflix.com/msl/playapi/cadmium/licensedmanifest/1"
const licensesEndpoint = "https://www.netflix.com/nq/msl_v1/cadmium/pbo_licenses/%5E1.0.0/router"
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

type Client struct {
	http                                 *http.Client
	cookies                              map[string]string
	esn, uiVersion, clientVersion, build string
	transport                            *msl
	playerSources                        []string
	osName, osVersion, userAgent         string
}

type Playback struct {
	Value                      any
	PSSH                       []byte
	Certificate                []byte
	LicenseURL, SessionID, XID string
	Heights                    []int
	request                    document
}

func New(ctx context.Context, cookies map[string]string) (*Client, error) {
	return NewForPlatform(ctx, cookies, "windows")
}

func NewForPlatform(ctx context.Context, cookies map[string]string, platform string) (*Client, error) {
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://www.netflix.com")
	for _, name := range []string{"NetflixId", "SecureNetflixId"} {
		if cookies[name] == "" {
			return nil, errors.New("faltan cookies de Netflix")
		}
		jar.SetCookies(origin, []*http.Cookie{{Name: name, Value: cookies[name], Domain: ".netflix.com", Path: "/", Secure: true, HttpOnly: true}})
	}
	client := &Client{http: &http.Client{Jar: jar, Timeout: 40 * time.Second}, cookies: cookies, osName: "windows", osVersion: "10.0", userAgent: browserUA}
	if platform == "linux" {
		client.osName = "linux"
		client.osVersion = ""
		client.userAgent = strings.Replace(browserUA, "Windows NT 10.0; Win64; x64", "X11; Linux x86_64", 1)
	} else if platform != "windows" {
		return nil, errors.New("plataforma CDM no válida")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.netflix.com/browse", nil)
	client.headers(request)
	response, err := client.http.Do(request)
	if err != nil {
		return nil, errors.New("no se pudo abrir Netflix")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || strings.Contains(response.Request.URL.Path, "/login") {
		return nil, errors.New("Netflix solicita iniciar sesión")
	}
	page, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	html := string(page)
	for _, asset := range regexp.MustCompile(`https://assets\.nflxext\.com[^"'<>\\\s]+\.js`).FindAllString(html, -1) {
		if strings.Contains(asset, "player") || strings.Contains(asset, "cadmium") {
			client.playerSources = append(client.playerSources, asset)
		}
	}
	field := func(name string) string {
		match := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*"([^"\\]+)"`).FindStringSubmatch(html)
		if len(match) == 2 {
			return match[1]
		}
		return ""
	}
	client.uiVersion = field("uiVersion")
	client.clientVersion = field("clientVersion")
	client.build = field("BUILD_IDENTIFIER")
	if client.build == "" {
		client.build = field("buildIdentifier")
	}
	if client.uiVersion == "" {
		client.uiVersion = "shakti-v25d2fa21"
	}
	if client.clientVersion == "" {
		client.clientVersion = "6.0041.930.911"
	}
	if client.build == "" {
		client.build = strings.TrimPrefix(client.uiVersion, "shakti-")
	}
	client.esn = field("esn")
	if client.esn == "" {
		var identity [16]byte
		if _, err := rand.Read(identity[:]); err != nil {
			return nil, err
		}
		client.esn = "NFCDCH-02-" + strings.ToUpper(hex.EncodeToString(identity[:]))
	}
	client.transport = &msl{client: client}
	return client, nil
}

func (c *Client) headers(request *http.Request) {
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Origin", "https://www.netflix.com")
	request.Header.Set("Referer", "https://www.netflix.com/browse")
	request.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
}

func (c *Client) PlayerSources() []string { return append([]string(nil), c.playerSources...) }

func Find(value any, key string) any {
	if fields := mslObject(value); fields != nil {
		if result, ok := fields[key]; ok {
			return result
		}
		for _, child := range fields {
			if result := Find(child, key); result != nil {
				return result
			}
		}
	} else if list, ok := value.([]any); ok {
		for _, child := range list {
			if result := Find(child, key); result != nil {
				return result
			}
		}
	}
	return nil
}

func (c *Client) Manifest(ctx context.Context, id int) (*Playback, error) {
	if id <= 0 {
		return nil, errors.New("ID de reproducción no válido")
	}
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return nil, err
	}
	playback := &Playback{SessionID: hex.EncodeToString(session[:]), XID: strconv.FormatInt(time.Now().UnixNano(), 10)[:18]}
	profiles := []string{"playready-h264bpl30-dash", "playready-h264mpl30-dash", "playready-h264mpl31-dash", "playready-h264mpl40-dash", "playready-h264hpl30-dash", "playready-h264hpl31-dash", "playready-h264hpl40-dash", "heaac-2-dash", "aac-2-dash", "ddplus-2.0-dash", "webvtt-lssdh", "dfxp-ls-sdh", "nflx-cmisc", "BIF240", "BIF320"}
	params := document{"type": "standard", "manifestVersion": "v2", "viewableId": id, "profiles": profiles, "flavor": "PRE_FETCH", "drmType": "widevine", "drmVersion": 25, "usePsshBox": true, "isBranching": false, "useHttpsStreams": true, "imageSubtitleHeight": 1080, "uiVersion": c.uiVersion, "uiPlatform": "SHAKTI", "clientVersion": c.clientVersion, "platform": "156.0.8078.73", "osName": c.osName, "osVersion": c.osVersion, "supportsPreReleasePin": true, "supportsWatermark": true, "supportsUnequalizedDownloadables": true, "showAllSubDubTracks": true, "titleSpecificData": document{strconv.Itoa(id): document{"unletterboxed": true}}, "videoOutputInfo": []document{{"type": "DigitalVideoOutputDescriptor", "outputType": "unknown", "supportedHdcpVersions": []string{}, "isHdcpEngaged": false}}, "preferAssistiveAudio": false, "isNonMember": false, "isUIAutoPlay": false, "desiredVmaf": "plus_lts", "desiredSegmentVmaf": "plus_lts", "requestSegmentVmaf": false, "supportsPartialHydration": false, "contentPlaygraph": []string{"start"}, "supportsAdBreakHydration": true, "liveMetadataFormat": "INDEXED_SEGMENT_TEMPLATE", "useBetterTextUrls": true, "xid": playback.XID, "challenge": "", "challenges": document{"default": []document{{"drmSessionId": playback.SessionID, "clientTime": time.Now().Unix(), "challengeBase64": ""}}}, "licenseType": "standard", "profileGroups": []document{{"name": "default", "profiles": profiles}}}
	playback.request = document{"version": 2, "url": "licensedManifest", "id": time.Now().UnixMilli(), "esn": c.esn, "languages": []string{"es-ES"}, "params": params}
	value, err := c.transport.request(ctx, manifestsEndpoint, playback.request)
	if err != nil {
		return nil, err
	}
	playback.Value = value
	links := mslObject(mslObject(value)["links"])
	playback.LicenseURL = text(mslObject(links["license"])["href"])
	for _, key := range []string{"psshb64", "pssh"} {
		if encoded := text(Find(value, key)); encoded != "" {
			playback.PSSH, _ = unb64(encoded)
			if len(playback.PSSH) > 0 {
				break
			}
		}
	}
	if len(playback.PSSH) == 0 {
		if header := mslObject(Find(value, "drmHeader")); header != nil {
			playback.PSSH, _ = unb64(text(header["bytes"]))
		}
	}
	for _, key := range []string{"cert", "serverCertificate"} {
		if encoded := text(Find(value, key)); encoded != "" {
			playback.Certificate, _ = unb64(encoded)
			if len(playback.Certificate) > 0 {
				break
			}
		}
	}
	var walk func(any)
	walk = func(value any) {
		if fields := mslObject(value); fields != nil {
			if height, ok := fields["res_h"].(float64); ok {
				playback.Heights = append(playback.Heights, int(height))
			}
			for _, child := range fields {
				walk(child)
			}
		} else if list, ok := value.([]any); ok {
			for _, child := range list {
				walk(child)
			}
		}
	}
	walk(value)
	if len(playback.PSSH) == 0 {
		return nil, errors.New("el manifiesto no contiene un PSSH Widevine utilizable")
	}
	if playback.LicenseURL == "" {
		return nil, errors.New("el manifiesto no ofrece un enlace de licencia")
	}
	return playback, nil
}

func (c *Client) License(ctx context.Context, playback *Playback, challenge []byte, session string) ([]byte, error) {
	if session == "" {
		session = playback.SessionID
	}
	value, err := c.transport.request(ctx, licensesEndpoint, document{"version": 2, "url": playback.LicenseURL, "id": time.Now().UnixMilli(), "esn": c.esn, "languages": []string{"es-ES"}, "uiVersion": c.uiVersion, "clientVersion": c.clientVersion, "params": []document{{"drmSessionId": session, "clientTime": time.Now().Unix(), "challengeBase64": b64(challenge), "xid": playback.XID, "clientVersion": c.clientVersion, "platform": "156.0.0.0", "osVersion": c.osVersion, "osName": c.osName}}, "echo": "drmSessionId"})
	if err != nil {
		return nil, err
	}
	if detail := Find(value, "error"); detail != nil {
		return nil, serviceFailure(detail)
	}
	response := text(Find(value, "licenseResponseBase64"))
	if response == "" {
		return nil, errors.New("Netflix no devolvió datos de licencia")
	}
	return unb64(response)
}

// BindChallenge completes the licensed-manifest step with the real CDM session.
// Linux carries xid in the challenge context rather than the outer parameters.
func (c *Client) BindChallenge(ctx context.Context, p *Playback, challenge []byte, session string) ([]byte, error) {
	if session == "" || len(challenge) == 0 || p.request == nil {
		return nil, errors.New("faltan datos de la sesión CDM")
	}
	var request document
	if err := json.Unmarshal(encode(p.request), &request); err != nil {
		return nil, err
	}
	request["id"] = time.Now().UnixMilli()
	params := mslObject(request["params"])
	params["challenge"] = b64(challenge)
	params["drmSessionId"] = session
	params["licenseType"] = "standard"
	context := document{"drmSessionId": session, "clientTime": time.Now().Unix(), "challengeBase64": b64(challenge)}
	if c.osName == "linux" {
		context["xid"] = p.XID
		delete(params, "xid")
	} else {
		params["xid"] = p.XID
	}
	params["challenges"] = document{"default": []document{context}}
	value, err := c.transport.request(ctx, manifestsEndpoint, request)
	if err != nil {
		return nil, err
	}
	if detail := Find(value, "error"); detail != nil {
		return nil, serviceFailure(detail)
	}
	if response := text(Find(value, "licenseResponseBase64")); response != "" {
		return unb64(response)
	}
	if href := text(mslObject(mslObject(mslObject(value)["links"])["license"])["href"]); href != "" {
		p.LicenseURL = href
	}
	p.SessionID = session
	return nil, nil
}

func (c *Client) Certificate(ctx context.Context, playback *Playback) ([]byte, error) {
	if len(playback.Certificate) > 0 {
		return playback.Certificate, nil
	}
	for _, source := range c.playerSources {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			continue
		}
		response, err := c.http.Do(request)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			continue
		}
		selector := regexp.MustCompile(`getCertificate\(\)\{return[^{}]{0,300}base64\.decode\(([$A-Za-z0-9_]+)\)\}`).FindSubmatch(data)
		if len(selector) != 2 {
			continue
		}
		pattern := `(?:^|[,;])` + regexp.QuoteMeta(string(selector[1])) + `\s*=\s*["'` + "`" + `]([A-Za-z0-9+/]{500,}={0,2})["'` + "`" + `]`
		certificatePattern, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		encoded := certificatePattern.FindSubmatch(data)
		if len(encoded) == 2 && len(encoded[1]) <= 8192 {
			certificate, err := unb64(string(encoded[1]))
			if err == nil {
				return certificate, nil
			}
		}
	}
	return nil, errors.New("no se pudo obtener el certificado público del reproductor actual de Netflix")
}

func (c *Client) Redact(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, value := range c.cookies {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[sesión omitida]")
		}
	}
	if len(message) > 700 {
		message = message[:700]
	}
	return errors.New(message)
}

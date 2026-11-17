package netflix

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type NetflixTrack struct {
	URL, Language, Profile string
	Height, Bitrate        int
}

func (p *Playback) AudioLanguages() []string {
	seen := map[string]bool{}
	var result []string
	for _, group := range array(mslObject(p.Value)["audio_tracks"]) {
		language := text(mslObject(group)["language"])
		if _, err := p.SelectAudio(language); err == nil && !seen[language] {
			seen[language] = true
			result = append(result, language)
		}
	}
	sort.Strings(result)
	return result
}

// MP4 stores ISO 639-2 language codes, while Netflix exposes BCP 47 tags.
func AudioLanguageTag(language string) string {
	base := strings.ToLower(strings.Split(language, "-")[0])
	code := map[string]string{"en": "eng", "es": "spa", "fr": "fra", "de": "deu", "it": "ita", "pt": "por", "ja": "jpn", "ko": "kor", "zh": "zho", "ar": "ara", "hi": "hin", "tr": "tur", "ru": "rus", "pl": "pol", "nl": "nld", "sv": "swe", "da": "dan", "no": "nor", "fi": "fin", "cs": "ces", "hu": "hun", "ro": "ron", "th": "tha", "ta": "tam", "te": "tel", "id": "ind", "vi": "vie"}[base]
	if code == "" {
		return "und"
	}
	return code
}

func number(value any) int  { result, _ := value.(float64); return int(result) }
func array(value any) []any { result, _ := value.([]any); return result }

func trackFrom(value any, language string) NetflixTrack {
	fields := mslObject(value)
	track := NetflixTrack{Language: language, Profile: text(fields["content_profile"]), Height: number(fields["res_h"]), Bitrate: number(fields["bitrate"])}
	for _, candidate := range array(fields["urls"]) {
		if address := text(mslObject(candidate)["url"]); address != "" {
			track.URL = address
			break
		}
	}
	return track
}

func (p *Playback) SelectVideo(maxHeight int) (NetflixTrack, error) {
	return p.SelectVideoProfile(maxHeight, "")
}

func (p *Playback) SelectVideoProfile(maxHeight int, profile string) (NetflixTrack, error) {
	var tracks []NetflixTrack
	for _, group := range array(mslObject(p.Value)["video_tracks"]) {
		for _, value := range array(mslObject(group)["streams"]) {
			track := trackFrom(value, "")
			if track.Height > 0 && track.Height <= maxHeight && track.URL != "" && (profile == "" || track.Profile == profile) {
				tracks = append(tracks, track)
			}
		}
	}
	if len(tracks) == 0 {
		return NetflixTrack{}, errors.New("no hay una pista de vídeo compatible con la altura elegida")
	}
	sort.Slice(tracks, func(i, j int) bool {
		if tracks[i].Height != tracks[j].Height {
			return tracks[i].Height > tracks[j].Height
		}
		return tracks[i].Bitrate > tracks[j].Bitrate
	})
	return tracks[0], nil
}

// DownloadEncryptedProbe fetches at most 32 MiB, ending after the first complete
// encrypted fragment. It permits license preflight without downloading a title.
func (c *Client) DownloadEncryptedProbe(ctx context.Context, track NetflixTrack, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, track.URL, nil)
	if err != nil {
		return errors.New("URL de pista no válida")
	}
	c.headers(request)
	request.Header.Set("Range", "bytes=0-33554431")
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("no se pudo leer el fragmento de comprobación")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 206 {
		return fmt.Errorf("fragmento de comprobación: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return errors.New("se interrumpió el fragmento de comprobación")
	}
	protected := false
	end := 0
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset:]))
		header := 8
		if size == 1 {
			if offset+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[offset+8:])
			header = 16
		}
		if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < uint64(header) || size > uint64(len(data)-offset) {
			break
		}
		kind := string(data[offset+4 : offset+8])
		boxEnd := offset + int(size)
		if kind == "moof" {
			protected = bytes.Contains(data[offset+header:boxEnd], []byte("senc"))
		}
		if kind == "mdat" && protected {
			end = boxEnd
			break
		}
		offset = boxEnd
	}
	if end == 0 {
		return errors.New("el prefijo no contiene un fragmento cifrado completo")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(data[:end]); err != nil {
		return err
	}
	return file.Sync()
}

func (p *Playback) SelectAudio(language string) (NetflixTrack, error) {
	var tracks []NetflixTrack
	for _, group := range array(mslObject(p.Value)["audio_tracks"]) {
		fields := mslObject(group)
		if text(fields["language"]) != language {
			continue
		}
		for _, value := range array(fields["streams"]) {
			track := trackFrom(value, language)
			if track.URL != "" && strings.Contains(track.Profile, "aac") {
				tracks = append(tracks, track)
			}
		}
	}
	if len(tracks) == 0 {
		return NetflixTrack{}, errors.New("no hay una pista AAC del idioma elegido")
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Bitrate > tracks[j].Bitrate })
	return tracks[0], nil
}

func (c *Client) DownloadTrack(ctx context.Context, track NetflixTrack, destination string, progress func(int64)) error {
	address, err := url.Parse(track.URL)
	if err != nil || address.Scheme != "https" {
		return errors.New("URL de pista no válida")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return errors.New("no se pudo preparar la descarga de la pista")
	}
	c.headers(request)
	transferClient := &http.Client{Jar: c.http.Jar, Transport: c.http.Transport}
	response, err := transferClient.Do(request)
	if err != nil {
		return errors.New("falló la conexión con el servidor de vídeo")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("el servidor de vídeo devolvió HTTP %d", response.StatusCode)
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("el archivo de destino ya existe")
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".transfer-*")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	buffer := make([]byte, 256*1024)
	var total int64
	for {
		n, readError := response.Body.Read(buffer)
		if n > 0 {
			if _, err = file.Write(buffer[:n]); err != nil {
				return err
			}
			total += int64(n)
			if progress != nil {
				progress(total)
			}
		}
		if readError == io.EOF {
			break
		}
		if readError != nil {
			return errors.New("la transferencia de la pista se interrumpió")
		}
	}
	if total == 0 || response.ContentLength > 0 && total != response.ContentLength {
		return errors.New("la pista no se transfirió completa")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

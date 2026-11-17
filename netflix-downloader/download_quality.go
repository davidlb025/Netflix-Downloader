package netflix

import (
	"errors"
	"sort"
	"strings"
)

// The installed export path has been validated through 960x540 H.264.
// A representation advertised by the service is not proof that this engine can
// export it. Raising this ceiling requires a successful export validation.
const ValidatedExportHeight = 540

func downloadableVideo(t NetflixTrack) bool {
	return t.Height > 0 && t.Height <= ValidatedExportHeight && t.URL != "" && strings.HasPrefix(t.Profile, "playready-h264")
}

func (p *Playback) DownloadHeights() []int {
	seen := map[int]bool{}
	result := []int{}
	for _, group := range array(mslObject(p.Value)["video_tracks"]) {
		for _, value := range array(mslObject(group)["streams"]) {
			t := trackFrom(value, "")
			if downloadableVideo(t) && !seen[t.Height] {
				seen[t.Height] = true
				result = append(result, t.Height)
			}
		}
	}
	sort.Ints(result)
	return result
}

// DownloadVideo selects the exact displayed height; there is no silent
// substitution by another resolution or upscaling.
func (p *Playback) DownloadVideo(height int) (NetflixTrack, error) {
	var best NetflixTrack
	for _, group := range array(mslObject(p.Value)["video_tracks"]) {
		for _, value := range array(mslObject(group)["streams"]) {
			t := trackFrom(value, "")
			if t.Height == height && downloadableVideo(t) && (best.URL == "" || t.Bitrate > best.Bitrate) {
				best = t
			}
		}
	}
	if best.URL == "" {
		return NetflixTrack{}, errors.New("la calidad elegida no tiene una pista compatible con el motor actual; vuelve a analizar el enlace")
	}
	return best, nil
}

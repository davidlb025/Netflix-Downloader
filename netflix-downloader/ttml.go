package netflix

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// TTMLToVTT exports timed text, not presentation. The original TTML is retained.
// Unsupported time containers fail rather than silently shifting subtitles.
func TTMLToVTT(data []byte) ([]byte, error) {
	if len(data) > 12<<20 {
		return nil, errors.New("TTML demasiado grande")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	tick, frame := 1.0, 30.0
	var out strings.Builder
	out.WriteString("WEBVTT\n\n")
	depth, pDepth, count := 0, -1, 0
	var text strings.Builder
	var begin, end float64
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 64 {
				return nil, errors.New("TTML demasiado profundo")
			}
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			if t.Name.Local == "tt" {
				if v := attrs["tickRate"]; v != "" {
					tick, err = strconv.ParseFloat(v, 64)
					if err != nil || tick <= 0 || math.IsInf(tick, 0) || math.IsNaN(tick) {
						return nil, errors.New("tickRate inválido")
					}
				}
				if v := attrs["frameRate"]; v != "" {
					frame, err = strconv.ParseFloat(v, 64)
					if err != nil || frame <= 0 {
						return nil, errors.New("frameRate inválido")
					}
				}
				if v := attrs["frameRateMultiplier"]; v != "" {
					bits := strings.Fields(v)
					if len(bits) != 2 {
						return nil, errors.New("frameRateMultiplier inválido")
					}
					a, e1 := strconv.ParseFloat(bits[0], 64)
					b, e2 := strconv.ParseFloat(bits[1], 64)
					if e1 != nil || e2 != nil || a <= 0 || b <= 0 {
						return nil, errors.New("frameRateMultiplier inválido")
					}
					frame *= a / b
				}
				if v := attrs["timeBase"]; v != "" && v != "media" {
					return nil, errors.New("base de tiempo TTML no admitida")
				}
			}
			if attrs["timeContainer"] == "seq" {
				return nil, errors.New("contenedor secuencial TTML no admitido")
			}
			if t.Name.Local != "p" && (attrs["begin"] != "" || attrs["end"] != "" || attrs["dur"] != "") {
				return nil, errors.New("tiempos TTML heredados no admitidos")
			}
			if t.Name.Local == "p" {
				if pDepth >= 0 {
					return nil, errors.New("párrafos TTML anidados")
				}
				begin, err = ttmlTime(attrs["begin"], tick, frame)
				if err != nil {
					return nil, err
				}
				if v := attrs["end"]; v != "" {
					end, err = ttmlTime(v, tick, frame)
				} else {
					var duration float64
					duration, err = ttmlTime(attrs["dur"], tick, frame)
					end = begin + duration
				}
				if err != nil || end <= begin || end > 604800 {
					return nil, errors.New("intervalo TTML inválido")
				}
				pDepth = depth
				text.Reset()
			} else if t.Name.Local == "br" && pDepth >= 0 {
				text.WriteByte('\n')
			}
		case xml.CharData:
			if pDepth >= 0 {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == pDepth {
				lines := strings.Split(text.String(), "\n")
				for i, line := range lines {
					lines[i] = strings.Join(strings.Fields(line), " ")
				}
				content := strings.TrimSpace(strings.Join(lines, "\n"))
				if content != "" {
					count++
					fmt.Fprintf(&out, "%d\n%s --> %s\n%s\n\n", count, vttTime(begin), vttTime(end), html.EscapeString(content))
				}
				pDepth = -1
			}
			depth--
		}
	}
	if count == 0 || depth != 0 || pDepth >= 0 {
		return nil, errors.New("TTML sin subtítulos temporizados completos")
	}
	return []byte(out.String()), nil
}

var offsetTime = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(h|m|s|ms|f|t)$`)

func ttmlTime(raw string, tick, frame float64) (float64, error) {
	if m := offsetTime.FindStringSubmatch(raw); len(m) == 3 {
		v, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "h":
			v *= 3600
		case "m":
			v *= 60
		case "ms":
			v /= 1000
		case "f":
			v /= frame
		case "t":
			v /= tick
		}
		if !math.IsInf(v, 0) && !math.IsNaN(v) && v >= 0 && v <= 604800 {
			return v, nil
		}
	}
	bits := strings.Split(raw, ":")
	if len(bits) == 3 || len(bits) == 4 {
		h, e1 := strconv.Atoi(bits[0])
		m, e2 := strconv.Atoi(bits[1])
		s, e3 := strconv.ParseFloat(bits[2], 64)
		if e1 == nil && e2 == nil && e3 == nil && h >= 0 && m >= 0 && m < 60 && s >= 0 && s < 60 {
			v := float64(h*3600+m*60) + s
			if len(bits) == 4 {
				f, e := strconv.ParseFloat(bits[3], 64)
				if e != nil || f < 0 || f >= frame {
					return 0, errors.New("fotograma TTML inválido")
				}
				v += f / frame
			}
			if v <= 604800 {
				return v, nil
			}
		}
	}
	return 0, errors.New("tiempo TTML no admitido: " + raw)
}
func vttTime(seconds float64) string {
	ms := int64(math.Round(seconds * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

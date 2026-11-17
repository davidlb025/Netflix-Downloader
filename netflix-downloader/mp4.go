package netflix

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

type Subsample struct{ Clear, Cipher uint32 }
type Sample struct {
	Offset              int64
	Size                uint32
	KID                 [16]byte
	IV                  []byte
	Subsamples          []Subsample
	Scheme, Crypt, Skip uint32
	Timestamp           int64
}
type Patch struct {
	Offset int64
	Value  [4]byte
}
type Plan struct {
	Path         string
	Size         int64
	Samples      []Sample
	Patches      []Patch
	Codec        *VideoCodec
	VideoSamples []Sample
}

type VideoCodec struct {
	Width, Height, Profile uint32
	Extra                  []byte
	RateNum, RateDen       uint32
}
type box struct {
	offset, size, header int64
	kind                 string
}
type encryption struct {
	protected           bool
	ivSize              int
	kid                 [16]byte
	constantIV          []byte
	scheme, crypt, skip uint32
}
type track struct {
	descriptions                    map[uint32]encryption
	defaultDescription, defaultSize uint32
	timescale, defaultDuration      uint32
	video                           bool
}
type parser struct {
	file   *os.File
	plan   *Plan
	tracks map[uint32]*track
}

func boxes(reader io.ReaderAt, start, end int64) ([]box, error) {
	var result []box
	for offset := start; offset < end; {
		var header [16]byte
		if end-offset < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		if _, err := reader.ReadAt(header[:8], offset); err != nil {
			return nil, err
		}
		size := uint64(binary.BigEndian.Uint32(header[:4]))
		headerSize := int64(8)
		if size == 1 {
			if end-offset < 16 {
				return nil, io.ErrUnexpectedEOF
			}
			if _, err := reader.ReadAt(header[8:], offset+8); err != nil {
				return nil, err
			}
			size = binary.BigEndian.Uint64(header[8:])
			headerSize = 16
		}
		if size == 0 {
			size = uint64(end - offset)
		}
		if size < uint64(headerSize) || size > uint64(end-offset) {
			return nil, errors.New("tamaño de caja MP4 fuera del archivo")
		}
		result = append(result, box{offset: offset, size: int64(size), header: headerSize, kind: string(header[4:8])})
		offset += int64(size)
		if len(result) > 1000000 {
			return nil, errors.New("demasiadas cajas MP4")
		}
	}
	return result, nil
}
func (p *parser) children(parent box, skip int64) ([]box, error) {
	return boxes(p.file, parent.offset+parent.header+skip, parent.offset+parent.size)
}
func (p *parser) data(b box) ([]byte, error) {
	if b.size-b.header > 64<<20 {
		return nil, errors.New("metadatos MP4 demasiado grandes")
	}
	data := make([]byte, b.size-b.header)
	_, err := p.file.ReadAt(data, b.offset+b.header)
	return data, err
}
func (p *parser) patch(b box, value string) {
	var v [4]byte
	copy(v[:], value)
	p.plan.Patches = append(p.plan.Patches, Patch{b.offset + 4, v})
}
func flags(data []byte) uint32 { return uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]) }

func CENCParse(path string) (*Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	p := &parser{file: file, plan: &Plan{Path: path, Size: info.Size()}, tracks: map[uint32]*track{}}
	top, err := boxes(file, 0, info.Size())
	if err != nil {
		return nil, err
	}
	for _, b := range top {
		if b.kind == "moov" {
			if err = p.moov(b); err != nil {
				return nil, err
			}
		}
	}
	if len(p.tracks) == 0 {
		return nil, errors.New("MP4 sin pistas interpretables")
	}
	fragments := 0
	for _, b := range top {
		if b.kind == "moof" {
			fragments++
			if err = p.moof(b); err != nil {
				return nil, fmt.Errorf("fragmento %d: %w", fragments, err)
			}
		} else if b.kind == "pssh" {
			p.patch(b, "free")
		}
	}
	if fragments == 0 {
		return nil, errors.New("esta versión requiere pistas DASH MP4 fragmentadas")
	}
	return p.plan, nil
}

func (p *parser) moov(parent box) error {
	children, err := p.children(parent, 0)
	if err != nil {
		return err
	}
	for _, b := range children {
		switch b.kind {
		case "trak":
			t := &track{descriptions: map[uint32]encryption{}, defaultDescription: 1}
			var id uint32
			var walk func(box) error
			walk = func(parent box) error {
				children, err := p.children(parent, 0)
				if err != nil {
					return err
				}
				for _, child := range children {
					switch child.kind {
					case "tkhd":
						data, err := p.data(child)
						if err != nil {
							return err
						}
						pos := 12
						if len(data) > 0 && data[0] == 1 {
							pos = 20
						}
						if len(data) < pos+4 {
							return io.ErrUnexpectedEOF
						}
						id = binary.BigEndian.Uint32(data[pos:])
					case "mdhd":
						data, err := p.data(child)
						if err != nil {
							return err
						}
						pos := 12
						if len(data) > 0 && data[0] == 1 {
							pos = 20
						}
						if len(data) < pos+4 {
							return io.ErrUnexpectedEOF
						}
						t.timescale = binary.BigEndian.Uint32(data[pos:])
					case "mdia", "minf", "stbl":
						if err = walk(child); err != nil {
							return err
						}
					case "stsd":
						if err = p.descriptions(child, t); err != nil {
							return err
						}
					}
				}
				return nil
			}
			if err = walk(b); err != nil {
				return err
			}
			if id == 0 {
				return errors.New("pista MP4 sin identificador")
			}
			p.tracks[id] = t
		case "pssh":
			p.patch(b, "free")
		}
	}
	for _, b := range children {
		if b.kind != "mvex" {
			continue
		}
		items, err := p.children(b, 0)
		if err != nil {
			return err
		}
		for _, child := range items {
			if child.kind != "trex" {
				continue
			}
			data, err := p.data(child)
			if err != nil {
				return err
			}
			if len(data) < 24 {
				return io.ErrUnexpectedEOF
			}
			if t := p.tracks[binary.BigEndian.Uint32(data[4:])]; t != nil {
				t.defaultDescription = binary.BigEndian.Uint32(data[8:])
				t.defaultSize = binary.BigEndian.Uint32(data[16:])
				t.defaultDuration = binary.BigEndian.Uint32(data[12:])
			}
		}
	}
	return nil
}

func (p *parser) descriptions(parent box, t *track) error {
	data, err := p.data(parent)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return io.ErrUnexpectedEOF
	}
	entries, err := p.children(parent, 8)
	if err != nil {
		return err
	}
	if len(entries) != int(binary.BigEndian.Uint32(data[4:])) {
		return errors.New("contador stsd no válido")
	}
	for i, entry := range entries {
		if entry.kind == "encv" || entry.kind == "avc1" || entry.kind == "avc3" {
			t.video = true
		}
		enc := encryption{scheme: 1}
		if entry.kind != "encv" && entry.kind != "enca" {
			t.descriptions[uint32(i+1)] = enc
			continue
		}
		start := int64(86)
		if entry.kind == "enca" {
			start = 36
		}
		children, err := boxes(p.file, entry.offset+start, entry.offset+entry.size)
		if err != nil {
			return err
		}
		found := false
		if entry.kind == "encv" {
			var dimensions [4]byte
			if _, err = p.file.ReadAt(dimensions[:], entry.offset+32); err != nil {
				return err
			}
			for _, child := range children {
				if child.kind == "avcC" {
					extra, err := p.data(child)
					if err != nil {
						return err
					}
					if len(extra) < 4 {
						return io.ErrUnexpectedEOF
					}
					profile := uint32(5)
					switch extra[1] {
					case 66:
						profile = 2
					case 77:
						profile = 3
					case 88:
						profile = 4
					}
					p.plan.Codec = &VideoCodec{Width: uint32(binary.BigEndian.Uint16(dimensions[:2])), Height: uint32(binary.BigEndian.Uint16(dimensions[2:])), Profile: profile, Extra: extra}
					break
				}
			}
		}
		for _, child := range children {
			if child.kind != "sinf" {
				continue
			}
			p.patch(child, "free")
			items, err := p.children(child, 0)
			if err != nil {
				return err
			}
			for _, item := range items {
				data, err := p.data(item)
				if err != nil {
					return err
				}
				switch item.kind {
				case "frma":
					if len(data) < 4 {
						return io.ErrUnexpectedEOF
					}
					p.patch(entry, string(data[:4]))
				case "schm":
					if len(data) < 8 {
						return io.ErrUnexpectedEOF
					}
					switch string(data[4:8]) {
					case "cenc":
						enc.scheme = 1
					case "cbcs":
						enc.scheme = 2
					default:
						return errors.New("esquema de cifrado MP4 no compatible")
					}
				case "schi":
					children, err := p.children(item, 0)
					if err != nil {
						return err
					}
					for _, tenc := range children {
						if tenc.kind != "tenc" {
							continue
						}
						data, err := p.data(tenc)
						if err != nil {
							return err
						}
						if len(data) < 24 {
							return io.ErrUnexpectedEOF
						}
						found = true
						enc.protected = data[6] != 0
						enc.ivSize = int(data[7])
						copy(enc.kid[:], data[8:24])
						if data[0] > 0 {
							enc.crypt = uint32(data[5] >> 4)
							enc.skip = uint32(data[5] & 15)
						}
						if enc.ivSize == 0 && enc.protected {
							if len(data) < 25 || len(data) < 25+int(data[24]) {
								return io.ErrUnexpectedEOF
							}
							enc.constantIV = append([]byte(nil), data[25:25+int(data[24])]...)
						}
						if enc.ivSize != 0 && enc.ivSize != 8 && enc.ivSize != 16 {
							return errors.New("tamaño IV CENC no compatible")
						}
					}
				}
			}
		}
		if !found {
			return errors.New("entrada cifrada sin tenc")
		}
		t.descriptions[uint32(i+1)] = enc
	}
	return nil
}

func (p *parser) moof(parent box) error {
	children, err := p.children(parent, 0)
	if err != nil {
		return err
	}
	for _, traf := range children {
		if traf.kind == "traf" {
			if err = p.traf(parent, traf); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *parser) traf(moof, parent box) error {
	children, err := p.children(parent, 0)
	if err != nil {
		return err
	}
	base := moof.offset
	var defaults *track
	var desc, defaultSize, defaultDuration uint32
	var decodeTime int64
	for _, b := range children {
		if b.kind != "tfhd" {
			continue
		}
		data, err := p.data(b)
		if err != nil {
			return err
		}
		if len(data) < 8 {
			return io.ErrUnexpectedEOF
		}
		defaults = p.tracks[binary.BigEndian.Uint32(data[4:])]
		if defaults == nil {
			return errors.New("tfhd referencia una pista desconocida")
		}
		f := flags(data)
		pos := 8
		desc = defaults.defaultDescription
		defaultSize = defaults.defaultSize
		defaultDuration = defaults.defaultDuration
		if f&1 != 0 {
			if len(data) < pos+8 {
				return io.ErrUnexpectedEOF
			}
			base = int64(binary.BigEndian.Uint64(data[pos:]))
			pos += 8
		}
		if f&2 != 0 {
			if len(data) < pos+4 {
				return io.ErrUnexpectedEOF
			}
			desc = binary.BigEndian.Uint32(data[pos:])
			pos += 4
		}
		if f&8 != 0 {
			if len(data) < pos+4 {
				return io.ErrUnexpectedEOF
			}
			defaultDuration = binary.BigEndian.Uint32(data[pos:])
			pos += 4
		}
		if f&16 != 0 {
			if len(data) < pos+4 {
				return io.ErrUnexpectedEOF
			}
			defaultSize = binary.BigEndian.Uint32(data[pos:])
			pos += 4
		}
		if f&32 != 0 {
			pos += 4
		}
		if len(data) < pos {
			return io.ErrUnexpectedEOF
		}
	}
	if defaults == nil {
		return errors.New("fragmento sin tfhd")
	}
	enc, ok := defaults.descriptions[desc]
	if !ok {
		return errors.New("descripción de muestra desconocida")
	}
	if !enc.protected && !defaults.video {
		return nil
	}
	if defaults.timescale == 0 {
		return errors.New("pista sin escala temporal")
	}
	for _, b := range children {
		if b.kind == "tfdt" {
			data, err := p.data(b)
			if err != nil {
				return err
			}
			if len(data) < 8 {
				return io.ErrUnexpectedEOF
			}
			if data[0] == 1 {
				if len(data) < 12 {
					return io.ErrUnexpectedEOF
				}
				decodeTime = int64(binary.BigEndian.Uint64(data[4:]))
			} else {
				decodeTime = int64(binary.BigEndian.Uint32(data[4:]))
			}
		}
	}
	var samples []Sample
	var nextOffset int64
	for _, b := range children {
		if b.kind != "trun" {
			continue
		}
		data, err := p.data(b)
		if err != nil {
			return err
		}
		if len(data) < 8 {
			return io.ErrUnexpectedEOF
		}
		f := flags(data)
		count := int(binary.BigEndian.Uint32(data[4:]))
		if count > 1000000 {
			return errors.New("demasiadas muestras por fragmento")
		}
		pos := 8
		if f&1 != 0 {
			if len(data) < pos+4 {
				return io.ErrUnexpectedEOF
			}
			nextOffset = base + int64(int32(binary.BigEndian.Uint32(data[pos:])))
			pos += 4
		} else if nextOffset == 0 {
			return errors.New("trun sin posición de datos")
		}
		if f&4 != 0 {
			pos += 4
		}
		for i := 0; i < count; i++ {
			size := defaultSize
			duration := defaultDuration
			var composition int64
			if f&0x100 != 0 {
				if len(data) < pos+4 {
					return io.ErrUnexpectedEOF
				}
				duration = binary.BigEndian.Uint32(data[pos:])
				pos += 4
			}
			if f&0x200 != 0 {
				if len(data) < pos+4 {
					return io.ErrUnexpectedEOF
				}
				size = binary.BigEndian.Uint32(data[pos:])
				pos += 4
			}
			if f&0x400 != 0 {
				pos += 4
			}
			if f&0x800 != 0 {
				if len(data) < pos+4 {
					return io.ErrUnexpectedEOF
				}
				value := binary.BigEndian.Uint32(data[pos:])
				if data[0] == 1 {
					composition = int64(int32(value))
				} else {
					composition = int64(value)
				}
				pos += 4
			}
			if len(data) < pos {
				return io.ErrUnexpectedEOF
			}
			if size == 0 || size > 32<<20 || nextOffset < 0 || nextOffset > p.plan.Size-int64(size) {
				return errors.New("muestra fuera del archivo")
			}
			if defaults.video && p.plan.Codec != nil {
				codec := p.plan.Codec
				if duration == 0 {
					return errors.New("vídeo sin duración de muestra")
				}
				if codec.RateNum == 0 {
					codec.RateNum, codec.RateDen = defaults.timescale, duration
				} else if uint64(codec.RateNum)*uint64(duration) != uint64(defaults.timescale)*uint64(codec.RateDen) {
					return errors.New("vídeo con frecuencia variable no compatible con esta exportación")
				}
			}
			samples = append(samples, Sample{Offset: nextOffset, Size: size, KID: enc.kid, Scheme: enc.scheme, Crypt: enc.crypt, Skip: enc.skip, Timestamp: (decodeTime + composition) * 1000000 / int64(defaults.timescale)})
			nextOffset += int64(size)
			decodeTime += int64(duration)
		}
	}
	if !enc.protected {
		for i := range samples {
			samples[i].Scheme = 0
			samples[i].KID = [16]byte{}
		}
		p.plan.VideoSamples = append(p.plan.VideoSamples, samples...)
		return nil
	}
	var senc *box
	for i := range children {
		b := children[i]
		switch b.kind {
		case "senc":
			senc = &children[i]
			p.patch(b, "free")
		case "saiz", "saio":
			p.patch(b, "free")
		case "sgpd", "sbgp":
			data, err := p.data(b)
			if err != nil {
				return err
			}
			if len(data) >= 8 && string(data[4:8]) == "seig" {
				return errors.New("rotación de claves seig no implementada")
			}
		}
	}
	if senc == nil {
		return errors.New("fragmento cifrado sin tabla senc")
	}
	data, err := p.data(*senc)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return io.ErrUnexpectedEOF
	}
	f := flags(data)
	pos := 4
	if f&1 != 0 {
		return errors.New("parámetros senc externos no compatibles")
	}
	count := int(binary.BigEndian.Uint32(data[pos:]))
	pos += 4
	if count != len(samples) {
		return errors.New("senc y trun no coinciden")
	}
	for i := range samples {
		if enc.ivSize > 0 {
			if len(data) < pos+enc.ivSize {
				return io.ErrUnexpectedEOF
			}
			samples[i].IV = append([]byte(nil), data[pos:pos+enc.ivSize]...)
			pos += enc.ivSize
		} else {
			samples[i].IV = append([]byte(nil), enc.constantIV...)
		}
		if len(samples[i].IV) != 8 && len(samples[i].IV) != 16 {
			return errors.New("IV CENC ausente")
		}
		if f&2 != 0 {
			if len(data) < pos+2 {
				return io.ErrUnexpectedEOF
			}
			n := int(binary.BigEndian.Uint16(data[pos:]))
			pos += 2
			var total uint64
			for j := 0; j < n; j++ {
				if len(data) < pos+6 {
					return io.ErrUnexpectedEOF
				}
				sub := Subsample{uint32(binary.BigEndian.Uint16(data[pos:])), binary.BigEndian.Uint32(data[pos+2:])}
				pos += 6
				total += uint64(sub.Clear) + uint64(sub.Cipher)
				samples[i].Subsamples = append(samples[i].Subsamples, sub)
			}
			if n > 0 && total != uint64(samples[i].Size) {
				return errors.New("tamaños de submuestras incoherentes")
			}
		}
	}
	if pos != len(data) {
		return errors.New("datos sobrantes en senc")
	}
	p.plan.Samples = append(p.plan.Samples, samples...)
	if defaults.video {
		p.plan.VideoSamples = append(p.plan.VideoSamples, samples...)
	}
	return nil
}

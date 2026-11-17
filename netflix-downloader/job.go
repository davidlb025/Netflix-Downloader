package netflix

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The job contains sample data and public encryption metadata, never keys.
// Each decrypted sample is produced by the CDM's public Decrypt interface.
type jobRecord struct {
	File                                              uint32
	Offset                                            uint64
	Size, IVSize, SubsampleCount, Scheme, Crypt, Skip uint32
	Timestamp                                         int64
}
type resultRecord struct {
	File   uint32
	Offset uint64
	Size   uint32
}

func sampleCount(plans []*Plan) (uint32, error) {
	var count uint64
	for _, plan := range plans {
		count += uint64(len(plan.Samples))
	}
	if count > 10000000 {
		return 0, errors.New("demasiadas muestras para una operación")
	}
	return uint32(count), nil
}

func WriteJob(destination string, plans []*Plan) error {
	return WriteJobMode(destination, plans, false)
}

func WriteJobMode(destination string, plans []*Plan, decode bool) error {
	count, err := sampleCount(plans)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(destination+".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 1024*1024)
	if _, err = writer.WriteString("SFCENC05"); err != nil {
		return err
	}
	if err = binary.Write(writer, binary.LittleEndian, count); err != nil {
		return err
	}
	mode := uint32(0)
	if decode {
		mode = 1
	}
	if err = binary.Write(writer, binary.LittleEndian, mode); err != nil {
		return err
	}
	var configuration bytes.Buffer
	for _, plan := range plans {
		if plan.Codec != nil {
			codec := plan.Codec
			for _, value := range []uint32{codec.Width, codec.Height, codec.Profile, codec.RateNum, codec.RateDen, uint32(len(codec.Extra))} {
				if err = binary.Write(&configuration, binary.LittleEndian, value); err != nil {
					return err
				}
			}
			configuration.Write(codec.Extra)
			break
		}
	}
	if err = binary.Write(writer, binary.LittleEndian, uint32(configuration.Len())); err != nil {
		return err
	}
	if _, err = writer.Write(configuration.Bytes()); err != nil {
		return err
	}
	for index, plan := range plans {
		input, err := os.Open(plan.Path)
		if err != nil {
			return err
		}
		for _, sample := range plan.Samples {
			record := jobRecord{File: uint32(index), Offset: uint64(sample.Offset), Size: sample.Size, IVSize: uint32(len(sample.IV)), SubsampleCount: uint32(len(sample.Subsamples)), Scheme: sample.Scheme, Crypt: sample.Crypt, Skip: sample.Skip, Timestamp: sample.Timestamp}
			if err = binary.Write(writer, binary.LittleEndian, record); err != nil {
				input.Close()
				return err
			}
			if _, err = writer.Write(sample.KID[:]); err != nil {
				input.Close()
				return err
			}
			if _, err = writer.Write(sample.IV); err != nil {
				input.Close()
				return err
			}
			for _, sub := range sample.Subsamples {
				if err = binary.Write(writer, binary.LittleEndian, sub); err != nil {
					input.Close()
					return err
				}
			}
			data := make([]byte, sample.Size)
			if _, err = input.ReadAt(data, sample.Offset); err != nil {
				input.Close()
				return err
			}
			if _, err = writer.Write(data); err != nil {
				input.Close()
				return err
			}
		}
		input.Close()
	}
	if err = writer.Flush(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(destination+".pending", destination)
}

func ApplyResult(result string, plans []*Plan, outputDir string) ([]string, error) {
	input, err := os.Open(result)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	reader := bufio.NewReaderSize(input, 1024*1024)
	var magic [8]byte
	if _, err = io.ReadFull(reader, magic[:]); err != nil {
		return nil, err
	}
	if string(magic[:]) != "SFCLEAR1" {
		return nil, errors.New("resultado CDM no reconocido")
	}
	var count uint32
	if err = binary.Read(reader, binary.LittleEndian, &count); err != nil {
		return nil, err
	}
	expectedCount, err := sampleCount(plans)
	if err != nil || count != expectedCount {
		return nil, errors.New("número de muestras descifradas incorrecto")
	}
	if err = os.MkdirAll(outputDir, 0700); err != nil {
		return nil, err
	}
	outputs := make([]*os.File, 0, len(plans))
	paths := make([]string, 0, len(plans))
	defer func() {
		for _, output := range outputs {
			output.Close()
		}
	}()
	for index, plan := range plans {
		path := filepath.Join(outputDir, fmt.Sprintf("track-%d.clear.mp4", index))
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
		paths = append(paths, path)
		source, err := os.Open(plan.Path)
		if err != nil {
			return nil, err
		}
		written, err := io.Copy(output, source)
		source.Close()
		if err != nil || written != plan.Size {
			return nil, errors.New("no se pudo preparar la pista de salida")
		}
	}
	for index, plan := range plans {
		for _, sample := range plan.Samples {
			var record resultRecord
			if err = binary.Read(reader, binary.LittleEndian, &record); err != nil {
				return nil, err
			}
			if record.File != uint32(index) || record.Offset != uint64(sample.Offset) || record.Size != sample.Size {
				return nil, errors.New("el resultado CDM no corresponde a las muestras solicitadas")
			}
			data := make([]byte, record.Size)
			if _, err = io.ReadFull(reader, data); err != nil {
				return nil, err
			}
			if _, err = outputs[index].WriteAt(data, sample.Offset); err != nil {
				return nil, err
			}
		}
		for _, patch := range plan.Patches {
			if patch.Offset < 0 || patch.Offset > plan.Size-4 {
				return nil, errors.New("metadatos de salida fuera del archivo")
			}
			if _, err = outputs[index].WriteAt(patch.Value[:], patch.Offset); err != nil {
				return nil, err
			}
		}
		if err = outputs[index].Sync(); err != nil {
			return nil, err
		}
	}
	if _, err = reader.ReadByte(); err != io.EOF {
		return nil, errors.New("datos sobrantes en el resultado CDM")
	}
	return paths, nil
}

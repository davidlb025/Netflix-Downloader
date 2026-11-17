package netflix

import "encoding/binary"

// WidevinePSSH names the actual key IDs of the selected encrypted samples.
// Key IDs are public MP4 metadata; the corresponding keys stay in the CDM.
func WidevinePSSH(plans []*Plan) []byte {
	seen := map[[16]byte]bool{}
	data := []byte{0x08, 0x01}
	for _, plan := range plans {
		for _, sample := range plan.Samples {
			if sample.Scheme == 0 {
				continue
			}
			if seen[sample.KID] {
				continue
			}
			seen[sample.KID] = true
			data = append(data, 0x12, 0x10)
			data = append(data, sample.KID[:]...)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	box := make([]byte, 32+len(data))
	binary.BigEndian.PutUint32(box, uint32(len(box)))
	copy(box[4:8], "pssh")
	copy(box[12:28], []byte{0xed, 0xef, 0x8b, 0xa9, 0x79, 0xd6, 0x4a, 0xce, 0xa3, 0xc8, 0x27, 0xdc, 0xd5, 0x1d, 0x21, 0xed})
	binary.BigEndian.PutUint32(box[28:32], uint32(len(data)))
	copy(box[32:], data)
	return box
}

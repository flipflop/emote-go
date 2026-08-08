package speech

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/flipflop/emote-go/internal/engine"
)

// EncodeWAV serializes audio as a 16-bit PCM mono RIFF/WAVE file.
// Samples are clamped to [-1, 1] before quantization.
func EncodeWAV(a engine.Audio) []byte {
	dataLen := len(a.Samples) * 2
	b := make([]byte, 44+dataLen)
	le := binary.LittleEndian

	copy(b[0:4], "RIFF")
	le.PutUint32(b[4:8], uint32(36+dataLen))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	le.PutUint32(b[16:20], 16)
	le.PutUint16(b[20:22], 1) // PCM
	le.PutUint16(b[22:24], 1) // mono
	le.PutUint32(b[24:28], uint32(a.SampleRate))
	le.PutUint32(b[28:32], uint32(a.SampleRate*2)) // byte rate
	le.PutUint16(b[32:34], 2)                      // block align
	le.PutUint16(b[34:36], 16)                     // bits per sample
	copy(b[36:40], "data")
	le.PutUint32(b[40:44], uint32(dataLen))

	for i, s := range a.Samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		v := int16(s * 32767)
		le.PutUint16(b[44+i*2:46+i*2], uint16(v))
	}
	return b
}

// WriteWAV writes audio to path as 16-bit PCM mono WAV.
func WriteWAV(path string, a engine.Audio) error {
	return os.WriteFile(path, EncodeWAV(a), 0o644)
}

// RepairRIFF fixes the RIFF and data chunk sizes of a wav held in memory.
// Servers that stream wav output write the header before the length is known,
// so the declared sizes are bogus (P1.6 §3); sizes are recomputed from the
// actual byte count. Returns a repaired copy; the input is not modified.
// Wavs whose declared sizes are already correct come back unchanged (but
// still copied).
func RepairRIFF(b []byte) ([]byte, error) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("speech: not a RIFF/WAVE file")
	}
	out := make([]byte, len(b))
	copy(out, b)
	le := binary.LittleEndian
	le.PutUint32(out[4:8], uint32(len(out)-8))

	// Walk chunks; rewrite any whose declared size overruns the file (the
	// data chunk in a streamed wav) to the bytes actually present.
	i := 12
	for i+8 <= len(out) {
		size := int(le.Uint32(out[i+4 : i+8]))
		body := i + 8
		if size < 0 || body+size > len(out) {
			size = len(out) - body
			le.PutUint32(out[i+4:i+8], uint32(size))
		}
		i = body + size
		if size%2 == 1 {
			i++
		}
	}
	return out, nil
}

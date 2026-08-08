package engine

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// readWaveFile decodes a PCM wav file to mono float32 in [-1, 1].
//
// It is deliberately tolerant of the bogus chunk sizes that server-streamed
// wavs carry (the Python server writes RIFF/data lengths before knowing the
// final size — P1.6 §3): any declared size that overruns the file is clamped
// to the bytes actually present, which is the "RIFF header repair" DESIGN.md
// requires before reading such files. Supports 16-bit PCM and 32-bit float,
// mono or multi-channel (channels are averaged).
func readWaveFile(path string) ([]float32, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	samples, rate, err := decodeWave(b)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	return samples, rate, nil
}

func decodeWave(b []byte) ([]float32, int, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	var (
		format      uint16
		channels    int
		rate        int
		bitsPerSamp int
		data        []byte
	)
	i := 12
	for i+8 <= len(b) {
		id := string(b[i : i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		body := i + 8
		// Clamp bogus sizes (server-streamed wavs) to what is really there.
		if size < 0 || body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("fmt chunk too short (%d bytes)", size)
			}
			format = binary.LittleEndian.Uint16(b[body : body+2])
			channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			bitsPerSamp = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
		case "data":
			data = b[body : body+size]
		}
		i = body + size
		if size%2 == 1 { // chunks are word-aligned
			i++
		}
	}
	if channels == 0 || rate == 0 {
		return nil, 0, fmt.Errorf("missing fmt chunk")
	}
	if data == nil {
		return nil, 0, fmt.Errorf("missing data chunk")
	}

	const waveFormatPCM, waveFormatFloat = 1, 3
	var mono []float32
	switch {
	case format == waveFormatPCM && bitsPerSamp == 16:
		n := len(data) / 2 / channels
		mono = make([]float32, n)
		for f := 0; f < n; f++ {
			var sum float32
			for c := 0; c < channels; c++ {
				off := (f*channels + c) * 2
				s := int16(binary.LittleEndian.Uint16(data[off : off+2]))
				sum += float32(s) / 32768
			}
			mono[f] = sum / float32(channels)
		}
	case format == waveFormatFloat && bitsPerSamp == 32:
		n := len(data) / 4 / channels
		mono = make([]float32, n)
		for f := 0; f < n; f++ {
			var sum float32
			for c := 0; c < channels; c++ {
				off := (f*channels + c) * 4
				sum += math.Float32frombits(binary.LittleEndian.Uint32(data[off : off+4]))
			}
			mono[f] = sum / float32(channels)
		}
	default:
		return nil, 0, fmt.Errorf("unsupported wav encoding (format %d, %d-bit)", format, bitsPerSamp)
	}
	if len(mono) == 0 {
		return nil, 0, fmt.Errorf("empty data chunk")
	}
	return mono, rate, nil
}

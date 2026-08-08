package play

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// wavInfo describes the PCM payload of a RIFF/WAVE file.
type wavInfo struct {
	audioFormat int // 1 = PCM
	channels    int
	sampleRate  int
	bits        int
	dataOffset  int64
	dataLen     int64
}

// parseWav walks the RIFF chunks of the file at path. It is deliberately
// tolerant of bogus chunk lengths (the emote-chat server streams WAVs
// with a placeholder RIFF length): any chunk size that runs past EOF is
// clamped to the real file size.
func parseWav(path string) (wavInfo, error) {
	var info wavInfo
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return info, err
	}
	fileSize := st.Size()

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return info, fmt.Errorf("%s: not a WAV file", path)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return info, fmt.Errorf("%s: not a WAV file", path)
	}

	off := int64(12)
	for off+8 <= fileSize {
		var chunk [8]byte
		if _, err := f.ReadAt(chunk[:], off); err != nil {
			break
		}
		id := string(chunk[0:4])
		size := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		body := off + 8
		if body+size > fileSize || size < 0 {
			size = fileSize - body // clamp bogus / streamed lengths
		}
		switch id {
		case "fmt ":
			var fmtBuf [16]byte
			if size >= 16 {
				if _, err := f.ReadAt(fmtBuf[:], body); err != nil {
					return info, err
				}
				info.audioFormat = int(binary.LittleEndian.Uint16(fmtBuf[0:2]))
				info.channels = int(binary.LittleEndian.Uint16(fmtBuf[2:4]))
				info.sampleRate = int(binary.LittleEndian.Uint32(fmtBuf[4:8]))
				info.bits = int(binary.LittleEndian.Uint16(fmtBuf[14:16]))
			}
		case "data":
			info.dataOffset = body
			info.dataLen = size
		}
		if size%2 == 1 {
			size++ // RIFF chunks are word-aligned
		}
		off = body + size
	}

	if info.sampleRate == 0 || info.channels == 0 {
		return info, fmt.Errorf("%s: missing fmt chunk", path)
	}
	if info.dataOffset == 0 || info.dataLen <= 0 {
		return info, fmt.Errorf("%s: missing data chunk", path)
	}
	return info, nil
}

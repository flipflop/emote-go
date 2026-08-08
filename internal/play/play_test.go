package play

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeWav writes a minimal PCM16 WAV; riffLen==-1 means correct length,
// otherwise the bogus value is written (server-streamed placeholder case).
func writeWav(t *testing.T, path string, rate, channels, nSamples int, riffLen int) {
	t.Helper()
	data := make([]byte, nSamples*channels*2)
	buf := make([]byte, 0, 44+len(data))
	le := binary.LittleEndian
	u32 := func(v uint32) []byte { b := make([]byte, 4); le.PutUint32(b, v); return b }
	u16 := func(v uint16) []byte { b := make([]byte, 2); le.PutUint16(b, v); return b }

	rl := uint32(36 + len(data))
	if riffLen >= 0 {
		rl = uint32(riffLen)
	}
	buf = append(buf, []byte("RIFF")...)
	buf = append(buf, u32(rl)...)
	buf = append(buf, []byte("WAVE")...)
	buf = append(buf, []byte("fmt ")...)
	buf = append(buf, u32(16)...)
	buf = append(buf, u16(1)...) // PCM
	buf = append(buf, u16(uint16(channels))...)
	buf = append(buf, u32(uint32(rate))...)
	buf = append(buf, u32(uint32(rate*channels*2))...)
	buf = append(buf, u16(uint16(channels*2))...)
	buf = append(buf, u16(16)...)
	buf = append(buf, []byte("data")...)
	dl := uint32(len(data))
	if riffLen >= 0 {
		dl = 0xFFFFFFF0 // bogus data length too
	}
	buf = append(buf, u32(dl)...)
	buf = append(buf, data...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseWav(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ok.wav")
	writeWav(t, p, 24000, 1, 2400, -1)
	info, err := parseWav(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.sampleRate != 24000 || info.channels != 1 || info.bits != 16 || info.audioFormat != 1 {
		t.Errorf("bad fmt parse: %+v", info)
	}
	if info.dataLen != 4800 || info.dataOffset != 44 {
		t.Errorf("bad data chunk: %+v", info)
	}
}

func TestParseWavBogusRiffLenClamped(t *testing.T) {
	// emote-chat server streams WAVs with placeholder RIFF/data lengths;
	// the parser must clamp to the real file size.
	p := filepath.Join(t.TempDir(), "bogus.wav")
	writeWav(t, p, 24000, 1, 1000, 0)
	info, err := parseWav(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.dataLen != 2000 {
		t.Errorf("bogus data length not clamped to file size: %+v", info)
	}
}

func TestParseWavRejectsNonWav(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no.wav")
	os.WriteFile(p, []byte("definitely not a wav"), 0o644)
	if _, err := parseWav(p); err == nil {
		t.Errorf("expected error for non-WAV file")
	}
}

func TestCurrentlyPlaying(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "afplay.pid")

	if _, ok := CurrentlyPlaying(pidfile); ok {
		t.Errorf("missing pidfile must report not playing")
	}

	// Our own pid is definitely alive.
	os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	if pid, ok := CurrentlyPlaying(pidfile); !ok || pid != os.Getpid() {
		t.Errorf("live pid not detected: %d %v", pid, ok)
	}

	// A stale pid: unlikely-to-exist pid, file must be removed.
	os.WriteFile(pidfile, []byte("999999"), 0o644)
	if _, ok := CurrentlyPlaying(pidfile); ok {
		t.Errorf("stale pid reported as playing")
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Errorf("stale pidfile must be removed")
	}

	// Garbage content.
	os.WriteFile(pidfile, []byte("not-a-pid"), 0o644)
	if _, ok := CurrentlyPlaying(pidfile); ok {
		t.Errorf("garbage pidfile reported as playing")
	}
}

func TestStopRemovesPidfile(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "afplay.pid")
	os.WriteFile(pidfile, []byte("999999"), 0o644)
	Stop(pidfile)
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Errorf("Stop must remove the pidfile")
	}
}

func TestDefaultPidfileHonorsEnv(t *testing.T) {
	t.Setenv("EMOTE_CONFIG_DIR", "/tmp/emote-test-cfg")
	if got := DefaultPidfile(); got != "/tmp/emote-test-cfg/afplay.pid" {
		t.Errorf("EMOTE_CONFIG_DIR not honored: %s", got)
	}
}

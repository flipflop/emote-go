package engine

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- pure unit tests (no model needed) -------------------------------------

func makeWav(t *testing.T, rate int, channels int, samples []int16, breakSizes bool) []byte {
	t.Helper()
	dataLen := len(samples) * 2
	b := make([]byte, 44+dataLen)
	le := binary.LittleEndian
	copy(b[0:4], "RIFF")
	le.PutUint32(b[4:8], uint32(36+dataLen))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	le.PutUint32(b[16:20], 16)
	le.PutUint16(b[20:22], 1)
	le.PutUint16(b[22:24], uint16(channels))
	le.PutUint32(b[24:28], uint32(rate))
	le.PutUint32(b[28:32], uint32(rate*2*channels))
	le.PutUint16(b[32:34], uint16(2*channels))
	le.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	le.PutUint32(b[40:44], uint32(dataLen))
	for i, s := range samples {
		le.PutUint16(b[44+i*2:46+i*2], uint16(s))
	}
	if breakSizes { // simulate a server-streamed wav with bogus lengths
		le.PutUint32(b[4:8], 0xFFFFFFFF)
		le.PutUint32(b[40:44], 0xFFFFFFFF)
	}
	return b
}

func TestDecodeWaveMono16(t *testing.T) {
	b := makeWav(t, 24000, 1, []int16{0, 16384, -16384, 32767}, false)
	got, rate, err := decodeWave(b)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 || len(got) != 4 {
		t.Fatalf("rate=%d len=%d", rate, len(got))
	}
	if got[0] != 0 || math.Abs(float64(got[1])-0.5) > 1e-4 || math.Abs(float64(got[2])+0.5) > 1e-4 {
		t.Errorf("samples = %v", got)
	}
}

func TestDecodeWaveBogusSizes(t *testing.T) {
	// The tolerant reader must clamp the huge declared sizes to the bytes
	// present (RIFF repair semantics for server-streamed wavs).
	b := makeWav(t, 24000, 1, []int16{100, 200, 300}, true)
	got, rate, err := decodeWave(b)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 || len(got) != 3 {
		t.Errorf("rate=%d len=%d, want 24000/3", rate, len(got))
	}
}

func TestDecodeWaveStereoAveraged(t *testing.T) {
	// Interleaved L/R pairs; mono out is the mean.
	b := makeWav(t, 8000, 2, []int16{16384, 0, -16384, -16384}, false)
	got, _, err := decodeWave(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if math.Abs(float64(got[0])-0.25) > 1e-4 || math.Abs(float64(got[1])+0.5) > 1e-4 {
		t.Errorf("averaged samples = %v", got)
	}
}

func TestDecodeWaveRejectsGarbage(t *testing.T) {
	if _, _, err := decodeWave([]byte("MP3 or whatever this is")); err == nil {
		t.Error("want error for non-wav bytes")
	}
}

func TestExtraJSONMaxFramesGuard(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(extraJSON("Hello!"), &m); err != nil {
		t.Fatal(err)
	}
	if got := m["max_frames"].(float64); got != 6*3+40 {
		t.Errorf("max_frames = %v, want 58", got)
	}
	if got := m["seed"].(float64); got != 42 {
		t.Errorf("seed = %v", got)
	}
	if got := m["temperature"].(float64); got != 0.7 {
		t.Errorf("temperature = %v", got)
	}
	long := strings.Repeat("a", 400)
	if err := json.Unmarshal(extraJSON(long), &m); err != nil {
		t.Fatal(err)
	}
	if got := m["max_frames"].(float64); got != maxFramesCap {
		t.Errorf("max_frames = %v, want cap %d", got, maxFramesCap)
	}
}

func TestModelPathPrefersInt8File(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "decoder.int8.onnx"), []byte("x"), 0o644)
	if got := modelPath(dir, "decoder"); !strings.HasSuffix(got, "decoder.int8.onnx") {
		t.Errorf("got %s", got)
	}
	if got := modelPath(dir, "encoder"); !strings.HasSuffix(got, "encoder.onnx") {
		t.Errorf("got %s", got)
	}
}

func TestBundleCompleteAndTierOrder(t *testing.T) {
	root := t.TempDir()
	mixed := filepath.Join(root, "models", "pocket-mixed-flowfp32")
	os.MkdirAll(mixed, 0o755)
	for _, f := range []string{"lm_flow.onnx", "lm_main.int8.onnx", "encoder.onnx", "decoder.int8.onnx", "text_conditioner.onnx", "vocab.json", "token_scores.json"} {
		os.WriteFile(filepath.Join(mixed, f), []byte("x"), 0o644)
	}
	if !bundleComplete(mixed) {
		t.Fatal("mixed bundle should be complete")
	}
	incomplete := filepath.Join(root, "models", "sherpa-onnx-pocket-tts-2026-01-26")
	os.MkdirAll(incomplete, 0o755)
	if bundleComplete(incomplete) {
		t.Fatal("empty dir must not count as a bundle")
	}
}

func TestNewFailsWithoutModels(t *testing.T) {
	if _, err := New(t.TempDir()); err == nil {
		t.Fatal("want error for unprovisioned root")
	}
}

// --- smoke test against the real model (skips if not present) --------------

// repoRoot returns the checkout root (this file lives in internal/engine).
func repoRoot() string {
	abs, _ := filepath.Abs(filepath.Join("..", ".."))
	return abs
}

func TestEngineSmoke(t *testing.T) {
	root := repoRoot()
	if !bundleAvailable(root) {
		t.Skipf("no pocket-tts bundle under %s/models; run provisioning or fetch P1 artifacts", root)
	}
	if _, err := os.Stat(filepath.Join(root, "refs", "jane-bright-synth.wav")); err != nil {
		t.Skip("jane-bright-synth.wav not present in refs/")
	}

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	t.Logf("model dir: %s", e.ModelDir())

	chunks := 0
	a, err := e.SynthesizeStream("Hello!", "jane-bright-synth", func(s []float32) bool {
		chunks++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 24000 {
		t.Errorf("sample rate = %d, want 24000", a.SampleRate)
	}
	dur := a.Duration()
	// The max_frames guard (58 frames ~ 4.6 s) must prevent the bare-
	// "Hello!" EOS runaway (P1.5 §4 measured 44 s without it).
	if dur < 0.2 || dur > 6.0 {
		t.Errorf("duration = %.2fs, want 0.2-6.0s (runaway guard)", dur)
	}
	if chunks == 0 {
		t.Error("streaming callback never fired")
	}
	t.Logf("rendered %.2fs in %d chunks", dur, chunks)

	// Unknown reference must error cleanly.
	if _, err := e.Synthesize("Hi.", "no-such-ref"); err == nil {
		t.Error("want error for missing reference")
	}
	// Empty text must error cleanly.
	if _, err := e.Synthesize("   ", "jane-bright-synth"); err == nil {
		t.Error("want error for empty text")
	}
}

func bundleAvailable(root string) bool {
	for _, d := range modelBundleDirs {
		if bundleComplete(filepath.Join(root, "models", d)) {
			return true
		}
	}
	return false
}

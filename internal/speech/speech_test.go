package speech

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/engine"
)

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Hello! This is jane. Done?", []string{"Hello!", "This is jane.", "Done?"}},
		{"No terminal punctuation", []string{"No terminal punctuation"}},
		{`He said "stop!" Then left.`, []string{`He said "stop!"`, "Then left."}},
		{"Wait... what?!", []string{"Wait...", "what?!"}},
		{"  spaced.   out.  ", []string{"spaced.", "out."}},
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		if got := SplitSentences(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitSentences(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRefForEmotion(t *testing.T) {
	cases := map[string]string{
		"sad":      "jane-sad-synth",
		"excited":  "jane-excited-synth",
		"bright":   "jane-bright-synth",
		"neutral":  "jane-bright-synth",
		"calm":     "jane-bright-synth",
		"happy":    "jane-bright-synth",
		"":         "jane-bright-synth",
		"SAD":      "jane-sad-synth",
		" Excited": "jane-excited-synth",
		"confused": "jane-bright-synth", // unknown falls back to default
	}
	for in, want := range cases {
		if got := RefForEmotion(in); got != want {
			t.Errorf("RefForEmotion(%q) = %q, want %q", in, got, want)
		}
	}
}

// sr 1000 makes ms == samples for pad math.
const testSR = 1000

func TestTrimBoundsExact(t *testing.T) {
	// 300 sub-gate, 500 voiced, 400 sub-gate.
	x := make([]float32, 1200)
	for i := range x {
		x[i] = 0.001
	}
	for i := 300; i < 800; i++ {
		x[i] = 0.5
	}
	a, b := TrimBounds(x, testSR, TrimGate, LeadPadMs, TrailPadMs)
	if a != 300-120 || b != 800+20 {
		t.Errorf("TrimBounds = (%d, %d), want (180, 820)", a, b)
	}
}

func TestTrimBoundsClamps(t *testing.T) {
	// Energy starts at sample 5: lead pad clamps to 0; energy runs to the
	// end: trail pad clamps to len.
	x := make([]float32, 100)
	for i := 5; i < 100; i++ {
		x[i] = 0.5
	}
	a, b := TrimBounds(x, testSR, TrimGate, LeadPadMs, TrailPadMs)
	if a != 0 || b != 100 {
		t.Errorf("TrimBounds = (%d, %d), want (0, 100)", a, b)
	}
}

func TestTrimBoundsAllSilence(t *testing.T) {
	x := make([]float32, 50) // all zero: never trim to nothing
	a, b := TrimBounds(x, testSR, TrimGate, LeadPadMs, TrailPadMs)
	if a != 0 || b != 50 {
		t.Errorf("TrimBounds(silence) = (%d, %d), want (0, 50)", a, b)
	}
}

func TestFadeEdgesExact(t *testing.T) {
	x := make([]float32, 100)
	for i := range x {
		x[i] = 1
	}
	FadeEdges(x, testSR, FadeMs) // n = 8 samples
	for i := 0; i < 8; i++ {
		want := float32(i) / 8
		if x[i] != want {
			t.Errorf("x[%d] = %v, want %v", i, x[i], want)
		}
		if x[99-i] != want {
			t.Errorf("x[%d] = %v, want %v", 99-i, x[99-i], want)
		}
	}
	if x[8] != 1 || x[91] != 1 {
		t.Errorf("fade overran: x[8]=%v x[91]=%v, want 1", x[8], x[91])
	}
}

func TestFadeEdgesShortSlice(t *testing.T) {
	x := []float32{1, 1, 1, 1} // 8-sample fade must shrink to len/2 = 2
	FadeEdges(x, testSR, FadeMs)
	want := []float32{0, 0.5, 0.5, 0}
	for i := range want {
		if x[i] != want[i] {
			t.Errorf("x = %v, want %v", x, want)
			break
		}
	}
}

// fakeSynth returns canned audio per sentence, in call order.
type fakeSynth struct {
	t        *testing.T
	rate     int
	perCall  [][]float32
	call     int
	refsSeen []string
	err      error
}

func (f *fakeSynth) Synthesize(text, refName string) (engine.Audio, error) {
	if f.err != nil {
		return engine.Audio{}, f.err
	}
	f.refsSeen = append(f.refsSeen, refName)
	if f.call >= len(f.perCall) {
		f.t.Fatalf("unexpected extra Synthesize call %d (%q)", f.call, text)
	}
	s := f.perCall[f.call]
	f.call++
	return engine.Audio{Samples: s, SampleRate: f.rate}, nil
}

// block returns n samples at amplitude a.
func block(n int, a float32) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = a
	}
	return x
}

func TestRenderRefGapAndStitchExact(t *testing.T) {
	// Two segments fully above the gate: trim keeps everything, so
	// total = len1 + gap + len2 with gap = sr*350/1000.
	// "Ten"/"Three" deliberately avoid the weak-onset predicate so this test
	// keeps exercising the plain (un-primed) path; primer-excision has its
	// own tests in onset_test.go.
	f := &fakeSynth{t: t, rate: testSR, perCall: [][]float32{block(100, 0.5), block(50, 0.5)}}
	got, err := RenderRef(f, "Ten two. Three!", "jane-bright-synth", nil)
	if err != nil {
		t.Fatal(err)
	}
	gap := testSR * GapMs / 1000       // 350 samples
	tail := testSR * TailRoomMs / 1000 // decay room appended once at the end
	wantLen := 100 + gap + 50 + tail
	if len(got.Samples) != wantLen {
		t.Fatalf("stitched length = %d, want %d", len(got.Samples), wantLen)
	}
	// Tail room must be pure silence.
	for i := wantLen - tail; i < wantLen; i++ {
		if got.Samples[i] != 0 {
			t.Fatalf("tail sample %d = %v, want 0", i, got.Samples[i])
		}
	}
	if got.SampleRate != testSR {
		t.Errorf("rate = %d, want %d", got.SampleRate, testSR)
	}
	// Gap region must be pure silence.
	for i := 100; i < 100+gap; i++ {
		if got.Samples[i] != 0 {
			t.Fatalf("gap sample %d = %v, want 0", i, got.Samples[i])
		}
	}
	// Fades applied at every segment edge (first fade sample is 0).
	if got.Samples[0] != 0 || got.Samples[99] != 0 {
		t.Errorf("segment 1 edges not faded: %v ... %v", got.Samples[0], got.Samples[99])
	}
	if got.Samples[100+gap] != 0 || got.Samples[100+gap+50-1] != 0 {
		t.Errorf("segment 2 edges not faded")
	}
	// Mid-fade value exact: sample 4 of an 8-sample fade on 0.5 amplitude.
	if want := float32(4) / 8 * 0.5; got.Samples[4] != want {
		t.Errorf("fade math: sample 4 = %v, want %v", got.Samples[4], want)
	}
}

func TestRenderRefTrimsPadding(t *testing.T) {
	// Segment: 300 samples of sub-gate noise, 400 voiced, 200 sub-gate.
	// Trim keeps [300-120, 700+20) = 540 samples.
	seg := append(append(block(300, 0.001), block(400, 0.5)...), block(200, 0.001)...)
	f := &fakeSynth{t: t, rate: testSR, perCall: [][]float32{seg}}
	got, err := RenderRef(f, "Testing!", "jane-bright-synth", nil) // non-weak onset: no primer

	if err != nil {
		t.Fatal(err)
	}
	if want := 540 + testSR*TailRoomMs/1000; len(got.Samples) != want {
		t.Errorf("trimmed length = %d, want %d", len(got.Samples), want)
	}
}

func TestRenderRefSegmentCallback(t *testing.T) {
	f := &fakeSynth{t: t, rate: testSR, perCall: [][]float32{block(40, 0.5), block(60, 0.5)}}
	var texts []string
	var lens []int
	_, err := RenderRef(f, "A one. B two.", "jane-sad-synth", func(s Segment, sr int) {
		texts = append(texts, s.Text)
		lens = append(lens, len(s.Samples))
		if sr != testSR {
			t.Errorf("callback sr = %d, want %d", sr, testSR)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(texts, []string{"A one.", "B two."}) {
		t.Errorf("callback texts = %q", texts)
	}
	if !reflect.DeepEqual(lens, []int{40, 60}) {
		t.Errorf("callback lens = %v", lens)
	}
	if !reflect.DeepEqual(f.refsSeen, []string{"jane-sad-synth", "jane-sad-synth"}) {
		t.Errorf("refs seen = %q", f.refsSeen)
	}
}

func TestRenderUsesEmotionMapping(t *testing.T) {
	f := &fakeSynth{t: t, rate: testSR, perCall: [][]float32{block(30, 0.5)}}
	if _, err := Render(f, "Oh no.", "sad"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.refsSeen, []string{"jane-sad-synth"}) {
		t.Errorf("refs seen = %q, want jane-sad-synth", f.refsSeen)
	}
}

func TestRenderErrors(t *testing.T) {
	f := &fakeSynth{t: t, rate: testSR}
	if _, err := RenderRef(f, "   ", "x", nil); err == nil {
		t.Error("empty text: want error")
	}
	f2 := &fakeSynth{t: t, err: fmt.Errorf("boom")}
	if _, err := RenderRef(f2, "Hi.", "x", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("propagated error = %v", err)
	}
}

func TestEncodeWAVHeaderAndSamples(t *testing.T) {
	a := engine.Audio{Samples: []float32{0, 0.5, -0.5, 1, -1, 2, -2}, SampleRate: 24000}
	b := EncodeWAV(a)
	if len(b) != 44+len(a.Samples)*2 {
		t.Fatalf("len = %d", len(b))
	}
	le := binary.LittleEndian
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[36:40]) != "data" {
		t.Fatal("bad magic")
	}
	if le.Uint32(b[4:8]) != uint32(len(b)-8) {
		t.Errorf("riff size = %d, want %d", le.Uint32(b[4:8]), len(b)-8)
	}
	if le.Uint16(b[20:22]) != 1 || le.Uint16(b[22:24]) != 1 || le.Uint16(b[34:36]) != 16 {
		t.Error("fmt fields wrong")
	}
	if le.Uint32(b[24:28]) != 24000 {
		t.Error("sample rate wrong")
	}
	if le.Uint32(b[40:44]) != uint32(len(a.Samples)*2) {
		t.Error("data size wrong")
	}
	get := func(i int) int16 { return int16(le.Uint16(b[44+i*2 : 46+i*2])) }
	if get(0) != 0 {
		t.Errorf("s0 = %d", get(0))
	}
	if got := get(1); math.Abs(float64(got)-16383.5) > 1 {
		t.Errorf("s1 = %d, want ~16384", got)
	}
	if get(3) != 32767 || get(4) != -32767 {
		t.Errorf("full scale: %d %d", get(3), get(4))
	}
	// Clamping: 2 and -2 must clip, not wrap.
	if get(5) != 32767 || get(6) != -32767 {
		t.Errorf("clamp: %d %d", get(5), get(6))
	}
}

func TestRepairRIFF(t *testing.T) {
	good := EncodeWAV(engine.Audio{Samples: block(100, 0.25), SampleRate: 24000})

	// Break the sizes the way a streaming server does.
	bad := make([]byte, len(good))
	copy(bad, good)
	binary.LittleEndian.PutUint32(bad[4:8], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(bad[40:44], 0xFFFFFFFF)

	fixed, err := RepairRIFF(bad)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixed, good) {
		t.Error("repair did not restore correct sizes")
	}
	// Input untouched.
	if binary.LittleEndian.Uint32(bad[4:8]) != 0xFFFFFFFF {
		t.Error("RepairRIFF modified its input")
	}
	// Idempotent on a correct file.
	again, err := RepairRIFF(good)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, good) {
		t.Error("repair changed an already-correct wav")
	}
	if _, err := RepairRIFF([]byte("not a wav, definitely not a wav, no sir not at all today")); err == nil {
		t.Error("want error for non-wav input")
	}
}

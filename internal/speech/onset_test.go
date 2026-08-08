package speech

import (
	"reflect"
	"testing"

	"github.com/flipflop/emote-go/internal/engine"
)

func TestHasWeakOnset(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Letter heuristic: h-, w-, y- (wh- included).
		{"Hello there, friend.", true},
		{"hello", true},
		{"Why is this happening?", true},
		{"What time is it?", true},
		{"Yes, we can.", true},
		{"Who goes there?", true},
		// Spelling/sound special cases (/w/ behind a vowel letter).
		{"One, two, three.", true},
		{"one hundred", true},
		{"Once upon a time.", true},
		// Leading punctuation is skipped to the first word.
		{`"Why me?"`, true},
		{"  ...well then.", true},
		// Non-weak controls.
		{"Ten green bottles.", false},
		{"Say something nice to me.", false},
		{"A one. B two.", false}, // "one" not first word
		{"Oh no.", false},
		{"Okay: fine.", false},
		// No usable first word.
		{"", false},
		{"   ", false},
		{"?!", false},
		// Leading digit run: normalization left it verbatim (hex/time/code),
		// never a weak onset.
		{"0x1F rocks.", false},
		{"11:41 already.", false},
	}
	for _, c := range cases {
		if got := HasWeakOnset(c.in); got != c.want {
			t.Errorf("HasWeakOnset(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// seam builds synthetic primed PCM at testSR (1000 Hz: ms == samples) from
// alternating silence/voiced spans, mirroring the real "Okay: ..." shape.
func seam(spans ...[]float32) []float32 {
	var x []float32
	for _, s := range spans {
		x = append(x, s...)
	}
	return x
}

func TestFindPrimerSeamExact(t *testing.T) {
	// 50 silence, 300 primer burst, 200 colon gap, 400 content, 100 silence.
	x := seam(block(50, 0), block(300, 0.5), block(200, 0), block(400, 0.5), block(100, 0))
	cut, ok := FindPrimerSeam(x, testSR, TrimGate)
	if !ok || cut != 430 { // gap end 550 - LeadPadMs 120
		t.Errorf("cut = (%d, %v), want (430, true)", cut, ok)
	}
}

func TestFindPrimerSeamSkipsStopClosure(t *testing.T) {
	// The /k/ closure inside "Okay" (60 ms at 60 ms after burst start) must
	// not be mistaken for the seam: too short AND too early.
	x := seam(block(50, 0), block(60, 0.5), block(60, 0), block(230, 0.5),
		block(200, 0), block(300, 0.5))
	cut, ok := FindPrimerSeam(x, testSR, TrimGate)
	if !ok || cut != 480 { // real gap [400,600): 600 - 120
		t.Errorf("cut = (%d, %v), want (480, true)", cut, ok)
	}
}

func TestFindPrimerSeamLeadPadClampsToGapStart(t *testing.T) {
	// Gap shorter than LeadPadMs: cut clamps to gap start, never back into
	// primer voicing.
	x := seam(block(50, 0), block(200, 0.5), block(100, 0), block(300, 0.5))
	cut, ok := FindPrimerSeam(x, testSR, TrimGate)
	if !ok || cut != 250 { // gap end 350 - 120 = 230 < gap start 250
		t.Errorf("cut = (%d, %v), want (250, true)", cut, ok)
	}
}

func TestFindPrimerSeamRejects(t *testing.T) {
	cases := []struct {
		name string
		x    []float32
	}{
		{"all silence", block(400, 0)},
		{"no gap at all", seam(block(50, 0), block(900, 0.5))},
		{"gap runs to EOF (no content after primer)",
			seam(block(50, 0), block(300, 0.5), block(500, 0))},
		{"gap past the window (would cut real content)",
			seam(block(50, 0), block(900, 0.5), block(200, 0), block(100, 0.5))},
		{"only early micro-gaps",
			seam(block(50, 0), block(100, 0.5), block(60, 0), block(700, 0.5))},
	}
	for _, c := range cases {
		if cut, ok := FindPrimerSeam(c.x, testSR, TrimGate); ok {
			t.Errorf("%s: FindPrimerSeam = (%d, true), want no seam", c.name, cut)
		}
	}
}

// textSynth serves canned audio keyed by exact synthesis text and records
// the calls, so tests can assert primed/un-primed call sequences.
type textSynth struct {
	t     *testing.T
	audio map[string][]float32
	calls []string
}

func (m *textSynth) Synthesize(text, ref string) (engine.Audio, error) {
	m.calls = append(m.calls, text)
	s, ok := m.audio[text]
	if !ok {
		m.t.Fatalf("unexpected Synthesize(%q)", text)
	}
	return engine.Audio{Samples: s, SampleRate: testSR}, nil
}

func TestRenderRefPrimerExcision(t *testing.T) {
	// Weak-onset sentence: RenderRef must synthesize "Okay: " + sentence and
	// stream ONLY the excised content — never primer audio, never the primer
	// text.
	primed := seam(block(10, 0), block(300, 0.5), block(200, 0), block(400, 0.5), block(30, 0))
	m := &textSynth{t: t, audio: map[string][]float32{"Okay: One two.": primed}}
	var segTexts []string
	var segLens []int
	got, err := RenderRef(m, "One two.", "jane-bright-synth", func(s Segment, sr int) {
		segTexts = append(segTexts, s.Text)
		segLens = append(segLens, len(s.Samples))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.calls, []string{"Okay: One two."}) {
		t.Errorf("calls = %q, want just the primed render", m.calls)
	}
	// Seam: burst 10, gap [310,510), cut 510-120=390. Excised = 550 samples
	// (120 sub-gate lead + 400 content + 30 tail). TrimBounds keeps
	// [0, 520+20) = 540; + TailRoomMs.
	if !reflect.DeepEqual(segTexts, []string{"One two."}) || !reflect.DeepEqual(segLens, []int{540}) {
		t.Errorf("segments = %q %v, want [\"One two.\"] [540]", segTexts, segLens)
	}
	if want := 540 + testSR*TailRoomMs/1000; len(got.Samples) != want {
		t.Errorf("render length = %d, want %d", len(got.Samples), want)
	}
}

func TestRenderRefPrimerFallback(t *testing.T) {
	// Primed render with no clean seam (solid voicing, no colon pause): the
	// HARD SAFETY RULE demands a fresh un-primed render, never a risky cut.
	m := &textSynth{t: t, audio: map[string][]float32{
		"Okay: Hello.": block(600, 0.5),
		"Hello.":       block(100, 0.5),
	}}
	var segLens []int
	got, err := RenderRef(m, "Hello.", "jane-bright-synth", func(s Segment, sr int) {
		segLens = append(segLens, len(s.Samples))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.calls, []string{"Okay: Hello.", "Hello."}) {
		t.Errorf("calls = %q, want primed then un-primed fallback", m.calls)
	}
	if !reflect.DeepEqual(segLens, []int{100}) {
		t.Errorf("segment lens = %v, want [100] (the un-primed render)", segLens)
	}
	if want := 100 + testSR*TailRoomMs/1000; len(got.Samples) != want {
		t.Errorf("render length = %d, want %d", len(got.Samples), want)
	}
}

func TestRenderRefNonWeakOnsetNeverPrimed(t *testing.T) {
	m := &textSynth{t: t, audio: map[string][]float32{"Ten green bottles.": block(120, 0.5)}}
	if _, err := RenderRef(m, "Ten green bottles.", "jane-bright-synth", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.calls, []string{"Ten green bottles."}) {
		t.Errorf("calls = %q, want the plain render only", m.calls)
	}
}

func TestRenderRefPerSentencePriming(t *testing.T) {
	// Weak onsets are guarded per sentence, not only render-initially:
	// whisper showed "Ten. One, two." clipping its SECOND sentence to "on".
	primed := seam(block(10, 0), block(300, 0.5), block(200, 0), block(400, 0.5), block(30, 0))
	m := &textSynth{t: t, audio: map[string][]float32{
		"Ten.":            block(80, 0.5),
		"Okay: One, two.": primed,
	}}
	if _, err := RenderRef(m, "Ten. One, two.", "jane-bright-synth", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.calls, []string{"Ten.", "Okay: One, two."}) {
		t.Errorf("calls = %q", m.calls)
	}
}

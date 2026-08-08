// Package speech implements the per-sentence rendering pipeline locked by
// P1.6/P1.7: sentences are split in Go (bypassing the engine's space-dropping
// sentence merge, the "Hello!This" garble), rendered one engine call each,
// edge-trimmed with a generous 120 ms lead pad (so /h/ frication survives the
// amplitude gate), given 8 ms anti-click fades, and stitched with 350 ms of
// our own silence. It also maps emotions to reference voice names and carries
// the WAV encode / RIFF-repair helpers.
package speech

import (
	"fmt"
	"strings"

	"github.com/flipflop/emote-go/internal/engine"
)

// Pipeline constants, locked by P1.7 (DESIGN.md "Engine ship config").
const (
	// GapMs is the inter-sentence silence. py-jane's pause profile is
	// 255-459 ms; 350 ms sits mid-band.
	GapMs = 350
	// LeadPadMs is kept before the first above-gate sample. 120 ms is
	// deliberately generous: an amplitude gate walks straight through /h/
	// frication (~0.002-0.008) and a 20 ms pad ate onsets (P1.7 §1a).
	LeadPadMs = 120
	// TrailPadMs is kept after the last above-gate sample.
	TrailPadMs = 20
	// TailRoomMs of silence is appended once at the very end of an assembled
	// render (decay room after the final word; also playback-drain insurance).
	TailRoomMs = 250
	// TrimGate is the amplitude threshold for edge trimming.
	TrimGate = 0.010
	// FadeMs is the linear fade applied to both trimmed edges (anti-click).
	FadeMs = 8
)

// Synthesizer is the engine capability the pipeline needs; *engine.Engine
// satisfies it. Tests substitute synthetic PCM generators.
type Synthesizer interface {
	Synthesize(text, refName string) (engine.Audio, error)
}

// RefForEmotion maps an emotion label to a provisioned reference wav name.
// Per DESIGN.md: bright is the user-approved default and also stands in for
// neutral, calm and happy until dedicated refs pass the onset QA gate; sad
// and excited have their own references. Unknown labels fall back to bright.
func RefForEmotion(emotion string) string {
	switch strings.ToLower(strings.TrimSpace(emotion)) {
	case "sad":
		return "jane-sad-synth"
	case "excited":
		return "jane-excited-synth"
	default: // "", bright, neutral, calm, happy, anything else
		return "jane-bright-synth"
	}
}

// SplitSentences splits text after `.` `!` `?` (plus trailing quotes and
// closing brackets), so each sentence becomes its own engine call and the
// engine's buggy short-sentence merge never runs across a join.
func SplitSentences(text string) []string {
	var out []string
	var cur []rune
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		cur = append(cur, c)
		if c == '.' || c == '!' || c == '?' {
			for i+1 < len(runes) && strings.ContainsRune(`.!?"')]`, runes[i+1]) {
				i++
				cur = append(cur, runes[i])
			}
			if s := strings.TrimSpace(string(cur)); s != "" {
				out = append(out, s)
			}
			cur = cur[:0]
		}
	}
	if s := strings.TrimSpace(string(cur)); s != "" {
		out = append(out, s)
	}
	return out
}

// TrimBounds finds the first and last samples above the amplitude gate and
// returns slice bounds [a, b) keeping leadPadMs of context before the first
// energy and trailPadMs after the last. If no sample exceeds the gate the
// whole slice is returned (never trim a segment to nothing).
func TrimBounds(x []float32, sampleRate int, gate float32, leadPadMs, trailPadMs int) (int, int) {
	leadPad := sampleRate * leadPadMs / 1000
	trailPad := sampleRate * trailPadMs / 1000
	a := 0
	for a < len(x) && absf(x[a]) < gate {
		a++
	}
	if a == len(x) { // nothing above the gate: keep everything
		return 0, len(x)
	}
	b := len(x)
	for b > a && absf(x[b-1]) < gate {
		b--
	}
	if a-leadPad > 0 {
		a -= leadPad
	} else {
		a = 0
	}
	if b+trailPad < len(x) {
		b += trailPad
	} else {
		b = len(x)
	}
	return a, b
}

// FadeEdges applies linear fades of ms milliseconds to both ends of x in
// place, so trimmed joins cannot click. If the slice is shorter than two
// fades, the fade shrinks to half the slice.
func FadeEdges(x []float32, sampleRate, ms int) {
	n := sampleRate * ms / 1000
	if n*2 > len(x) {
		n = len(x) / 2
	}
	for i := 0; i < n; i++ {
		g := float32(i) / float32(n)
		x[i] *= g
		x[len(x)-1-i] *= g
	}
}

// Segment is one rendered sentence after trim+fade, as assembled by Render.
type Segment struct {
	Text    string
	Samples []float32
}

// Render runs the full per-sentence pipeline for an emotion label:
// RefForEmotion + RenderRef.
func Render(s Synthesizer, text, emotion string) (engine.Audio, error) {
	return RenderRef(s, text, RefForEmotion(emotion), nil)
}

// RenderRef normalizes number tokens to spoken words (NormalizeForSpeech —
// before the split, so dots inside "3.14" or "1.2.3" never become sentence
// boundaries), splits text into sentences, synthesizes each with the named
// reference voice (weak-onset sentences via primer-excision, see onset.go —
// segments and callbacks only ever carry excised audio, never the primer),
// edge-trims (gate TrimGate, lead pad LeadPadMs, trail pad TrailPadMs),
// fades FadeMs, and stitches the segments with GapMs of silence.
// If onSegment is non-nil it is called with each finished segment in order —
// the natural streaming unit for playback (first sentence is ready in
// ~0.2-0.4 s while the rest still render).
func RenderRef(s Synthesizer, text, refName string, onSegment func(Segment, int)) (engine.Audio, error) {
	sentences := SplitSentences(NormalizeForSpeech(text))
	if len(sentences) == 0 {
		return engine.Audio{}, fmt.Errorf("speech: no sentences in %q", text)
	}
	var stitched []float32
	sampleRate := 0
	for i, sentence := range sentences {
		a, err := synthesizeWithOnsetGuard(s, sentence, refName)
		if err != nil {
			return engine.Audio{}, fmt.Errorf("speech: sentence %d/%d (%q): %w", i+1, len(sentences), sentence, err)
		}
		if len(a.Samples) == 0 {
			return engine.Audio{}, fmt.Errorf("speech: sentence %d/%d (%q) produced no audio", i+1, len(sentences), sentence)
		}
		if sampleRate == 0 {
			sampleRate = a.SampleRate
		} else if a.SampleRate != sampleRate {
			return engine.Audio{}, fmt.Errorf("speech: sample rate changed mid-render (%d -> %d)", sampleRate, a.SampleRate)
		}
		lo, hi := TrimBounds(a.Samples, sampleRate, TrimGate, LeadPadMs, TrailPadMs)
		seg := make([]float32, hi-lo)
		copy(seg, a.Samples[lo:hi])
		FadeEdges(seg, sampleRate, FadeMs)
		if onSegment != nil {
			onSegment(Segment{Text: sentence, Samples: seg}, sampleRate)
		}
		if i > 0 {
			stitched = append(stitched, make([]float32, sampleRate*GapMs/1000)...)
		}
		stitched = append(stitched, seg...)
	}
	// Natural decay room after the final word: the per-segment trail pad is
	// deliberately tight (it shapes the calibrated inter-sentence gaps), so
	// the assembled render ends almost immediately after the last phoneme and
	// sounds clipped even under perfect playback. Pad the very end only.
	stitched = append(stitched, make([]float32, sampleRate*TailRoomMs/1000)...)
	return engine.Audio{Samples: stitched, SampleRate: sampleRate}, nil
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

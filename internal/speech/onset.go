// PRIMER-EXCISION: the residual utterance-initial weak-onset fix.
//
// P1.7 established that the model itself skips utterance-initial weak onsets
// with some reference embeddings (raw output starts mid-phonation at sample 0
// — no trim setting can restore audio that was never generated), and that
// plain text priming is not shippable because the primer words leak into the
// audio. This file closes the gap left by the P1.7 lead-pad fix: sentences
// whose first word has a weak onset (/h/ /w/ /y/ — "one" clipping to "on",
// "Yes, we" garbling) are synthesized as "Okay: " + sentence, and the primer
// audio is then excised by seam detection, so the caller never hears it.
// Every sentence is its own engine call, so the guard is applied per
// sentence, not just to the first of a render (whisper-verified: the second
// sentence of "Ten. One, two." clipped to "on" exactly like a render-initial
// one).
//
// Seam detection (empirical, whisper-verified 2026-08-06 across the bright/
// sad/excited refs): in a raw "Okay: ..." render the colon pause is a
// sustained below-gate run of 127-277 ms starting 218-403 ms after the
// primer's first above-gate sample. The only competing gaps are the /k/
// closure inside "Okay" (55-61 ms, starting < 110 ms after burst start) and
// micro-gaps of 5-25 ms, so a qualifying seam is the first below-gate run of
// >= seamMinGapMs that starts within [seamMinStartMs, seamMaxStartMs] of
// burst start. Position bounds matter more than gap size: pauses between
// content words can be longer than the colon pause (194 ms observed), so
// "first qualifying", never "longest".
//
// HARD SAFETY RULE: if no clean seam is found — no burst, gap outside the
// window, or nothing above the gate after the gap — the sentence is
// re-rendered un-primed. Degraded onset beats eaten content.
package speech

import (
	"strings"
	"unicode"

	"github.com/flipflop/emote-go/internal/engine"
)

// onsetPrimer is prepended to weak-onset sentences before synthesis. The
// colon renders as a reliable 127-277 ms pause with every shipped ref.
// Variants tried and rejected in P1.7/ab4: bare comma prefixes (", ") make
// the model repeat short segments ("Hello! Hello!"); NBSP-joins byte-tokenize
// and garble; silence-padded refs destroy the embedding. "Okay: " needed no
// such compromise: 11/11 whisper-verified primed renders across the three
// refs carried the primer plus intact content, no repeats.
const onsetPrimer = "Okay: "

// Seam detector tuning (ms). See package comment for the measurements.
const (
	// seamMinGapMs is the minimum sustained below-gate run accepted as the
	// primer/content seam. Must sit above the /k/-closure gap inside "Okay"
	// (55-61 ms observed) and below the shortest colon pause (127 ms).
	seamMinGapMs = 80
	// seamMinStartMs..seamMaxStartMs is where the seam gap may start,
	// relative to the first above-gate sample. Colon pauses start 218-403 ms
	// after burst start; the /k/ closure starts < 110 ms after; the pause
	// after the first *content* word starts ~700 ms+. Outside the window we
	// assume the render did not shape "Okay: <pause>" as expected and fall
	// back rather than risk cutting real content.
	seamMinStartMs = 150
	seamMaxStartMs = 700
)

// weakOnsetSpecials lists spelling/sound mismatches the letter heuristic
// cannot see: words whose spelling opens with a vowel letter but whose sound
// opens with /w/. Keep this list minimal and evidence-backed — when unsure,
// exclude: a missed weak onset costs one degraded phoneme, a spurious entry
// costs an extra engine call per occurrence.
var weakOnsetSpecials = map[string]bool{
	"one":  true, // /wʌn/ — whisper-verified clipping to "on" (norm.go doc)
	"once": true, // /wʌns/
}

// HasWeakOnset reports whether the sentence's first word starts with a sound
// the model is prone to skip utterance-initially: /h/ /w/ /y/ (letter
// heuristic h-, w-, y-, which covers wh- too) plus the documented
// spelling/sound special cases. Sentences with no letters ("0x1F.") never
// qualify.
func HasWeakOnset(sentence string) bool {
	w := firstWord(sentence)
	if w == "" {
		return false
	}
	if weakOnsetSpecials[w] {
		return true
	}
	switch w[0] {
	case 'h', 'w', 'y':
		return true
	}
	return false
}

// firstWord returns the sentence's first run of letters, lowercased. A
// leading digit run means normalization chose to leave the token verbatim
// (hex, time, code-like) — no weak onset.
func firstWord(sentence string) string {
	start := -1
	runes := []rune(sentence)
	for i, c := range runes {
		if unicode.IsLetter(c) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			return strings.ToLower(string(runes[start:i]))
		}
		if unicode.IsDigit(c) {
			return ""
		}
	}
	if start < 0 {
		return ""
	}
	return strings.ToLower(string(runes[start:]))
}

// FindPrimerSeam locates the primer/content seam in a raw "Okay: ..." render
// and returns the sample index to cut at: seam-gap end minus LeadPadMs of
// pad, clamped so the cut never reaches back into primer voicing. ok is
// false when no clean seam exists (see the HARD SAFETY RULE above).
func FindPrimerSeam(x []float32, sampleRate int, gate float32) (int, bool) {
	minGap := sampleRate * seamMinGapMs / 1000
	minStart := sampleRate * seamMinStartMs / 1000
	maxStart := sampleRate * seamMaxStartMs / 1000
	leadPad := sampleRate * LeadPadMs / 1000

	burst := 0
	for burst < len(x) && absf(x[burst]) < gate {
		burst++
	}
	if burst == len(x) {
		return 0, false // never any energy: no primer burst to excise
	}
	i := burst
	for i < len(x) {
		// Advance to the next below-gate run.
		for i < len(x) && absf(x[i]) >= gate {
			i++
		}
		rs := i
		for i < len(x) && absf(x[i]) < gate {
			i++
		}
		if rs-burst > maxStart {
			return 0, false // past the plausible "Okay" span: no clean seam
		}
		if i-rs < minGap || rs-burst < minStart {
			continue // micro-gap or stop closure inside the primer
		}
		if i == len(x) {
			return 0, false // gap runs to EOF: no content after the primer
		}
		cut := i - leadPad
		if cut < rs {
			cut = rs // never cut earlier than the seam gap itself
		}
		return cut, true
	}
	return 0, false
}

// synthesizeWithOnsetGuard renders one sentence, applying primer-excision
// when the sentence opens with a weak onset. On any primed-path failure
// (synthesis error, empty audio, no clean seam) it falls back to the plain
// un-primed render.
func synthesizeWithOnsetGuard(s Synthesizer, sentence, refName string) (engine.Audio, error) {
	if !HasWeakOnset(sentence) {
		return s.Synthesize(sentence, refName)
	}
	primed, err := s.Synthesize(onsetPrimer+sentence, refName)
	if err == nil && len(primed.Samples) > 0 {
		if cut, ok := FindPrimerSeam(primed.Samples, primed.SampleRate, TrimGate); ok {
			return engine.Audio{Samples: primed.Samples[cut:], SampleRate: primed.SampleRate}, nil
		}
	}
	return s.Synthesize(sentence, refName)
}

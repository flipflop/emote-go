// Number normalization for the embedded engine, run on utterance text before
// sentence splitting (so "3.14" and "1.2.3" are worded before SplitSentences
// can cut them at the dots).
//
// Philosophy (adopted from the Super-Enfabulator-Game's toSpoken, which
// normalized "-N" to "minus N" and nothing else): normalize MINIMALLY,
// converting only what the engine measurably mispronounces. Whisper-verified
// evidence against the sherpa-onnx pocket-tts tokenizer (2026-08-06):
//
//	kept   raw integers   "1, 2, ... 10." clipped "1" to "on"; "100 ... 1000."
//	                      stuttered "300" twice and dropped "1000" entirely
//	kept   minus numbers  "-10 degrees" lost the minus completely
//	kept   decimals       "Pi is 3.14." garbled to "5 is 3. This 3. 14."
//	                      (the dot also hit the sentence splitter)
//	kept   versions       "1.2.3" stuttered ("1. 2. 2. 3.")
//	kept   percent        "%" itself is fine, but "50" wordifies to "fifty"
//	                      and "fifty%" is not viable engine input
//	DROPPED ordinals      "2nd", "3rd", "21st", "103rd" all rendered correctly
//	DROPPED times         "11:41", "9:05" rendered correctly (left verbatim)
package speech

import (
	"regexp"
	"strconv"
	"strings"
)

// minusRe is a verbatim port of the Super-Enfabulator-Game precedent
// (Reference/Super-Enfabulator-Game/game/mission-controller.js toSpoken):
//
//	String(text).replace(/(^|\s)-(\d+)/g, "$1minus $2")
//
// Only a hyphen that starts a number after whitespace (or start-of-text) is a
// minus, so "Fine-Tune" and the range "5-10" are left alone — and so is
// "(-5)", by the same deliberate rule.
var minusRe = regexp.MustCompile(`(^|\s)-(\d+)`)

// NormalizeForSpeech converts the number tokens the engine mispronounces into
// spoken words, leaving everything else byte-for-byte intact. Rules:
//
//   - "-10" → "minus 10" first (toSpoken port above), then the digits wordify.
//   - Integers up to 12 digits (999 billion) become cardinal words
//     ("2026" → "two thousand twenty six" — plain cardinal, no year-style
//     "twenty twenty six"). Longer runs and runs with a leading zero
//     ("007") are spoken digit-by-digit.
//   - A comma is thousands grouping ONLY when the leading group has 1-3
//     digits and every comma is immediately followed by exactly three digits
//     (no more, no space): "1,000" → "one thousand". Anything else — "1, 2",
//     "1,23" — is list punctuation and each number wordifies separately.
//   - "3.14" → "three point one four" (integer part cardinal, fraction
//     digit-by-digit). Two or more dots is a version: "1.2.3" → "one point
//     two point three", every component digit-by-digit.
//   - "50%" → "fifty percent".
//   - Left untouched: digits glued to word characters ("abc123", "file2.txt",
//     and ordinals like "2nd" — engine-verified correct), hex ("0x1F"), and
//     clock times — d{1,2}:dd(:dd)* with hour ≤ 23 and minutes ≤ 59
//     ("11:41" — engine-verified correct). A colon sequence that is not a
//     valid time ("3:1") wordifies around the literal colon.
func NormalizeForSpeech(text string) string {
	text = minusRe.ReplaceAllString(text, "${1}minus ${2}")
	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(text) + len(text)/2)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if !isDigit(c) {
			b.WriteRune(c)
			continue
		}
		if i > 0 && isWordRune(runes[i-1]) {
			// Digits glued to a word/identifier ("abc123", "v1.2"): copy the
			// rest of the alphanumeric-and-inner-dot run verbatim.
			j := verbatimRunEnd(runes, i)
			b.WriteString(string(runes[i:j]))
			i = j - 1
			continue
		}
		spoken, j := spellNumberToken(runes, i)
		b.WriteString(spoken)
		i = j - 1
	}
	return b.String()
}

// spellNumberToken parses the number token starting at the digit runes[i] and
// returns its spoken form plus the index just past the token. When the token
// must stay untouched (hex, time, word-glued) the returned string is the
// verbatim input span.
func spellNumberToken(runes []rune, i int) (string, int) {
	p := digitRunEnd(runes, i)
	intPart := string(runes[i:p])

	// Hex literal: leave the whole 0x... token alone.
	if intPart == "0" && p < len(runes) && (runes[p] == 'x' || runes[p] == 'X') &&
		p+1 < len(runes) && isHexDigit(runes[p+1]) {
		q := p + 1
		for q < len(runes) && isHexDigit(runes[q]) {
			q++
		}
		return string(runes[i:q]), q
	}

	// Clock time (engine-verified correct): leave verbatim.
	if end, ok := timeEnd(runes, i, p); ok {
		return string(runes[i:end]), end
	}

	// Version-like d(.d)+ with 2+ dots: digit-by-digit, dots become "point".
	if comps, end, ok := versionSpan(runes, i, p); ok {
		if end < len(runes) && isWordRune(runes[end]) { // "1.2.3b4" — code-like
			return string(runes[i:verbatimRunEnd(runes, i)]), verbatimRunEnd(runes, i)
		}
		parts := make([]string, len(comps))
		for k, comp := range comps {
			parts[k] = spellDigits(comp)
		}
		return strings.Join(parts, " point "), end
	}

	// Thousands grouping: leading group 1-3 digits, then (",ddd")+ with each
	// group exactly three digits.
	if len(intPart) <= 3 {
		for p+3 < len(runes) && runes[p] == ',' &&
			isDigit(runes[p+1]) && isDigit(runes[p+2]) && isDigit(runes[p+3]) &&
			!(p+4 < len(runes) && isDigit(runes[p+4])) {
			intPart += string(runes[p+1 : p+4])
			p += 4
		}
	}

	// Single decimal point with digits after it.
	frac := ""
	if p+1 < len(runes) && runes[p] == '.' && isDigit(runes[p+1]) {
		q := digitRunEnd(runes, p+1)
		frac = string(runes[p+1 : q])
		p = q
	}

	// Glued to word characters after all that ("123abc", "10s", "2nd"):
	// leave the whole run verbatim. Ordinal suffixes land here on purpose —
	// the engine pronounces "2nd"/"21st"/"103rd" correctly (see doc).
	if p < len(runes) && isWordRune(runes[p]) {
		j := verbatimRunEnd(runes, i)
		return string(runes[i:j]), j
	}

	spoken := spellInteger(intPart)
	if frac != "" {
		spoken += " point " + spellDigits(frac)
	}
	if p < len(runes) && runes[p] == '%' {
		spoken += " percent"
		p++
	}
	return spoken, p
}

// timeEnd reports whether runes[i:] starts a clock time d{1,2}:dd(:dd)* with
// hour ≤ 23 and every following group ≤ 59, returning the index past it.
func timeEnd(runes []rune, i, p int) (int, bool) {
	if p-i > 2 || p >= len(runes) || runes[p] != ':' {
		return 0, false
	}
	if h, _ := strconv.Atoi(string(runes[i:p])); h > 23 {
		return 0, false
	}
	end := p
	for end < len(runes) && runes[end] == ':' {
		if end+2 >= len(runes) || !isDigit(runes[end+1]) || !isDigit(runes[end+2]) {
			return 0, false
		}
		if end+3 < len(runes) && isDigit(runes[end+3]) { // 3+ digit group
			return 0, false
		}
		if mm, _ := strconv.Atoi(string(runes[end+1 : end+3])); mm > 59 {
			return 0, false
		}
		end += 3
	}
	return end, true
}

// versionSpan matches d+(.d+){2,} (two or more dots) starting at i, returning
// the digit components and the index past the match.
func versionSpan(runes []rune, i, p int) ([]string, int, bool) {
	comps := []string{string(runes[i:p])}
	end := p
	for end+1 < len(runes) && runes[end] == '.' && isDigit(runes[end+1]) {
		q := digitRunEnd(runes, end+1)
		comps = append(comps, string(runes[end+1:q]))
		end = q
	}
	if len(comps) < 3 {
		return nil, 0, false
	}
	return comps, end, true
}

// verbatimRunEnd returns the index past the run of word runes starting at i,
// allowing dots that sit between two word runes ("file2.txt", "1.2.3b4").
func verbatimRunEnd(runes []rune, i int) int {
	j := i
	for j < len(runes) {
		if isWordRune(runes[j]) {
			j++
		} else if runes[j] == '.' && j+1 < len(runes) && isWordRune(runes[j+1]) {
			j++
		} else {
			break
		}
	}
	return j
}

func digitRunEnd(runes []rune, i int) int {
	for i < len(runes) && isDigit(runes[i]) {
		i++
	}
	return i
}

func isDigit(c rune) bool { return c >= '0' && c <= '9' }

func isWordRune(c rune) bool {
	return c == '_' || isDigit(c) ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		c > 127 // non-ASCII letters count as word-glue (leave "café2" alone)
}

func isHexDigit(c rune) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

var onesWords = [...]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen",
	"sixteen", "seventeen", "eighteen", "nineteen",
}

var tensWords = [...]string{
	"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy",
	"eighty", "ninety",
}

// spellInteger words a digit string: cardinal up to 12 digits (999 billion),
// digit-by-digit beyond that scope or when a multi-digit run has a leading
// zero ("007" → "zero zero seven").
func spellInteger(digits string) string {
	if len(digits) > 12 || (len(digits) > 1 && digits[0] == '0') {
		return spellDigits(digits)
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return spellDigits(digits) // unreachable at ≤ 12 digits; belt-and-braces
	}
	return spellCardinal(n)
}

func spellCardinal(n uint64) string {
	if n == 0 {
		return "zero"
	}
	scales := []struct {
		div  uint64
		name string
	}{{1e9, "billion"}, {1e6, "million"}, {1e3, "thousand"}, {1, ""}}
	var parts []string
	for _, s := range scales {
		if g := n / s.div; g > 0 {
			parts = append(parts, spellUnder1000(int(g)))
			if s.name != "" {
				parts = append(parts, s.name)
			}
			n %= s.div
		}
	}
	return strings.Join(parts, " ")
}

func spellUnder1000(n int) string {
	var parts []string
	if n >= 100 {
		parts = append(parts, onesWords[n/100], "hundred")
		n %= 100
	}
	switch {
	case n == 0:
	case n < 20:
		parts = append(parts, onesWords[n])
	default:
		if n%10 == 0 {
			parts = append(parts, tensWords[n/10])
		} else {
			parts = append(parts, tensWords[n/10]+" "+onesWords[n%10])
		}
	}
	return strings.Join(parts, " ")
}

// spellDigits words a digit string one digit at a time ("107" → "one zero
// seven").
func spellDigits(digits string) string {
	parts := make([]string, 0, len(digits))
	for _, c := range digits {
		parts = append(parts, onesWords[c-'0'])
	}
	return strings.Join(parts, " ")
}

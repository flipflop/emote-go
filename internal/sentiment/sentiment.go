// Package sentiment ports emote_cli/sentiment.py: a deterministic lexicon
// classifier mapping text -> (emotion, reason).
//
// Emotions: neutral | happy | sad | calm | excited
//
// Case-insensitive, word-boundary lexicon with weights; the strongest total
// wins; ties -> neutral. Multiword phrases are matched first and consume
// their span so contained single words are not double-counted. If the happy
// family wins and an explicit excited phrase matched (or happy score >= 3),
// the result escalates from happy to excited.
//
// All lexicon patterns are RE2-compatible as written in the Python reference
// (only \b word boundaries and non-capturing groups); no restructuring was
// required in this package.
package sentiment

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type entry struct {
	emotion string
	label   string
	rx      *regexp.Regexp
	weight  int
	excited bool
}

func mk(emotion, label, pattern string, weight int, excited bool) entry {
	return entry{emotion, label, regexp.MustCompile(`(?i)` + pattern), weight, excited}
}

func word(emotion, w string, weight int) entry {
	return mk(emotion, w, `\b`+w+`\b`, weight, false)
}

// lexicon is ordered: phrases (and excited terms) first so they consume their
// span before the single words inside them are tried.
var lexicon = buildLexicon()

func buildLexicon() []entry {
	var lex []entry

	// --- excited (explicit phrases; scored into the happy family, weight 3) ---
	for _, e := range []struct{ label, pattern string }{
		{"all tests pass", `\ball\s+(?:\d+\s+)?tests?\s+(?:pass|passed|passing|green)\b`},
		{"all green", `\ball\s+green\b`},
		{"ship", `\b(?:ship|ships|shipped|shipping)\b`},
		{"huge", `\bhuge\b`},
		{"breakthrough", `\bbreakthroughs?\b`},
		{"amazing", `\bamazing\b`},
		{"incredible", `\bincredible\b`},
		{"fantastic", `\bfantastic\b`},
		{"awesome", `\bawesome\b`},
		{"🎉", "🎉"},
		{"🚀", "🚀"},
		{"🥳", "🥳"},
		{"✨", "✨"},
	} {
		lex = append(lex, mk("happy", e.label, e.pattern, 3, true))
	}

	// --- sad phrases / strong evidence ---
	lex = append(lex, mk("sad", "tests failed", `\btests?\s+(?:fail|fails|failed|failing)\b`, 2, false))
	lex = append(lex, word("sad", "traceback", 2))

	// --- calm phrases ---
	for _, e := range []struct {
		label, pattern string
		weight         int
	}{
		{"here's how", `\bhere[’']?s\s+how\b|\bhere\s+is\s+how\b`, 2},
		{"let me explain", `\blet\s+me\s+explain\b`, 2},
		{"let's walk through", `\blet[’']?s\s+walk\s+through\b`, 2},
		{"walk through", `\bwalk(?:ing)?\s+through\b`, 1},
		{"step by step", `\bstep\s+by\s+step\b`, 2},
		{"in other words", `\bin\s+other\s+words\b`, 1},
		{"would you like", `\bwould\s+you\s+like\b`, 1},
		{"do you want", `\bdo\s+you\s+want\b`, 1},
		{"should I", `\bshould\s+i\b`, 1},
		{"shall I", `\bshall\s+i\b`, 1},
		{"next steps", `\bnext\s+steps?\b`, 1},
	} {
		lex = append(lex, mk("calm", e.label, e.pattern, e.weight, false))
	}

	// --- single words ---
	for _, w := range []string{"fail", "fails", "failed", "failing", "failure", "failures",
		"error", "errors", "exception", "exceptions", "broken",
		"crash", "crashes", "crashed", "regression", "regressions",
		"denied", "unable"} {
		lex = append(lex, word("sad", w, 1))
	}

	lex = append(lex, word("happy", "fixed", 2))
	lex = append(lex, word("happy", "resolved", 2))
	for _, w := range []string{"pass", "passes", "passed", "passing", "success", "successful",
		"successfully", "works", "working", "complete", "completed",
		"deployed", "improved", "improvement", "improvements"} {
		lex = append(lex, word("happy", w, 1))
	}

	lex = append(lex, word("calm", "because", 1))
	lex = append(lex, mk("calm", "plan", `\bplan(?:s|ned|ning)?\b`, 1, false))

	return lex
}

func unique(seq []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range seq {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

// Classify maps text to (emotion, reason). Deterministic.
func Classify(text string) (string, string) {
	if strings.TrimSpace(text) == "" {
		return "neutral", "empty text"
	}

	scores := map[string]int{"sad": 0, "happy": 0, "calm": 0}
	matched := map[string][]string{}
	excitedHit := false
	consumed := make([]bool, len(text))

	for _, e := range lexicon {
		for _, span := range e.rx.FindAllStringIndex(text, -1) {
			claimed := false
			for k := span[0]; k < span[1]; k++ {
				if consumed[k] {
					claimed = true
					break
				}
			}
			if claimed {
				continue // already claimed by a longer phrase
			}
			for k := span[0]; k < span[1]; k++ {
				consumed[k] = true
			}
			scores[e.emotion] += e.weight
			matched[e.emotion] = append(matched[e.emotion], e.label)
			if e.excited {
				excitedHit = true
			}
		}
	}

	// Question-to-the-user cue: counted once.
	if strings.Contains(text, "?") {
		scores["calm"]++
		matched["calm"] = append(matched["calm"], "question")
	}

	best := 0
	for _, s := range scores {
		if s > best {
			best = s
		}
	}
	if best == 0 {
		return "neutral", "no lexicon matches"
	}

	var winners []string
	for e, s := range scores {
		if s == best {
			winners = append(winners, e)
		}
	}
	sort.Strings(winners)
	if len(winners) > 1 {
		parts := make([]string, 0, len(winners))
		for _, e := range winners {
			parts = append(parts, fmt.Sprintf("%s %d", e, scores[e]))
		}
		return "neutral", "tie: " + strings.Join(parts, " vs ")
	}

	winner := winners[0]
	labels := unique(matched[winner])
	if len(labels) > 4 {
		labels = labels[:4]
	}
	reason := "matched: " + strings.Join(labels, ", ")

	if winner == "happy" {
		if excitedHit {
			return "excited", reason
		}
		if scores["happy"] >= 3 {
			return "excited", fmt.Sprintf("%s (happy score %d)", reason, scores["happy"])
		}
	}

	return winner, reason
}

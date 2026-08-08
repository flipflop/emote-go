package extract_test

// Golden parity: every fixture in testdata/fixtures is replayed through the
// Go extract + sentiment ports and compared byte-for-byte against the output
// recorded from the Python reference:
//
//	cd ../roz-emote-cli && ./emote --quiet --mode <mode> < fixture
//
// quietLines replicates the Python speak path exactly: stdin text stripped
// (main.py), per-utterance auto emotion (main.py _cmd_speak), adjacent
// same-emotion utterances merged with sentence-final punctuation added
// (tts.merge_runs), printed as "[emotion] text" lines (tts.speak quiet).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/extract"
	"github.com/flipflop/emote-go/internal/sentiment"
)

const sentenceFinal = ".!?…:;,"

func lastRuneOf(s string) rune {
	r := rune(0)
	for _, c := range s {
		r = c
	}
	return r
}

// quietLines mirrors emote --quiet --mode <mode> for the default config
// (emotion auto, max_sentences 3).
func quietLines(t *testing.T, text, mode string) string {
	t.Helper()
	utts := extract.Extract(strings.TrimSpace(text), mode, 3)

	type run struct{ emotion, text string }
	var runs []run
	for _, utt := range utts {
		emotion, _ := sentiment.Classify(utt.Text) // per-utterance, pre-merge
		if emotion == "" {
			emotion = "neutral"
		}
		txt := strings.TrimSpace(utt.Text)
		if txt == "" {
			continue
		}
		if !strings.ContainsRune(sentenceFinal, lastRuneOf(txt)) {
			txt += "."
		}
		if len(runs) > 0 && runs[len(runs)-1].emotion == emotion {
			runs[len(runs)-1].text += " " + txt
		} else {
			runs = append(runs, run{emotion, txt})
		}
	}

	var out strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&out, "[%s] %s\n", r.emotion, r.text)
	}
	return out.String()
}

func TestGoldenParity(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("..", "..", "testdata", "fixtures", "*.txt"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	total, matched := 0, 0
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".txt")
		raw, err := os.ReadFile(fx)
		if err != nil {
			t.Fatalf("read fixture %s: %v", fx, err)
		}
		for _, mode := range extract.Modes {
			total++
			goldenPath := filepath.Join("..", "..", "testdata", "goldens",
				name+"."+mode+".txt")
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Errorf("%s/%s: missing golden: %v", name, mode, err)
				continue
			}
			got := quietLines(t, string(raw), mode)
			if got != string(want) {
				t.Errorf("%s/%s: parity mismatch\n--- python golden ---\n%s\n--- go ---\n%s",
					name, mode, want, got)
				continue
			}
			matched++
		}
	}
	t.Logf("extract+sentiment quiet parity: %d/%d goldens matched", matched, total)
}

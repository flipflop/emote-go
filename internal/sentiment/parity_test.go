package sentiment_test

// Golden parity: sentiment.Classify over each whole fixture text must match
// the (emotion, reason) recorded from the Python reference
// (emote_cli.sentiment.classify), stored as "<emotion>\n<reason>\n".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/sentiment"
)

func TestSentimentGoldenParity(t *testing.T) {
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
		goldenPath := filepath.Join("..", "..", "testdata", "goldens",
			name+".sentiment.txt")
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Errorf("%s: missing sentiment golden: %v", name, err)
			continue
		}
		total++
		emotion, reason := sentiment.Classify(strings.TrimSpace(string(raw)))
		got := emotion + "\n" + reason + "\n"
		if got != string(want) {
			t.Errorf("%s: sentiment parity mismatch\n--- python ---\n%s--- go ---\n%s",
				name, want, got)
			continue
		}
		matched++
	}
	t.Logf("sentiment parity: %d/%d goldens matched", matched, total)
}

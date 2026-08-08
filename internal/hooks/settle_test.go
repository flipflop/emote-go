package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForTranscriptSettle(t *testing.T) {
	old := settlePoll
	settlePoll = 10 * time.Millisecond
	defer func() { settlePoll = old }()

	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grow the file shortly after settle starts; it must pick up the growth
	// and still return (bounded), with the final size observed stable.
	go func() {
		time.Sleep(15 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString("line2-the-real-final-message\n")
		f.Close()
	}()
	start := time.Now()
	waitForTranscriptSettle(path)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("settle took too long")
	}
	st, _ := os.Stat(path)
	if st.Size() != int64(len("line1\n")+len("line2-the-real-final-message\n")) {
		t.Fatalf("settle returned before growth was observed")
	}
}

func TestWaitForTranscriptSettleMissingFile(t *testing.T) {
	waitForTranscriptSettle(filepath.Join(t.TempDir(), "absent.jsonl")) // must not hang or panic
}

// Command cmd_selftest renders the P1 A/B sentence through the full
// engine + speech pipeline as bright-jane and reports timings against the
// P1-REPORT budgets (first audio < 0.5 s, RTF ~0.22-0.29).
//
// Usage:
//
//	go build -o /tmp/emote-selftest ./internal/engine/cmd_selftest
//	codesign -f -s - /tmp/emote-selftest   # Darwin 25 + Go 1.21 quirk
//	/tmp/emote-selftest [--root DIR] [--out FILE.wav] [--emotion NAME] [--text "..."]
//
// --root defaults to the current directory if it holds models/ + refs/
// (the source checkout), else ~/.cache/emote.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flipflop/emote-go/internal/engine"
	"github.com/flipflop/emote-go/internal/provision"
	"github.com/flipflop/emote-go/internal/speech"
)

const abSentence = "Hello! This is jane speaking. The quick brown fox jumps over the lazy dog."

// timingSynth adapts the engine for the pipeline while capturing the moment
// the very first decoder chunk arrives (the honest "first audio" number).
type timingSynth struct {
	e          *engine.Engine
	start      time.Time
	firstChunk time.Duration
}

func (t *timingSynth) Synthesize(text, refName string) (engine.Audio, error) {
	return t.e.SynthesizeStream(text, refName, func([]float32) bool {
		if t.firstChunk == 0 {
			t.firstChunk = time.Since(t.start)
		}
		return true
	})
}

func main() {
	root := flag.String("root", "", "cache root containing models/ and refs/ (default: cwd if it qualifies, else ~/.cache/emote)")
	out := flag.String("out", "", "write the rendered wav here (optional)")
	emotion := flag.String("emotion", "bright", "emotion label (bright/neutral/calm/happy/sad/excited)")
	text := flag.String("text", abSentence, "text to render")
	flag.Parse()

	if *root == "" {
		if cwd, err := os.Getwd(); err == nil && dirExists(filepath.Join(cwd, "models")) && dirExists(filepath.Join(cwd, "refs")) {
			*root = cwd
		} else {
			*root = provision.CacheDir()
		}
	}

	t0 := time.Now()
	e, err := engine.New(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "selftest:", err)
		os.Exit(1)
	}
	defer e.Close()
	loadDur := time.Since(t0)
	fmt.Printf("model dir:    %s\n", e.ModelDir())
	fmt.Printf("model load:   %6.2f s\n", loadDur.Seconds())

	ts := &timingSynth{e: e, start: time.Now()}
	ref := speech.RefForEmotion(*emotion)
	nSeg := 0
	audio, err := speech.RenderRef(ts, *text, ref, func(seg speech.Segment, sr int) {
		nSeg++
		fmt.Printf("  seg %d %-40q ready at %5.2f s (%5.2f s audio)\n",
			nSeg, seg.Text, time.Since(ts.start).Seconds(), float64(len(seg.Samples))/float64(sr))
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "selftest:", err)
		os.Exit(1)
	}
	genDur := time.Since(ts.start)

	fmt.Printf("ref voice:    %s (emotion %q)\n", ref, *emotion)
	fmt.Printf("first audio:  %6.2f s   (budget < 0.5 s, P1.7: 0.20-0.35 s)\n", ts.firstChunk.Seconds())
	fmt.Printf("total gen:    %6.2f s\n", genDur.Seconds())
	fmt.Printf("audio out:    %6.2f s @ %d Hz\n", audio.Duration(), audio.SampleRate)
	fmt.Printf("RTF:          %6.2f    (P1.7: 0.24-0.29)\n", genDur.Seconds()/audio.Duration())

	if *out != "" {
		if err := speech.WriteWAV(*out, audio); err != nil {
			fmt.Fprintln(os.Stderr, "selftest:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote:        %s\n", *out)
	}
	if ts.firstChunk.Seconds() >= 0.5 {
		fmt.Fprintln(os.Stderr, "selftest: FAIL — first audio exceeded the 0.5 s budget")
		os.Exit(1)
	}
	fmt.Println("selftest: OK")
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

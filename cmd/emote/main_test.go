package main

import (
	"reflect"
	"testing"
)

func TestMergeRuns(t *testing.T) {
	// Port of the Python tts.merge_runs semantics: trim, add terminal
	// punctuation, default neutral, merge adjacent same-emotion runs.
	items := []spoken{
		{Text: "  Build passed ", Emotion: "happy"},
		{Text: "Everything is green", Emotion: "happy"},
		{Text: "But one warning remains…", Emotion: "sad"},
		{Text: "   ", Emotion: "happy"}, // dropped
		{Text: "Next steps below:"},     // no emotion -> neutral, ':' kept
	}
	got := mergeRuns(items)
	want := []ttsRun{
		{"happy", "Build passed. Everything is green."},
		{"sad", "But one warning remains…"},
		{"neutral", "Next steps below:"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeRuns:\n got %#v\nwant %#v", got, want)
	}
}

func TestMergeRunsEmpty(t *testing.T) {
	if runs := mergeRuns([]spoken{{Text: "  "}}); len(runs) != 0 {
		t.Errorf("blank utterances must yield no runs, got %#v", runs)
	}
}

func TestParseSpeakArgsInterleaved(t *testing.T) {
	opts, code, ok := parseSpeakArgs([]string{"All", "tests", "passed!", "--quiet", "--mode", "verbose"})
	if !ok || code != 0 {
		t.Fatalf("parse failed: code=%d ok=%v", code, ok)
	}
	if !opts.quiet || opts.mode != "verbose" {
		t.Errorf("flags after positionals not parsed: %+v", opts)
	}
	if got := len(opts.text); got != 3 {
		t.Errorf("expected 3 text words, got %d", got)
	}
}

func TestParseSpeakArgsEquals(t *testing.T) {
	opts, _, ok := parseSpeakArgs([]string{"--emotion=sad", "--voice=jane", "hi"})
	if !ok || opts.emotion != "sad" || opts.voice != "jane" || !opts.voiceSet {
		t.Errorf("--flag=value form not parsed: %+v", opts)
	}
}

func TestParseSpeakArgsBadChoice(t *testing.T) {
	_, code, ok := parseSpeakArgs([]string{"--mode", "bogus"})
	if ok || code != 1 {
		t.Errorf("invalid choice must be usage error 1, got code=%d ok=%v", code, ok)
	}
	_, code, ok = parseSpeakArgs([]string{"--frobnicate"})
	if ok || code != 1 {
		t.Errorf("unknown flag must be usage error 1, got code=%d ok=%v", code, ok)
	}
}

func TestParseSpeakArgsDoubleDash(t *testing.T) {
	opts, _, ok := parseSpeakArgs([]string{"--", "--quiet", "literal"})
	if !ok || opts.quiet {
		t.Fatalf("args after -- must be positional: %+v", opts)
	}
	if len(opts.text) != 2 || opts.text[0] != "--quiet" {
		t.Errorf("-- handling wrong: %+v", opts.text)
	}
}

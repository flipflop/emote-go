package sentiment_test

// Port of ../roz-emote-cli/tests/test_sentiment.py — table-driven.

import (
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/sentiment"
)

var emotions = map[string]bool{
	"neutral": true, "happy": true, "sad": true, "calm": true, "excited": true,
}

// (text, expected emotion, substring the reason must contain)
var classifyCases = []struct {
	text, expected, reasonPart string
}{
	// --- sad ---
	{"The build failed with a traceback", "sad", "traceback"},
	{"Two tests failed and one error remains", "sad", "tests failed"},
	{"Access denied: unable to write the file", "sad", "denied"},
	{"Everything is broken after the crash", "sad", "broken"},
	{"This introduced a regression and an exception", "sad", "regression"},
	// --- happy ---
	{"Fixed the failing test", "happy", "fixed"},
	{"The change works and the run completed", "happy", "works"},
	{"Deployment is complete", "happy", "complete"},
	{"The patch improved startup time", "happy", "improved"},
	{"FIXED THE TYPO", "happy", "fixed"}, // case-insensitive
	// --- excited: explicit phrases ---
	{"All tests pass!", "excited", "all tests pass"},
	{"All 47 tests passed, shipping it now", "excited", "all tests pass"},
	{"The dashboard is all green", "excited", "all green"},
	{"Huge breakthrough on the parser", "excited", "huge"},
	{"\U0001f389 Deployed to production", "excited", "\U0001f389"},
	// --- excited: escalation on happy score >= 3 ---
	{"Fixed the auth bug, resolved the flaky test, and the suite passed",
		"excited", "happy score"},
	// --- calm ---
	{"Here's how the loader works: because the file is parsed at " +
		"startup, changes need a restart", "calm", "here's how"},
	{"Let me explain the plan step by step", "calm", "let me explain"},
	{"Would you like me to refactor this?", "calm", "would you like"},
	{"The plan: refactor the client, then re-run the linter", "calm", "plan"},
	// --- neutral ---
	{"The file contains twelve entries", "neutral", "no lexicon matches"},
	{"The test passed but the build failed", "neutral", "tie"},
	// word boundaries: compass/passport/membership must not match
	{"Use a compass at the passport office to renew a membership",
		"neutral", "no lexicon matches"},
}

func TestClassifyTable(t *testing.T) {
	for _, c := range classifyCases {
		emotion, reason := sentiment.Classify(c.text)
		if emotion != c.expected {
			t.Errorf("Classify(%q) = %q, want %q (reason %q)",
				c.text, emotion, c.expected, reason)
			continue
		}
		if !strings.Contains(strings.ToLower(reason), strings.ToLower(c.reasonPart)) {
			t.Errorf("Classify(%q) reason %q missing %q", c.text, reason, c.reasonPart)
		}
	}
}

func TestContract(t *testing.T) {
	for _, text := range []string{"", "hello", "all tests pass", "it failed"} {
		emotion, reason := sentiment.Classify(text)
		if !emotions[emotion] {
			t.Errorf("Classify(%q) emotion %q not in the emotion set", text, emotion)
		}
		if reason == "" {
			t.Errorf("Classify(%q) empty reason", text)
		}
	}
	if e, _ := sentiment.Classify(""); e != "neutral" {
		t.Errorf("empty text = %q", e)
	}
	if e, _ := sentiment.Classify("   \n\t "); e != "neutral" {
		t.Errorf("whitespace text = %q", e)
	}
}

func TestDeterministic(t *testing.T) {
	for _, c := range classifyCases {
		e1, r1 := sentiment.Classify(c.text)
		e2, r2 := sentiment.Classify(c.text)
		if e1 != e2 || r1 != r2 {
			t.Errorf("Classify(%q) not deterministic: (%q,%q) vs (%q,%q)",
				c.text, e1, r1, e2, r2)
		}
	}
}

func TestReasonFormat(t *testing.T) {
	emotion, reason := sentiment.Classify("The deploy failed")
	if emotion != "sad" {
		t.Fatalf("emotion = %q", emotion)
	}
	if !strings.HasPrefix(reason, "matched: ") {
		t.Errorf("reason %q lacks 'matched: ' prefix", reason)
	}
	if !strings.Contains(reason, "failed") {
		t.Errorf("reason %q missing 'failed'", reason)
	}
}

func TestWeighting(t *testing.T) {
	// Key documented judgment call: "fixed" (weight 2) must beat the
	// mentioned problem word "failing" (weight 1) -> happy, not sad,
	// and score 2 stays below the excited threshold of 3.
	emotion, reason := sentiment.Classify("Fixed the failing test")
	if emotion != "happy" || !strings.Contains(reason, "fixed") {
		t.Errorf("resolution verb: got (%q, %q)", emotion, reason)
	}

	if e, _ := sentiment.Classify("Resolved the error in the parser"); e != "happy" {
		t.Errorf("resolved beats error: got %q", e)
	}
	if e, _ := sentiment.Classify("Unable to fix the error"); e != "sad" {
		t.Errorf("unresolved problem: got %q", e)
	}

	// "tests failed" (weight 2) masks "failed"; "error" adds 1 -> sad 3.
	// Reason must not double-report "failed" on its own.
	emotion, reason = sentiment.Classify("Two tests failed and one error remains")
	if emotion != "sad" {
		t.Fatalf("phrase consumption: got %q", emotion)
	}
	terms := strings.Split(strings.TrimPrefix(reason, "matched: "), ",")
	seen := map[string]bool{}
	for _, term := range terms {
		seen[strings.TrimSpace(term)] = true
	}
	if !seen["tests failed"] {
		t.Errorf("reason %q missing 'tests failed'", reason)
	}
	if seen["failed"] {
		t.Errorf("reason %q double-reports 'failed'", reason)
	}

	if e, _ := sentiment.Classify("The fix works but the tests failed again"); e != "sad" {
		t.Errorf("strong failure evidence: got %q", e) // sad 2 vs happy 1
	}
	// Explicit excited phrase (3) vs heavy failure evidence (4): sad wins.
	if e, _ := sentiment.Classify("All tests pass locally, but the deploy failed " +
		"with a traceback and everything is broken"); e != "sad" {
		t.Errorf("excited needs happy to win: got %q", e)
	}
	if e, _ := sentiment.Classify("The run completed and it works"); e != "happy" {
		t.Errorf("happy below threshold: got %q", e) // score 2 < 3
	}
	if e, _ := sentiment.Classify("The shipment of membership cards"); e != "neutral" {
		t.Errorf("ship word boundary: got %q", e)
	}
	emotion, reason = sentiment.Classify("Shipped the new release")
	if emotion != "excited" || !strings.Contains(reason, "ship") {
		t.Errorf("shipped: got (%q, %q)", emotion, reason)
	}
	emotion, reason = sentiment.Classify("Do we need the second cache?")
	if emotion != "calm" || !strings.Contains(reason, "question") {
		t.Errorf("question cue: got (%q, %q)", emotion, reason)
	}
	if e, _ := sentiment.Classify("Here's how the retry logic works under load"); e != "calm" {
		t.Errorf("calm explanation: got %q", e) // "here's how" 2 vs "works" 1
	}
	// Closing line of a typical Claude reply: the explicit excited phrase (3)
	// beats the question cues (should I 1 + "?" 1 = 2).
	if e, _ := sentiment.Classify("All 47 tests passed. Should I commit the changes?"); e != "excited" {
		t.Errorf("success question: got %q", e)
	}
}

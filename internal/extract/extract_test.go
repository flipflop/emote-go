package extract_test

// Port of ../roz-emote-cli/tests/test_extract.py — table-driven.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/extract"
)

// A realistic Claude Code response: heading, prose with inline code / a file
// path / a URL, a fenced Python block, a bullet list, a markdown table, a
// warning line, and a closing success + question paragraph.
const fixture = `## Fix applied

I updated ` + "`emote_cli/tts.py`" + ` to retry the request. Details at https://docs.python.org/3/library/urllib.html today.

` + "```python" + `
def speak(text):
    resp = post(url, data)
    return resp
` + "```" + `

Key changes:
- Added a retry loop
- Removed the ` + "`time.sleep(5)`" + ` call

| File | Status |
| ---- | ------ |
| tts.py | updated |
| main.py | unchanged |

Warning: the server must be restarted.

All 47 tests passed. Should I commit the changes?
`

const (
	lead    = "I updated tts.py to retry the request. Details at docs.python.org today."
	bullets = "Key changes: Added a retry loop. Removed the time.sleep(5) call."
	warning = "Warning: the server must be restarted."
	closing = "All 47 tests passed. Should I commit the changes?"
)

// ANSI-colored CLI output, as pasted from a terminal.
const ansiFixture = "\x1b[33mwarning:\x1b[0m deprecated API used\n" +
	"\n" +
	"\x1b[1m\x1b[32m\u2713 All 12 checks passed\x1b[0m\n"

const tracebackFixture = `The command crashed:

Traceback (most recent call last):
  File "/Users/dev/proj/app.py", line 10, in <module>
    main()
ZeroDivisionError: division by zero
`

func texts(utts []extract.Utterance) []string {
	out := make([]string, len(utts))
	for i, u := range utts {
		out[i] = u.Text
	}
	return out
}

func kinds(utts []extract.Utterance) []string {
	out := make([]string, len(utts))
	for i, u := range utts {
		out[i] = u.Kind
	}
	return out
}

// --- preprocessing ---------------------------------------------------------

func TestANSIStrippedEverywhere(t *testing.T) {
	for _, u := range extract.Extract(ansiFixture, "verbose", 3) {
		if strings.Contains(u.Text, "\x1b") {
			t.Errorf("ANSI escape survived in %q", u.Text)
		}
	}
}

func TestANSIFixtureKinds(t *testing.T) {
	utts := extract.Extract(ansiFixture, "verbose", 3)
	if got := kinds(utts); !reflect.DeepEqual(got, []string{"warning", "success"}) {
		t.Fatalf("kinds = %v", got)
	}
	if utts[0].Text != "warning: deprecated API used" {
		t.Errorf("utts[0] = %q", utts[0].Text)
	}
	if utts[1].Text != "\u2713 All 12 checks passed" {
		t.Errorf("utts[1] = %q", utts[1].Text)
	}
}

func TestInlineCleanup(t *testing.T) {
	cases := []struct{ in, want string }{
		{"run `npm test` now", "run npm test now"},
		{"This is **bold** and *emphasis* and _quiet_.",
			"This is bold and emphasis and quiet."},
		{"See [the docs](https://example.com/page) here.", "See the docs here."},
		{"POST to http://localhost:8123/api/tts with JSON.",
			"POST to localhost with JSON."},
		{"Edit tests/test_extract.py and ~/.config/emote/config.json now.",
			"Edit test_extract.py and config.json now."},
		{"Choose one and/or both, open 24/7.", "Choose one and/or both, open 24/7."},
	}
	for _, c := range cases {
		utts := extract.Extract(c.in, "verbose", 3)
		if len(utts) != 1 || utts[0].Text != c.want {
			t.Errorf("Extract(%q) = %v, want one utterance %q", c.in, utts, c.want)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	for _, mode := range extract.Modes {
		for _, in := range []string{"", "   \n\n"} {
			if got := extract.Extract(in, mode, 3); len(got) != 0 {
				t.Errorf("Extract(%q, %s) = %v, want empty", in, mode, got)
			}
		}
	}
}

func TestUnknownModePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Extract with unknown mode did not panic")
		}
	}()
	extract.Extract("hello", "bogus", 3)
}

func TestUtteranceShape(t *testing.T) {
	valid := map[string]bool{"prose": true, "heading": true, "error": true,
		"warning": true, "success": true, "question": true, "code_summary": true}
	for _, mode := range extract.Modes {
		for _, u := range extract.Extract(fixture, mode, 3) {
			if !valid[u.Kind] {
				t.Errorf("mode %s: invalid kind %q", mode, u.Kind)
			}
		}
	}
}

// --- code summaries --------------------------------------------------------

func TestCodeSummaryLanguageAndLineCount(t *testing.T) {
	cases := []struct{ in, want string }{
		{"```python\na = 1\nb = 2\nc = 3\n```", "a Python block, 3 lines"},
		{"```js\nconst x = 1;\n```", "a JavaScript block, 1 line"},
		{"```\nfoo\nbar\n```", "a code block, 2 lines"},
		{"```elm\nmain = text\n```", "a elm block, 1 line"},
	}
	for _, c := range cases {
		utts := extract.Extract(c.in, "verbose", 3)
		want := []extract.Utterance{{Text: c.want, Kind: "code_summary"}}
		if !reflect.DeepEqual(utts, want) {
			t.Errorf("Extract(%q) = %v, want %v", c.in, utts, want)
		}
	}
}

func TestUnterminatedFenceIsCode(t *testing.T) {
	utts := extract.Extract("intro\n\n```python\nx = 1\ny = 2", "verbose", 3)
	if utts[0].Kind != "prose" {
		t.Errorf("utts[0].Kind = %q", utts[0].Kind)
	}
	want := extract.Utterance{Text: "a Python block, 2 lines", Kind: "code_summary"}
	if utts[1] != want {
		t.Errorf("utts[1] = %v, want %v", utts[1], want)
	}
}

// --- tables ----------------------------------------------------------------

func TestTables(t *testing.T) {
	utts := extract.Extract(
		"| a | b |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |\n| 5 | 6 |", "verbose", 3)
	want := []extract.Utterance{{Text: "a table with 3 rows", Kind: "prose"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("got %v, want %v", utts, want)
	}
	utts = extract.Extract("| a | b |\n| --- | --- |\n| 1 | 2 |", "verbose", 3)
	if utts[0].Text != "a table with 1 row" {
		t.Errorf("single row table = %q", utts[0].Text)
	}
}

// --- verbose ---------------------------------------------------------------

func TestVerboseFixtureFullSequence(t *testing.T) {
	utts := extract.Extract(fixture, "verbose", 3)
	want := []extract.Utterance{
		{Text: "Fix applied", Kind: "heading"},
		{Text: lead, Kind: "prose"},
		{Text: "a Python block, 3 lines", Kind: "code_summary"},
		{Text: bullets, Kind: "prose"},
		{Text: "a table with 2 rows", Kind: "prose"},
		{Text: warning, Kind: "warning"},
		{Text: closing, Kind: "question"},
	}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("verbose sequence:\n got %v\nwant %v", utts, want)
	}
}

// --- kind classification ---------------------------------------------------

func TestKindClassification(t *testing.T) {
	cases := []struct{ text, kind string }{
		{"Traceback (most recent call last): boom", "error"},
		{"2 tests failed in the suite.", "error"},
		{"The build is broken.", "error"},
		{"Warning: this API is deprecated.", "warning"},
		{"All 47 tests passed.", "success"},
		{"Deployed successfully.", "success"},
		{"Would you like me to continue?", "question"},
		{"The config lives in one file.", "prose"},
		// precedence: mentions of failure beat success wording
		{"Fixed 3 tests but 1 still failed.", "error"},
		// precedence: a trailing question beats success wording
		{"Everything passed, shall we merge?", "question"},
	}
	for _, c := range cases {
		utts := extract.Extract(c.text, "verbose", 3)
		if len(utts) != 1 || utts[0].Kind != c.kind {
			t.Errorf("kind of %q = %v, want %s", c.text, utts, c.kind)
		}
	}
}

// --- summarised ------------------------------------------------------------

func TestSummarisedFixtureLeadPlusFinal(t *testing.T) {
	utts := extract.Extract(fixture, "summarised", 3)
	want := []extract.Utterance{
		{Text: lead, Kind: "prose"},
		{Text: "All 47 tests passed.", Kind: "question"},
	}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("got %v, want %v", utts, want)
	}
}

func TestSummarisedCaps(t *testing.T) {
	text := "One. Two. Three. Four. Five."
	utts := extract.Extract(text, "summarised", 2)
	want := []extract.Utterance{{Text: "One. Two.", Kind: "prose"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("cap 2: got %v, want %v", utts, want)
	}
	utts = extract.Extract(text, "summarised", 3)
	if utts[0].Text != "One. Two. Three." {
		t.Errorf("default cap: got %q", utts[0].Text)
	}
}

func TestSummarisedMultipleFinalParagraphsFitBudget(t *testing.T) {
	text := "Lead sentence.\n\nMiddle one.\n\nSecond last.\n\nLast one."
	utts := extract.Extract(text, "summarised", 3)
	// lead + the two final paragraphs fit in 3 sentences; "Middle one."
	// does not make the cut.
	want := []string{"Lead sentence.", "Second last.", "Last one."}
	if got := texts(utts); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarisedSkipsCodeSummaryWhenProseExists(t *testing.T) {
	for _, u := range extract.Extract(fixture, "summarised", 3) {
		if u.Kind == "code_summary" {
			t.Errorf("unexpected code_summary: %v", u)
		}
	}
}

func TestSummarisedCodeSummaryWhenNothingElse(t *testing.T) {
	utts := extract.Extract("```python\nx = 1\n```", "summarised", 3)
	want := []extract.Utterance{{Text: "a Python block, 1 line", Kind: "code_summary"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("got %v, want %v", utts, want)
	}
}

func TestSummarisedHeadingsOnlyWhenNothingElseQualifies(t *testing.T) {
	utts := extract.Extract("# Title\n\n## Section", "summarised", 3)
	if got := kinds(utts); !reflect.DeepEqual(got, []string{"heading", "heading"}) {
		t.Fatalf("kinds = %v", got)
	}
	if got := texts(utts); !reflect.DeepEqual(got, []string{"Title", "Section"}) {
		t.Errorf("texts = %v", got)
	}
	utts = extract.Extract("# Title\n\nSome prose here.", "summarised", 3)
	want := []extract.Utterance{{Text: "Some prose here.", Kind: "prose"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("got %v, want %v", utts, want)
	}
}

func TestSummarisedLeadNotProseUsesFinalOnly(t *testing.T) {
	utts := extract.Extract("```js\nx()\n```\n\nDone, the fix is in place.",
		"summarised", 3)
	want := []string{"Done, the fix is in place."}
	if got := texts(utts); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- important / warnings --------------------------------------------------

func TestImportantAndWarnings(t *testing.T) {
	utts := extract.Extract(fixture, "important", 3)
	want := []extract.Utterance{
		{Text: warning, Kind: "warning"},
		{Text: closing, Kind: "question"},
	}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("important: got %v, want %v", utts, want)
	}

	utts = extract.Extract("Intro text.\n\nAll 47 tests passed.", "important", 3)
	want = []extract.Utterance{{Text: "All 47 tests passed.", Kind: "success"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("important success: got %v, want %v", utts, want)
	}

	utts = extract.Extract(fixture, "warnings", 3)
	want = []extract.Utterance{{Text: warning, Kind: "warning"}}
	if !reflect.DeepEqual(utts, want) {
		t.Errorf("warnings: got %v, want %v", utts, want)
	}

	utts = extract.Extract(tracebackFixture, "warnings", 3)
	if got := kinds(utts); !reflect.DeepEqual(got, []string{"error", "error"}) {
		t.Fatalf("traceback kinds = %v", got)
	}
	if utts[0].Text != "The command crashed:" {
		t.Errorf("utts[0] = %q", utts[0].Text)
	}
	if !strings.Contains(utts[1].Text, `File "app.py", line 10`) {
		t.Errorf("basename not applied: %q", utts[1].Text)
	}
	if strings.Contains(utts[1].Text, "/Users/") {
		t.Errorf("full path leaked: %q", utts[1].Text)
	}

	if got := extract.Extract("Just plain prose here.", "important", 3); len(got) != 0 {
		t.Errorf("important on plain prose = %v, want empty", got)
	}
}

// --- code mode -------------------------------------------------------------

func TestCodeModeFixture(t *testing.T) {
	utts := extract.Extract(fixture, "code", 3)
	want := []string{
		lead,
		"Python code, 3 lines",
		"def speak open paren text close paren colon",
		"resp equals post open paren url comma data close paren",
		"return resp",
		"All 47 tests passed.",
	}
	if got := texts(utts); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := 1; i <= 4; i++ {
		if utts[i].Kind != "code_summary" {
			t.Errorf("utts[%d].Kind = %q", i, utts[i].Kind)
		}
	}
}

func TestSymbolVerbalisation(t *testing.T) {
	cases := []struct{ in, spoken string }{
		{"```python\ndef fetch_data(url) -> dict:\n```",
			"def fetch underscore data open paren url close paren arrow dict colon"},
		{"```python\nif a == b:\n```", "if a double equals b colon"},
		{"```python\nx = items[0]\n```",
			"x equals items open bracket 0 close bracket"},
		{"```python\ntotal += 1\n```", "total plus equals 1"},
	}
	for _, c := range cases {
		utts := extract.Extract(c.in, "code", 3)
		if len(utts) < 2 || utts[1].Text != c.spoken {
			t.Errorf("Extract(%q, code)[1] = %v, want %q", c.in, utts, c.spoken)
		}
	}
}

func TestCodeModeAnnouncements(t *testing.T) {
	utts := extract.Extract("```rust\nlet x;\n```", "code", 3)
	if utts[0].Text != "Rust code, 1 line" {
		t.Errorf("rust announce = %q", utts[0].Text)
	}
	utts = extract.Extract("```\nfoo\n```", "code", 3)
	if utts[0].Text != "Code, 1 line" {
		t.Errorf("no-lang announce = %q", utts[0].Text)
	}
}

func TestBlankCodeLinesSkipped(t *testing.T) {
	utts := extract.Extract("```python\na = 1\n\nb = 2\n```", "code", 3)
	// announcement says 3 lines, but only 2 are read aloud
	if utts[0].Text != "Python code, 3 lines" {
		t.Errorf("announce = %q", utts[0].Text)
	}
	if len(utts) != 3 {
		t.Errorf("len = %d, want 3 (%v)", len(utts), utts)
	}
}

func TestCodeModeWithoutCodeIsSummarised(t *testing.T) {
	code := extract.Extract("Only prose here.", "code", 3)
	summ := extract.Extract("Only prose here.", "summarised", 3)
	if !reflect.DeepEqual(code, summ) {
		t.Errorf("code %v != summarised %v", code, summ)
	}
}

// Port of test_extract.py TestEmoji — emoji sequences -> CLDR short names.
func TestEmojiTable(t *testing.T) {
	one := func(text string) string {
		t.Helper()
		utts := extract.Extract(text, "verbose", 3)
		if len(utts) != 1 {
			t.Fatalf("%q: want 1 utterance, got %d: %v", text, len(utts), utts)
		}
		return utts[0].Text
	}
	cases := []struct{ text, want string }{
		// single emoji, sentence flow
		{"Great job! 🎉", "Great job! party popper"},
		{"I fixed it 😊 today.", "I fixed it smiling face with smiling eyes today."},
		// emoji before punctuation attaches without a space
		{"You did it 🎉!", "You did it party popper!"},
		{"Done (🎉) now.", "Done (party popper) now."},
		// immediate repeats collapse to one name
		{"Great job! 🎉🎉🎉 You did it!", "Great job! party popper You did it!"},
		{"Nice 😊😊.", "Nice smiling face with smiling eyes."},
		// skin tone sequence, longest match wins
		{"Thanks 👍🏽 a lot.", "Thanks thumbs up medium skin tone a lot."},
		// repeat collapse must not steal the base of a longer sequence
		{"Vote 👍👍🏽 now.", "Vote thumbs up thumbs up medium skin tone now."},
		// ZWJ family sequence is one name
		{"The 👨‍👩‍👧‍👦 arrived.", "The family man, woman, girl, boy arrived."},
		// flag (regional indicator pair)
		{"Deployed to 🇫🇷 first.", "Deployed to flag France first."},
		// unknown Extended_Pictographic codepoint is dropped silently
		{"weird \U0001F0C0 glyph", "weird glyph"},
		{"all \U0001F0C0\U0001F02C gone", "all gone"},
		// variation selectors handled (fully- and unqualified forms)
		{"Careful ⚠️ here.", "Careful warning here."},
		{"Careful ⚠ here.", "Careful warning here."},
		// non-emoji unicode untouched
		{"It’s 25° outside — café time.", "It’s 25° outside — café time."},
		// adjacent different emoji each get a name
		{"Ship it 🚀🎉 today.", "Ship it rocket party popper today."},
		// emoji jammed between words gets spaced
		{"mid🎉word", "mid party popper word"},
		// mixed prose
		{"Great job! 🎉🎉 You did it! 😊",
			"Great job! party popper You did it! smiling face with smiling eyes"},
	}
	for _, c := range cases {
		if got := one(c.text); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.text, got, c.want)
		}
	}
}

func TestEmojiOnlyParagraphOfUnknownsVanishes(t *testing.T) {
	if utts := extract.Extract("\U0001F0C0\U0001F0D0", "verbose", 3); len(utts) != 0 {
		t.Fatalf("want no utterances, got %v", utts)
	}
}

func TestEmojiInHeading(t *testing.T) {
	utts := extract.Extract("## Deploy complete 🎉", "verbose", 3)
	want := []extract.Utterance{{Text: "Deploy complete party popper", Kind: "heading"}}
	if !reflect.DeepEqual(utts, want) {
		t.Fatalf("got %v, want %v", utts, want)
	}
}

func TestEmojiInCodeModeLines(t *testing.T) {
	utts := extract.Extract("```python\nprint(\"done ✅\")\n```", "code", 3)
	if len(utts) < 2 {
		t.Fatalf("want >= 2 utterances, got %v", utts)
	}
	if !strings.Contains(utts[1].Text, "check mark button") ||
		strings.Contains(utts[1].Text, "✅") {
		t.Fatalf("code line not emoji-replaced: %q", utts[1].Text)
	}
}

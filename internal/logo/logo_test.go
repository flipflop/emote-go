package logo

import (
	"strings"
	"testing"
)

func TestRenderPlain(t *testing.T) {
	out := Render(false, "0.1.0")
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain render must not contain ANSI escapes")
	}
	for _, line := range art {
		if !strings.Contains(out, line) {
			t.Errorf("plain render missing art line %q", line)
		}
	}
	if !strings.Contains(out, "your code, spoken with feeling  ·  v0.1.0") {
		t.Errorf("plain render missing strapline+version: %q", out)
	}
}

func TestRenderColorGradientEndpoints(t *testing.T) {
	out := Render(true, "0.1.0")
	// First colored char of the widest line sits at col 0 -> pure coral;
	// line 4 (bottom) starts at col 0 and ends at the last col -> pure amber.
	if !strings.Contains(out, "\x1b[38;2;255;95;86m") {
		t.Errorf("gradient missing coral endpoint 255;95;86")
	}
	if !strings.Contains(out, "\x1b[38;2;255;196;61m") {
		t.Errorf("gradient missing amber endpoint 255;196;61")
	}
	if !strings.HasSuffix(strings.Split(out, "\n")[0], "\x1b[0m") {
		t.Errorf("colored lines must reset")
	}
	if !strings.Contains(out, "\x1b[2;3m") { // dim+italic strapline
		t.Errorf("strapline must be dim italic")
	}
}

func TestShouldColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ShouldColor(nil) {
		t.Errorf("NO_COLOR must disable color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if ShouldColor(nil) {
		t.Errorf("TERM=dumb must disable color")
	}
}

func TestArtShape(t *testing.T) {
	if len(art) != 5 {
		t.Fatalf("wordmark must be 5 lines, got %d", len(art))
	}
	w := len([]rune(art[0]))
	for i, line := range art {
		if len([]rune(line)) != w {
			t.Errorf("art line %d width %d != %d", i, len([]rune(line)), w)
		}
	}
}

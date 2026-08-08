// Package logo renders the EMOTE wordmark: 5-line block letters, soundwave
// flanks, coral→amber truecolor gradient. Faithful port of
// roz-emote-cli/emote_cli/logo.py.
package logo

import (
	"fmt"
	"os"
	"strings"
)

// 5-line block-letter wordmark, flanked by equalizer-style soundwaves.
// Identical art to the Python reference.
var art = []string{
	"    ▂   ▆   █▀▀▀▀  █▄   ▄█  ▄█▀▀█▄  ▀▀█▀▀  █▀▀▀▀   ▆   ▂    ",
	"    █ ▂ █   █      █ ▀▄▀ █  █    █    █    █       █ ▂ █    ",
	"  ▅ █ █ █   █▀▀▀   █  ▀  █  █    █    █    █▀▀▀    █ █ █ ▅  ",
	"▃ █ █ █ █   █      █     █  █    █    █    █       █ █ █ █ ▃",
	"█ █ █ █ █   █▄▄▄▄  █     █  ▀█▄▄█▀    █    █▄▄▄▄   █ █ █ █ █",
}

const strapline = "your code, spoken with feeling"

// Gradient endpoints: warm coral -> amber.
var (
	coral = [3]int{255, 95, 86}
	amber = [3]int{255, 196, 61}
)

func lerp(a, b int, t float64) int {
	return int(float64(a) + (float64(b)-float64(a))*t + 0.5)
}

// gradientLine colors each non-space rune by its column position across the
// gradient. Column positions are rune (character) positions, as in Python.
func gradientLine(line string, width int) string {
	var out strings.Builder
	span := width - 1
	if span < 1 {
		span = 1
	}
	col := 0
	for _, ch := range line {
		if ch == ' ' {
			out.WriteRune(ch)
			col++
			continue
		}
		t := float64(col) / float64(span)
		r := lerp(coral[0], amber[0], t)
		g := lerp(coral[1], amber[1], t)
		b := lerp(coral[2], amber[2], t)
		fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm%c", r, g, b, ch)
		col++
	}
	out.WriteString("\x1b[0m")
	return out.String()
}

// ShouldColor reports whether truecolor output is appropriate on f:
// only for a real TTY, without NO_COLOR, on a non-dumb terminal.
func ShouldColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Render returns the logo as a string: wordmark, strapline, version.
// Plain (uncolored) output when color is false.
func Render(color bool, version string) string {
	width := 0
	for _, line := range art {
		if n := len([]rune(line)); n > width {
			width = n
		}
	}
	strap := fmt.Sprintf("%s  ·  v%s", strapline, version)
	padN := (width - len([]rune(strap))) / 2
	if padN < 0 {
		padN = 0
	}
	pad := strings.Repeat(" ", padN)

	var lines []string
	if color {
		for _, line := range art {
			lines = append(lines, gradientLine(line, width))
		}
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("%s\x1b[2;3m%s\x1b[0m", pad, strap))
	} else {
		lines = append(lines, art...)
		lines = append(lines, "")
		lines = append(lines, pad+strap)
	}
	return strings.Join(lines, "\n")
}

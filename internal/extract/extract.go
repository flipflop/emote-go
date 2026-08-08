// Package extract ports emote_cli/extract.py: markdown/transcript -> []Utterance per mode.
//
// Utterance kinds: prose | heading | error | warning | success | question | code_summary
//
// Behavior-parity port of the Python reference (../roz-emote-cli/emote_cli/extract.py).
// Go regexp is RE2, so every Python lookaround/backreference has been restructured:
//
//   - _URL_RE used a lookahead to leave trailing sentence punctuation outside the
//     match; here the URL is matched greedily to the next whitespace and the
//     maximal trailing run of )].,;:!?"' is given back (equivalent result).
//   - _ITALIC_RE/_ITALIC_U_RE used lookbehind/lookahead word-boundary guards with
//     a lazy body; replaced by an explicit left-to-right scanner (replaceItalic)
//     with identical semantics.
//   - _PATH_RE used a negative lookbehind (?<![\w./\-]); replaced by matching one
//     preceding boundary char (or start of string) and re-emitting it.
//   - _HR_RE used a backreference \1 to repeat the rule char; replaced by
//     stripping whitespace and checking "all the same char from -*_ and >= 3".
//   - _SENT_SPLIT_RE used a lookbehind (?<=[.!?])\s+; replaced by a manual
//     scanner that splits at whitespace runs preceded by . ! or ?.
package extract

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// --------------------------------------------------------------------------
// Emoji -> spoken names (generated table: emoji_names.go)
// --------------------------------------------------------------------------
//
// Port of extract.py's _replace_emoji. Placement rule: an emoji sequence is
// replaced by its CLDR short name as words in the sentence flow, separated
// from neighbouring text by single spaces — except that no space is inserted
// after an opening bracket/quote or before a closing bracket/quote or
// sentence punctuation ("Great job! 🎉" -> "Great job! party popper";
// "You did it 🎉!" -> "You did it party popper!"). IMMEDIATELY repeated
// copies of the same sequence collapse to one name (🎉🎉🎉 -> "party
// popper"); a repeat is only collapsed when the longest match at that
// position is the same sequence (👍👍🏽 stays "thumbs up thumbs up medium
// skin tone"). Codepoints in the Extended_Pictographic ranges not resolved
// via the table — plus bare emoji machinery: variation selectors, ZWJ, the
// keycap combiner, tag characters, lone regional indicators — are DROPPED
// silently. Other non-emoji unicode (accents, symbols like °) is untouched.
//
// Go strings are bytes; all matching here walks a []rune slice so sequence
// lengths count code points exactly like Python string slicing.

var (
	emojiMaxLen int           // longest table key, in runes
	emojiFirst  map[rune]bool // first rune of every table key
)

func init() {
	emojiFirst = make(map[rune]bool, len(emojiNames))
	for k := range emojiNames {
		r := []rune(k)
		if len(r) > emojiMaxLen {
			emojiMaxLen = len(r)
		}
		emojiFirst[r[0]] = true
	}
}

const (
	emojiAttachBefore = "([{\"'“‘"        // openers: no space after these
	emojiAttachAfter  = ")]}.,!?;:…\"'”’" // no space before these
)

func isDroppedRune(r rune) bool {
	switch r {
	case 0xFE0E, 0xFE0F, 0x200D, 0x20E3:
		return true
	}
	if (r >= 0x1F1E6 && r <= 0x1F1FF) || (r >= 0xE0020 && r <= 0xE007F) {
		return true
	}
	// binary search the sorted, merged Extended_Pictographic ranges
	lo, hi := 0, len(extendedPictographic)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		rg := extendedPictographic[mid]
		switch {
		case r < rg[0]:
			hi = mid - 1
		case r > rg[1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// matchEmoji finds the longest table match at position i -> (name, length in
// runes) or ("", 0).
func matchEmoji(runes []rune, i int) (string, int) {
	if i < len(runes) && emojiFirst[runes[i]] {
		max := emojiMaxLen
		if rest := len(runes) - i; rest < max {
			max = rest
		}
		for k := max; k > 0; k-- {
			if name, ok := emojiNames[string(runes[i:i+k])]; ok {
				return name, k
			}
		}
	}
	return "", 0
}

func replaceEmoji(text string) string {
	runes := []rune(text)
	var out []rune
	i, n := 0, len(runes)
	for i < n {
		name, k := matchEmoji(runes, i)
		if k > 0 {
			seq := string(runes[i : i+k])
			j := i + k
			for j < n { // collapse IMMEDIATE repeats of the same sequence
				_, k2 := matchEmoji(runes, j)
				if k2 > 0 && string(runes[j:j+k2]) == seq {
					j += k2
				} else {
					break
				}
			}
			if len(out) > 0 {
				last := out[len(out)-1]
				if !unicode.IsSpace(last) && !strings.ContainsRune(emojiAttachBefore, last) {
					out = append(out, ' ')
				}
			}
			out = append(out, []rune(name)...)
			if j < n {
				nxt := runes[j]
				if !unicode.IsSpace(nxt) && !strings.ContainsRune(emojiAttachAfter, nxt) {
					out = append(out, ' ')
				}
			}
			i = j
		} else if isDroppedRune(runes[i]) {
			i++
		} else {
			out = append(out, runes[i])
			i++
		}
	}
	return string(out)
}

// Utterance is one spoken unit: the cleaned text and its kind.
type Utterance struct {
	Text string
	Kind string
}

// Modes lists the supported extraction modes.
var Modes = []string{"verbose", "summarised", "important", "warnings", "code"}

var proseish = map[string]bool{
	"prose": true, "error": true, "warning": true, "success": true, "question": true,
}

var important = map[string]bool{
	"error": true, "warning": true, "success": true, "question": true,
}

// --------------------------------------------------------------------------
// ANSI stripping
// --------------------------------------------------------------------------

var ansiRE = regexp.MustCompile(
	"\x1b\\[[0-9;?]*[ -/]*[@-~]" + // CSI sequences (colors, cursor)
		"|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)" + // OSC sequences (titles, links)
		"|\x1b[@-Z\\\\-_]", // other two-byte escapes
)

func stripANSI(text string) string { return ansiRE.ReplaceAllString(text, "") }

// --------------------------------------------------------------------------
// Inline cleanup: markdown symbols, URLs -> domain, paths -> basename
// --------------------------------------------------------------------------

var (
	imageRE = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkRE  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	// Python's _URL_RE used a lazy path plus a lookahead so trailing sentence
	// punctuation stays outside the match. RE2 has no lookahead: match greedily
	// to the next whitespace, then give back the trailing punctuation run.
	urlRE   = regexp.MustCompile(`https?://([\p{L}\p{N}_\-]+(?:\.[\p{L}\p{N}_\-]+)*)(?::\d+)?(?:/[^\s]*)?`)
	boldRE  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	boldURE = regexp.MustCompile(`__(.+?)__`)
	// A path is a slashed token that either starts with /, ~/, ./, ../ or whose
	// basename has a file extension ("and/or", "24/7", "I/O" stay untouched).
	// Python used a negative lookbehind (?<![\w./\-]); here one boundary char
	// (or start of string) is matched and re-emitted by the replacer. \w is
	// spelled out as \p{L}\p{N}_ because Python's \w is Unicode-aware and Go's
	// is ASCII-only ("café/menu.txt" must reduce to "menu.txt").
	pathRE = regexp.MustCompile(
		`(^|[^\p{L}\p{N}_./\-])((?:~/|\.{1,2}/|/)[\p{L}\p{N}_.\-@+~/]+|[\p{L}\p{N}_.\-@+]+(?:/[\p{L}\p{N}_.\-@+]+)+)/?`)
	wsRE = regexp.MustCompile(`\s+`)
)

const urlTrailPunct = `)].,;:!?"'`

func urlRepl(match string) string {
	stripped := strings.TrimRight(match, urlTrailPunct)
	trail := match[len(stripped):]
	m := urlRE.FindStringSubmatch(stripped)
	if m == nil { // trimming ate the whole path — re-derive from the full match
		m = urlRE.FindStringSubmatch(match)
		if m == nil {
			return match
		}
	}
	return m[1] + trail
}

func pathRepl(match []string) string {
	prefix := match[1]
	// Python's group(0) excluded the (zero-width) lookbehind; here the boundary
	// char was consumed, so cut it off to reconstruct what Python's repl saw.
	full := match[0][len(prefix):]
	stripped := strings.TrimRight(full, ".") // trailing dots are sentence
	trail := full[len(stripped):]            // punctuation, not path chars
	tok := strings.TrimRight(stripped, "/")
	base := tok
	if i := strings.LastIndex(tok, "/"); i >= 0 {
		base = tok[i+1:]
	}
	hasPrefix := strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/") ||
		strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../")
	if !hasPrefix && !strings.Contains(base, ".") {
		return prefix + full
	}
	if base == "" {
		return prefix + full
	}
	return prefix + base + trail
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// replaceItalic ports Python's (?<!\w)\*([^*\s][^*]*?)\*(?!\w) (and the _
// variant) without lookarounds: scan left to right; at each delimiter not
// preceded by a word char, the body runs to the next delimiter, which must
// not be followed by a word char; the body must start with a non-space,
// non-delimiter char and cannot contain the delimiter.
func replaceItalic(s string, delim rune) string {
	runes := []rune(s)
	var out []rune
	i := 0
	for i < len(runes) {
		if runes[i] == delim && (len(out) == 0 || !isWordRune(out[len(out)-1])) {
			j := i + 1
			if j < len(runes) && runes[j] != delim && !unicode.IsSpace(runes[j]) {
				k := j + 1
				for k < len(runes) && runes[k] != delim {
					k++
				}
				if k < len(runes) && (k+1 >= len(runes) || !isWordRune(runes[k+1])) {
					out = append(out, runes[j:k]...) // keep body, drop delimiters
					i = k + 1
					continue
				}
			}
		}
		out = append(out, runes[i])
		i++
	}
	return string(out)
}

func clean(s string) string {
	s = imageRE.ReplaceAllString(s, "$1")
	s = linkRE.ReplaceAllString(s, "$1") // [text](url) -> text
	s = urlRE.ReplaceAllStringFunc(s, urlRepl)
	s = strings.ReplaceAll(s, "`", "") // inline code kept literal
	s = boldRE.ReplaceAllString(s, "$1")
	s = boldURE.ReplaceAllString(s, "$1")
	s = replaceItalic(s, '*')
	s = replaceItalic(s, '_')
	s = pathRE.ReplaceAllStringFunc(s, func(m string) string {
		return pathRepl(pathRE.FindStringSubmatch(m))
	})
	return strings.TrimSpace(wsRE.ReplaceAllString(s, " "))
}

// --------------------------------------------------------------------------
// Kind classification for prose blocks
// --------------------------------------------------------------------------

var (
	errorRE = regexp.MustCompile(`(?i)\b(error|errors|exception|exceptions|traceback|fatal|fail|fails|failed` +
		`|failing|failure|failures|crash|crashes|crashed|broken|denied|unable)\b` +
		`|[✗✖❌]`)
	warningRE = regexp.MustCompile(`(?i)\b(warn|warning|warnings|caution|deprecated|deprecation)\b|⚠`)
	successRE = regexp.MustCompile(`(?i)\b(pass|passes|passed|passing|success|successful|successfully|fixed` +
		`|resolved|complete|completed|works|working|deployed|done)\b` +
		`|\ball green\b|[✓✔✅🎉]`)
)

func kindOf(text string) string {
	switch {
	case errorRE.MatchString(text):
		return "error"
	case warningRE.MatchString(text):
		return "warning"
	case strings.HasSuffix(strings.TrimRightFunc(text, unicode.IsSpace), "?"):
		return "question"
	case successRE.MatchString(text):
		return "success"
	}
	return "prose"
}

// --------------------------------------------------------------------------
// Fenced code blocks
// --------------------------------------------------------------------------

var fenceRE = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})\\s*(.*)$")

// langNames maps fence info tokens to spoken names. A present key with an
// empty value means "no language" (announced as plain code).
var langNames = map[string]string{
	"python": "Python", "py": "Python", "python3": "Python",
	"javascript": "JavaScript", "js": "JavaScript", "jsx": "JavaScript",
	"typescript": "TypeScript", "ts": "TypeScript", "tsx": "TypeScript",
	"bash": "Bash", "sh": "shell", "shell": "shell", "zsh": "shell",
	"console": "console", "json": "JSON", "yaml": "YAML", "yml": "YAML",
	"toml": "TOML", "html": "HTML", "css": "CSS", "sql": "SQL",
	"c": "C", "cpp": "C++", "c++": "C++", "java": "Java", "go": "Go",
	"rust": "Rust", "ruby": "Ruby", "rb": "Ruby", "swift": "Swift",
	"kotlin": "Kotlin", "php": "PHP", "diff": "diff", "markdown": "Markdown",
	"md": "Markdown", "text": "", "txt": "", "plain": "", "": "",
}

func langDisplay(info string) string {
	token := ""
	if fields := strings.Fields(strings.TrimSpace(info)); len(fields) > 0 {
		token = fields[0]
	}
	if display, ok := langNames[strings.ToLower(token)]; ok {
		return display
	}
	return token
}

func pluralLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func codeSummaryText(info string, nLines int) string {
	display := langDisplay(info)
	if display == "" {
		display = "code"
	}
	return fmt.Sprintf("a %s block, %s", display, pluralLines(nLines))
}

type segment struct {
	isCode bool
	text   string   // isCode == false
	info   string   // isCode == true
	code   []string // isCode == true
}

func splitFences(text string) []segment {
	var segments []segment
	var buf []string
	lines := strings.Split(text, "\n")
	i, n := 0, len(lines)
	for i < n {
		m := fenceRE.FindStringSubmatch(lines[i])
		if m != nil {
			if len(buf) > 0 {
				segments = append(segments, segment{text: strings.Join(buf, "\n")})
				buf = nil
			}
			fenceChar := m[1][:1]
			info := strings.TrimSpace(m[2])
			closer := regexp.MustCompile(`^\s{0,3}` + regexp.QuoteMeta(fenceChar) + `{3,}\s*$`)
			i++
			var code []string
			for i < n && !closer.MatchString(lines[i]) {
				code = append(code, lines[i])
				i++
			}
			if i < n {
				i++ // skip closing fence (unterminated fence = rest is code)
			}
			segments = append(segments, segment{isCode: true, info: info, code: code})
		} else {
			buf = append(buf, lines[i])
			i++
		}
	}
	if len(buf) > 0 {
		segments = append(segments, segment{text: strings.Join(buf, "\n")})
	}
	return segments
}

// --------------------------------------------------------------------------
// Text segments -> blocks
// --------------------------------------------------------------------------

var (
	headingRE   = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	tableLineRE = regexp.MustCompile(`^\s*\|`)
	bulletRE    = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,3}[.)])\s+`)
	quoteRE     = regexp.MustCompile(`^\s*>\s?`)
)

// isHR ports _HR_RE (^\s*([-*_])\s*(?:\1\s*){2,}$) without the backreference:
// after removing all whitespace, the line must be >= 3 copies of one rule char.
func isHR(line string) bool {
	var c rune
	count := 0
	for _, r := range line {
		if unicode.IsSpace(r) {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if count == 0 {
			c = r
		} else if r != c {
			return false
		}
		count++
	}
	return count >= 3
}

func isTableSep(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" || !strings.Contains(s, "-") {
		return false
	}
	for _, r := range s {
		switch r {
		case '|', '-', ':', ' ':
		default:
			return false
		}
	}
	return true
}

func tableRows(lines []string) int {
	sep := 0
	for _, l := range lines {
		if isTableSep(l) {
			sep++
		}
	}
	rows := len(lines) - sep
	if sep > 0 {
		rows--
	}
	if rows < 0 {
		rows = 0
	}
	return rows
}

func paragraphText(lines []string) string {
	var parts []string
	for _, line := range lines {
		line = quoteRE.ReplaceAllString(line, "")
		if loc := bulletRE.FindStringIndex(line); loc != nil {
			item := strings.TrimSpace(line[loc[1]:])
			if item != "" && !strings.ContainsRune(".!?:;", lastRune(item)) {
				item += "." // list items become spoken sentences
			}
			parts = append(parts, item)
		} else {
			parts = append(parts, strings.TrimSpace(line))
		}
	}
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return clean(strings.Join(nonEmpty, " "))
}

func lastRune(s string) rune {
	r := rune(0)
	for _, c := range s {
		r = c
	}
	return r
}

type block struct {
	kind  string
	text  string
	info  string   // code_summary only
	lines []string // code_summary only
}

func runBlocks(lines []string) []block {
	var blocks []block
	var para, table []string

	flushPara := func() {
		if len(para) > 0 {
			if t := paragraphText(para); t != "" {
				blocks = append(blocks, block{kind: kindOf(t), text: t})
			}
			para = nil
		}
	}
	flushTable := func() {
		if len(table) > 0 {
			n := tableRows(table)
			plural := "s"
			if n == 1 {
				plural = ""
			}
			blocks = append(blocks, block{
				kind: "prose",
				text: fmt.Sprintf("a table with %d row%s", n, plural),
			})
			table = nil
		}
	}

	for _, line := range lines {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flushPara()
			flushTable()
			if t := clean(m[2]); t != "" {
				blocks = append(blocks, block{kind: "heading", text: t})
			}
			continue
		}
		if tableLineRE.MatchString(line) {
			flushPara()
			table = append(table, line)
			continue
		}
		flushTable()
		if isHR(line) {
			flushPara()
			continue
		}
		para = append(para, line)
	}
	flushPara()
	flushTable()
	return blocks
}

func textBlocks(text string) []block {
	var blocks []block
	var run []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			run = append(run, line)
		} else if len(run) > 0 {
			blocks = append(blocks, runBlocks(run)...)
			run = nil
		}
	}
	if len(run) > 0 {
		blocks = append(blocks, runBlocks(run)...)
	}
	return blocks
}

func parse(text string) []block {
	var blocks []block
	for _, seg := range splitFences(replaceEmoji(stripANSI(text))) {
		if seg.isCode {
			blocks = append(blocks, block{
				kind:  "code_summary",
				text:  codeSummaryText(seg.info, len(seg.code)),
				info:  seg.info,
				lines: seg.code,
			})
		} else {
			blocks = append(blocks, textBlocks(seg.text)...)
		}
	}
	return blocks
}

// --------------------------------------------------------------------------
// Sentences + summarised selection
// --------------------------------------------------------------------------

// sentences ports (?<=[.!?])\s+ splitting without the lookbehind: split at
// each whitespace run whose preceding character is . ! or ?.
func sentences(text string) []string {
	t := strings.TrimSpace(text)
	var out []string
	runes := []rune(t)
	start, i := 0, 0
	for i < len(runes) {
		r := runes[i]
		if (r == '.' || r == '!' || r == '?') && i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
			out = append(out, string(runes[start:i+1]))
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			start, i = j, j
			continue
		}
		i++
	}
	if start < len(runes) {
		out = append(out, string(runes[start:]))
	}
	var nonEmpty []string
	for _, s := range out {
		if s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	return nonEmpty
}

type picked struct {
	text string
	kind string
	i    int // document index
}

// summarise: lead prose paragraph + final prose paragraph(s), capped at
// maxSentences sentences total.
func summarise(blocks []block, maxSentences int) []picked {
	var proseIdx []int
	for i, b := range blocks {
		if proseish[b.kind] {
			proseIdx = append(proseIdx, i)
		}
	}
	if len(proseIdx) == 0 {
		// Headings spoken only if nothing else qualifies...
		var pickedIdx []int
		for i, b := range blocks {
			if b.kind == "heading" {
				pickedIdx = append(pickedIdx, i)
			}
		}
		if len(pickedIdx) == 0 {
			// ...and code_summary only if nothing at all remains.
			for i, b := range blocks {
				if b.kind == "code_summary" {
					pickedIdx = append(pickedIdx, i)
				}
			}
		}
		out := make([]picked, 0, len(pickedIdx))
		for _, i := range pickedIdx {
			out = append(out, picked{text: blocks[i].text, kind: blocks[i].kind, i: i})
		}
		return out
	}

	budget := maxSentences
	if budget < 1 {
		budget = 1
	}
	chosen := map[int]string{}

	// Lead paragraph: the first non-heading block, if it is prose-ish.
	leadI := -1
	for i, b := range blocks {
		if b.kind == "heading" {
			continue
		}
		if proseish[b.kind] {
			leadI = i
		}
		break
	}
	var tail []int
	for _, i := range proseIdx {
		if i != leadI {
			tail = append(tail, i)
		}
	}

	if leadI >= 0 {
		leadCap := budget
		if len(tail) > 0 {
			leadCap = budget - 1 // reserve 1 for the finale
		}
		sents := sentences(blocks[leadI].text)
		if len(sents) > leadCap {
			sents = sents[:leadCap]
		}
		if len(sents) > 0 {
			chosen[leadI] = strings.Join(sents, " ")
			budget -= len(sents)
		}
	}

	firstTail := true
	for t := len(tail) - 1; t >= 0; t-- {
		if budget <= 0 {
			break
		}
		i := tail[t]
		sents := sentences(blocks[i].text)
		if len(sents) <= budget {
			chosen[i] = blocks[i].text
			budget -= len(sents)
		} else if firstTail {
			chosen[i] = strings.Join(sents[:budget], " ")
			budget = 0
		} else {
			break // earlier paragraphs only when they fit whole
		}
		firstTail = false
	}

	var idx []int
	for i := range chosen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]picked, 0, len(idx))
	for _, i := range idx {
		out = append(out, picked{text: chosen[i], kind: blocks[i].kind, i: i})
	}
	return out
}

// --------------------------------------------------------------------------
// Code mode: read code aloud, symbols verbalised
// --------------------------------------------------------------------------

var multiSymbols = []struct{ sym, word string }{
	{"->", "arrow"}, {"=>", "fat arrow"}, {"==", "double equals"},
	{"!=", "not equals"}, {">=", "greater or equal"}, {"<=", "less or equal"},
	{"+=", "plus equals"}, {"-=", "minus equals"}, {"**", "double star"},
	{"//", "double slash"}, {"::", "double colon"},
}

var symbols = map[rune]string{
	'_': "underscore", '(': "open paren", ')': "close paren",
	'[': "open bracket", ']': "close bracket",
	'{': "open brace", '}': "close brace",
	'=': "equals", ':': "colon", ';': "semicolon", ',': "comma", '.': "dot",
	'"': "quote", '\'': "quote", '#': "hash", '*': "star", '+': "plus",
	'-': "dash", '/': "slash", '\\': "backslash", '<': "less than",
	'>': "greater than", '%': "percent", '&': "ampersand", '|': "pipe",
	'!': "bang", '@': "at", '^': "caret", '~': "tilde", '?': "question mark",
	'$': "dollar", '`': "backtick",
}

func verbalise(line string) string {
	s := strings.TrimSpace(line)
	for _, ms := range multiSymbols {
		s = strings.ReplaceAll(s, ms.sym, " "+ms.word+" ")
	}
	var out strings.Builder
	for _, ch := range s {
		if word, ok := symbols[ch]; ok {
			out.WriteString(" " + word + " ")
		} else {
			out.WriteRune(ch)
		}
	}
	return strings.TrimSpace(wsRE.ReplaceAllString(out.String(), " "))
}

func readCode(b block) []Utterance {
	n := len(b.lines)
	display := langDisplay(b.info)
	announce := "Code, " + pluralLines(n)
	if display != "" {
		announce = display + " code, " + pluralLines(n)
	}
	out := []Utterance{{Text: announce, Kind: "code_summary"}}
	for _, line := range b.lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, Utterance{Text: verbalise(line), Kind: "code_summary"})
		}
	}
	return out
}

// --------------------------------------------------------------------------
// Public API
// --------------------------------------------------------------------------

// Extract turns markdown-ish text into utterances for mode. Like the Python
// reference (which raises ValueError), an unknown mode panics; hook callers
// recover and fall back to speaking the raw text.
func Extract(text, mode string, maxSentences int) []Utterance {
	known := false
	for _, m := range Modes {
		if m == mode {
			known = true
			break
		}
	}
	if !known {
		panic(fmt.Sprintf("unknown mode %q (expected one of %s)",
			mode, strings.Join(Modes, ", ")))
	}

	blocks := parse(text)

	switch mode {
	case "verbose":
		out := make([]Utterance, 0, len(blocks))
		for _, b := range blocks {
			out = append(out, Utterance{Text: b.text, Kind: b.kind})
		}
		return out

	case "important":
		var out []Utterance
		for _, b := range blocks {
			if important[b.kind] {
				out = append(out, Utterance{Text: b.text, Kind: b.kind})
			}
		}
		return out

	case "warnings":
		var out []Utterance
		for _, b := range blocks {
			if b.kind == "error" || b.kind == "warning" {
				out = append(out, Utterance{Text: b.text, Kind: b.kind})
			}
		}
		return out

	case "summarised":
		sel := summarise(blocks, maxSentences)
		out := make([]Utterance, 0, len(sel))
		for _, s := range sel {
			out = append(out, Utterance{Text: s.text, Kind: s.kind})
		}
		return out
	}

	// mode == "code": full code blocks read aloud + summarised prose,
	// in document order.
	chosen := map[int]picked{}
	for _, s := range summarise(blocks, maxSentences) {
		if blocks[s.i].kind != "code_summary" {
			chosen[s.i] = s
		}
	}
	var out []Utterance
	for i, b := range blocks {
		if b.kind == "code_summary" {
			out = append(out, readCode(b)...)
		} else if s, ok := chosen[i]; ok {
			out = append(out, Utterance{Text: s.text, Kind: s.kind})
		}
	}
	return out
}

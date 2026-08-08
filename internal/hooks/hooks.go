// Package hooks ports emote_cli/hooks.py: the Claude Code hook runtime
// (stop / notification) plus install/uninstall/status for the hooks section
// of settings.json.
//
// Two halves:
//
//   - RunHook(kind, stdinJSON, cfg) — the runtime side, invoked by
//     "emote hook stop" / "emote hook notification" with Claude Code's hook
//     JSON already parsed from stdin. It never panics outward and always
//     returns 0: a broken speaker must never fail a Claude session.
//
//   - Install(scope, mode) / Uninstall(scope) / Status() — manage the hooks
//     section of ~/.claude/settings.json (user scope) or .claude/settings.json
//     (project scope), merging non-destructively with the "emote hook" marker.
//
// Transcript shape: each line is a JSON object; assistant entries have
// message.role == "assistant" and message.content as a list of blocks
// (text / thinking / tool_use). A single assistant turn is split across
// SEVERAL consecutive lines — one content block per line — all sharing the
// same message.id, so the "last assistant message" is the concatenation of
// the text blocks of every entry carrying the final assistant id.
package hooks

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/extract"
	"github.com/flipflop/emote-go/internal/sentiment"
)

// isOurs reports whether an installed hook command belongs to any emote
// binary. The Python CLI installs ".../emote hook ..." and the Go CLI
// ".../emote-go hook ..." — a bare `Contains(s, "emote hook")` missed the
// latter, so reinstalling DUPLICATED Go-installed hooks instead of replacing
// them (found live 2026-08-06).
func isOurs(command string) bool {
	return strings.Contains(command, "emote hook") ||
		strings.Contains(command, "emote-go hook")
}

// Marker is present in every command we install; Uninstall removes only
// entries whose command contains it.
const Marker = "emote hook"

const LogMaxBytes = 200 * 1024

// HookEvents are the settings.json events emote installs into.
var HookEvents = []string{"Stop", "Notification"}

// NotificationMaxChars caps how much of a notification message is spoken.
const NotificationMaxChars = 240

// ---------------------------------------------------------------------------
// Logging (one line per event, ring-truncated at 200 KB)
// ---------------------------------------------------------------------------

// logPath is a variable so tests can point it somewhere unwritable. Like the
// Python reference it is hard-wired to ~/.config/emote/emote.log (NOT the
// EMOTE_CONFIG_DIR override — hooks always log to the real user config dir).
var logPath = defaultLogPath

func defaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, ".config", "emote", "emote.log")
}

// logf appends one line to the emote log; never fails.
func logf(line string) {
	defer func() { _ = recover() }()
	path := logPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	stamp := time.Now().Format("2006-01-02 15:04:05")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = fh.WriteString(stamp + " " + line + "\n")
	_ = fh.Close()
	st, err := os.Stat(path)
	if err != nil || st.Size() <= LogMaxBytes {
		return
	}
	// Ring truncation: keep the newest half, aligned to a line start.
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > LogMaxBytes/2 {
		data = data[len(data)-LogMaxBytes/2:]
	}
	if cut := indexByte(data, '\n'); cut != -1 {
		data = data[cut+1:]
	}
	_ = os.WriteFile(path, data, 0o644)
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Transcript reading
// ---------------------------------------------------------------------------

// joinTextBlocks joins the text of blocks with type == "text"; tolerates odd
// shapes.
func joinTextBlocks(content interface{}) string {
	if s, ok := content.(string); ok {
		return s
	}
	list, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range list {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if block["type"] != "text" {
			continue
		}
		if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// lastAssistantText returns the text of the last assistant message in a
// Claude Code transcript JSONL file. A single assistant turn spans multiple
// lines (one content block each) sharing message.id, so text is accumulated
// per id and the last id that produced any text wins (a trailing id whose
// blocks are all tool_use/thinking falls back to the previous one).
func lastAssistantText(transcriptPath string) string {
	fh, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer fh.Close()

	var order []string             // message ids in order of first appearance
	texts := map[string][]string{} // id -> list of text fragments
	counter := 0

	reader := bufio.NewReader(fh)
	for {
		line, err := reader.ReadString('\n')
		done := err == io.EOF
		line = strings.TrimSpace(line)
		if line != "" {
			if obj, perr := parseJSON([]byte(line)); perr == nil {
				if om, ok := obj.(*omap); ok {
					processTranscriptLine(om, &order, texts, &counter)
				}
			}
		}
		if done || (err != nil && err != io.EOF) {
			break
		}
	}

	for i := len(order) - 1; i >= 0; i-- {
		joined := strings.TrimSpace(strings.Join(texts[order[i]], "\n\n"))
		if joined != "" {
			return joined
		}
	}
	return ""
}

func processTranscriptLine(obj *omap, order *[]string, texts map[string][]string, counter *int) {
	msgVal, _ := obj.Get("message")
	msg, ok := msgVal.(*omap)
	if !ok {
		return
	}
	if role, _ := msg.Get("role"); role != "assistant" {
		return
	}
	msgID := ""
	if idVal, ok := msg.Get("id"); ok {
		if s, ok := idVal.(string); ok {
			msgID = s
		}
	}
	if msgID == "" { // synthesise an id so entries without one still work
		*counter++
		msgID = fmt.Sprintf("_anon_%d", *counter)
	}
	if _, seen := texts[msgID]; !seen {
		texts[msgID] = nil
		*order = append(*order, msgID)
	}
	contentVal, _ := msg.Get("content")
	fragment := joinTextBlocks(toPlain(contentVal))
	if strings.TrimSpace(fragment) != "" {
		texts[msgID] = append(texts[msgID], fragment)
	}
}

// toPlain converts omap-based values to plain map/slice values so the
// transcript helpers can share code with callers that pass plain JSON.
func toPlain(v interface{}) interface{} {
	switch t := v.(type) {
	case *omap:
		out := map[string]interface{}{}
		for _, k := range t.keys {
			out[k] = toPlain(t.vals[k])
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, item := range t {
			out[i] = toPlain(item)
		}
		return out
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// Bridges to sibling modules (patchable in tests; degrade gracefully)
// ---------------------------------------------------------------------------

// Player is wired by cmd/emote to the real speech pipeline. When nil, hooks
// are silent no-ops (per DESIGN.md an unprovisioned engine must never break
// or spam a Claude session).
var Player func(utterances []extract.Utterance, emotion string, cfg config.Config) error

var (
	speakFn    = speakDefault
	extractFn  = extractBridge
	classifyFn = classifyBridge
)

// speakDefault honors quiet mode without touching the audio pipeline at all.
func speakDefault(utterances []extract.Utterance, emotion string, cfg config.Config) error {
	if cfg.Quiet {
		for _, utt := range utterances {
			fmt.Println(utt.Text)
		}
		return nil
	}
	if Player != nil {
		return Player(utterances, emotion, cfg)
	}
	return nil
}

// extractBridge mirrors Python's _extract bridge: it calls extract with the
// module's default sentence budget (the Python bridge calls
// extract.extract(text, mode), never forwarding config max_sentences), and
// falls back to one prose utterance if extract panics or returns nothing.
func extractBridge(text, mode string, cfg config.Config) (out []extract.Utterance) {
	fallback := []extract.Utterance{{Text: text, Kind: "prose"}}
	defer func() {
		if recover() != nil || len(out) == 0 {
			out = fallback
		}
	}()
	out = extract.Extract(text, mode, 3)
	if len(out) == 0 {
		out = fallback
	}
	return out
}

// classifyBridge mirrors Python's _classify bridge: neutral on any failure.
func classifyBridge(text string) (emotion, reason string) {
	defer func() {
		if recover() != nil {
			emotion, reason = "neutral", "sentiment unavailable"
		}
	}()
	emotion, reason = sentiment.Classify(text)
	if emotion == "" {
		emotion = "neutral"
	}
	return emotion, reason
}

// ---------------------------------------------------------------------------
// Hook runtime
// ---------------------------------------------------------------------------

// truthy mirrors Python truthiness for decoded JSON values.
func truthy(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case []interface{}:
		return len(t) > 0
	case map[string]interface{}:
		return len(t) > 0
	default:
		return true
	}
}

// applyDirOverride applies the per-directory override (HOOK events only):
// the hook payload's "cwd" field is matched against cfg.DirOverrides
// (longest path-prefix match on path components). A missing "cwd" or no
// match leaves cfg untouched. Precedence: an override's mode beats
// everything (baked --mode, EMOTE_MODE, config file); for enabled, any OFF
// wins.
func applyDirOverride(kind string, stdinJSON map[string]interface{}, cfg *config.Config) {
	cwd, _ := stdinJSON["cwd"].(string)
	if cwd == "" || len(cfg.DirOverrides) == 0 {
		return
	}
	resolved := cwd
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		resolved = r
	}
	key, override, ok := config.MatchDirOverride(cfg.DirOverrides, resolved)
	if !ok {
		return
	}
	if override.Enabled != nil {
		cfg.Enabled = cfg.Enabled && *override.Enabled
	}
	if override.Mode != "" {
		cfg.Mode = override.Mode
	}
	logf(fmt.Sprintf("hook %s: dir override %s -> enabled=%v mode=%s",
		kind, key, cfg.Enabled, cfg.Mode))
}

// RunHook is the entry point for "emote hook <kind>". Always returns 0.
func RunHook(kind string, stdinJSON map[string]interface{}, cfg config.Config) int {
	defer func() {
		if r := recover(); r != nil {
			logf(fmt.Sprintf("hook %s: error: %v", kind, r))
		}
	}()
	if stdinJSON == nil {
		stdinJSON = map[string]interface{}{}
	}
	applyDirOverride(kind, stdinJSON, &cfg)
	switch kind {
	case "stop":
		runStop(stdinJSON, cfg)
	case "notification":
		runNotification(stdinJSON, cfg)
	default:
		logf(fmt.Sprintf("hook %s: unknown hook kind, ignored", kind))
	}
	return 0
}

// settlePoll is variable so tests can shrink the wait.
var settlePoll = 300 * time.Millisecond

func waitForTranscriptSettle(path string) {
	size := int64(-1)
	stable := 0
	for i := 0; i < 8 && stable < 2; i++ {
		st, err := os.Stat(path)
		if err != nil {
			return
		}
		if st.Size() == size {
			stable++
		} else {
			size = st.Size()
			stable = 0
		}
		if stable < 2 {
			time.Sleep(settlePoll)
		}
	}
}

func runStop(stdinJSON map[string]interface{}, cfg config.Config) {
	if truthy(stdinJSON["stop_hook_active"]) {
		logf("hook stop: skipped (stop_hook_active loop guard)")
		return
	}
	if !cfg.Enabled {
		return
	}
	transcriptPath, _ := stdinJSON["transcript_path"].(string)
	if transcriptPath == "" {
		logf(fmt.Sprintf("hook stop: no transcript at %q", transcriptPath))
		return
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		logf(fmt.Sprintf("hook stop: no transcript at %q", transcriptPath))
		return
	}
	// Stop can fire before Claude Code has flushed the final assistant message
	// to the transcript (observed 2026-08-06: hook spoke the PREVIOUS turn's
	// reply — a one-turn lag). Wait for the file to go quiet before reading:
	// re-stat every 300 ms until the size holds still twice, bounded at ~2.4 s.
	waitForTranscriptSettle(transcriptPath)
	text := lastAssistantText(transcriptPath)
	if text == "" {
		logf("hook stop: no assistant text found in transcript")
		return
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "summarised"
	}
	utterances := extractFn(text, mode, cfg)
	if len(utterances) == 0 {
		logf(fmt.Sprintf("hook stop: mode %s produced nothing to say", mode))
		return
	}
	var emotion, reason string
	if cfg.Emotion == "" || cfg.Emotion == "auto" {
		emotion, reason = classifyFn(text)
	} else {
		emotion, reason = cfg.Emotion, "pinned by config"
	}
	if err := speakFn(utterances, emotion, cfg); err != nil {
		logf(fmt.Sprintf("hook stop: error: %v", err))
		return
	}
	logf(fmt.Sprintf("hook stop: spoke %d utterance(s) mode=%s emotion=%s (%s)",
		len(utterances), mode, emotion, reason))
}

// SpeechBusy is injected by the CLI (play.CurrentlyPlaying against the shared
// pidfile). Notifications YIELD to speech already in progress: with
// interrupt=true a "waiting for your input" notification was killing a long
// verbose reading mid-sentence to say 28 characters (found live 2026-08-06) —
// and if jane is mid-reading, the listener is present anyway.
var SpeechBusy func() bool

func runNotification(stdinJSON map[string]interface{}, cfg config.Config) {
	if !cfg.Enabled {
		return
	}
	if SpeechBusy != nil && SpeechBusy() {
		logf("hook notification: skipped (speech in progress)")
		return
	}
	message, _ := stdinJSON["message"].(string)
	if strings.TrimSpace(message) == "" {
		logf("hook notification: empty message, nothing to say")
		return
	}
	brief := strings.Join(strings.Fields(message), " ")
	if utf8.RuneCountInString(brief) > NotificationMaxChars {
		runes := []rune(brief)
		brief = strings.TrimRightFunc(string(runes[:NotificationMaxChars-1]),
			unicode.IsSpace) + "…"
	}
	if err := speakFn([]extract.Utterance{{Text: brief, Kind: "prose"}}, "calm", cfg); err != nil {
		logf(fmt.Sprintf("hook notification: error: %v", err))
		return
	}
	logf(fmt.Sprintf("hook notification: spoke %d chars (calm)",
		utf8.RuneCountInString(brief)))
}

// ---------------------------------------------------------------------------
// settings.json install / uninstall / status
// ---------------------------------------------------------------------------

func settingsPath(scope string) (string, error) {
	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	}
	return "", fmt.Errorf("unknown scope %q (expected 'user' or 'project')", scope)
}

// emoteCommand returns the absolute path of the running emote binary, so the
// installed hook commands point at whichever binary installed them. It is a
// variable so tests can pin it (the test binary is not named "emote", so its
// path would not carry the marker).
var emoteCommand = defaultEmoteCommand

func defaultEmoteCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

func quotePath(path string) string {
	if strings.Contains(path, " ") {
		return `"` + path + `"`
	}
	return path
}

// readSettings returns (data, errMsg). Missing file -> empty object, "";
// unparseable file -> nil and a message so callers refuse to clobber it.
func readSettings(path string) (*omap, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newOmap(), ""
		}
		return nil, fmt.Sprintf("%s could not be read — left untouched.", path)
	}
	parsed, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Sprintf("%s exists but is not valid JSON — left untouched.", path)
	}
	obj, ok := parsed.(*omap)
	if !ok {
		return nil, fmt.Sprintf("%s is not a JSON object — left untouched.", path)
	}
	return obj, ""
}

func writeSettings(path string, data *omap) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := marshalIndent(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// stripEmoteEntries removes emote commands from a list of matcher groups.
// Returns the kept groups and how many commands were removed; groups left
// empty are dropped.
func stripEmoteEntries(matcherGroups []interface{}) ([]interface{}, int) {
	removed := 0
	var keptGroups []interface{}
	for _, groupVal := range matcherGroups {
		group, ok := groupVal.(*omap)
		if !ok {
			keptGroups = append(keptGroups, groupVal)
			continue
		}
		innerVal, _ := group.Get("hooks")
		if inner, ok := innerVal.([]interface{}); ok {
			var keptInner []interface{}
			for _, hookVal := range inner {
				hook, ok := hookVal.(*omap)
				if ok {
					if cmd, ok2 := hook.Get("command"); ok2 {
						if s, ok3 := cmd.(string); ok3 && isOurs(s) {
							removed++
							continue
						}
					}
				}
				keptInner = append(keptInner, hookVal)
			}
			if keptInner == nil {
				keptInner = []interface{}{}
			}
			group.Set("hooks", keptInner)
			if len(keptInner) == 0 {
				continue // drop the now-empty group
			}
		}
		keptGroups = append(keptGroups, groupVal)
	}
	if keptGroups == nil {
		keptGroups = []interface{}{}
	}
	return keptGroups, removed
}

// Install merges emote's Stop + Notification hooks into settings.json.
// Non-destructive and idempotent: unrelated keys and other hooks are
// preserved; any previous emote entries are replaced, not duplicated.
func Install(scope, mode string) string {
	path, err := settingsPath(scope)
	if err != nil {
		return fmt.Sprintf("Cannot install hooks: %v", err)
	}
	data, errMsg := readSettings(path)
	if errMsg != "" {
		return "Cannot install hooks: " + errMsg
	}

	emote := quotePath(emoteCommand())
	modeSuffix := ""
	if mode != "" {
		modeSuffix = " --mode " + mode
	}
	commands := map[string]string{
		"Stop":         emote + " hook stop" + modeSuffix,
		"Notification": emote + " hook notification",
	}

	hooksVal, ok := data.Get("hooks")
	if !ok {
		hooksVal = newOmap()
		data.Set("hooks", hooksVal)
	}
	hooksOM, ok := hooksVal.(*omap)
	if !ok {
		return fmt.Sprintf(
			"Cannot install hooks: %s has a non-object 'hooks' key — left untouched.", path)
	}

	for _, event := range HookEvents {
		groupsVal, ok := hooksOM.Get(event)
		if !ok {
			groupsVal = []interface{}{}
		}
		groups, ok := groupsVal.([]interface{})
		if !ok {
			return fmt.Sprintf(
				"Cannot install hooks: %s has a non-list hooks.%s — left untouched.",
				path, event)
		}
		groups, _ = stripEmoteEntries(groups) // replace any previous emote entry
		entry := newOmap()
		entry.Set("type", "command")
		entry.Set("command", commands[event])
		group := newOmap()
		group.Set("hooks", []interface{}{entry})
		groups = append(groups, group)
		hooksOM.Set(event, groups)
	}

	if err := writeSettings(path, data); err != nil {
		return fmt.Sprintf("Cannot install hooks: %v", err)
	}
	modeNote := ""
	if mode != "" {
		modeNote = fmt.Sprintf(" (mode: %s)", mode)
	}
	return fmt.Sprintf(
		"Installed emote hooks (Stop, Notification) into %s%s.\n"+
			"Claude Code will speak responses at the end of each turn. "+
			"Run 'emote uninstall-hooks' to remove.", path, modeNote)
}

// Uninstall removes only hook commands containing the marker string
// "emote hook".
func Uninstall(scope string) string {
	path, err := settingsPath(scope)
	if err != nil {
		return fmt.Sprintf("Cannot uninstall hooks: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Sprintf("No settings file at %s — nothing to uninstall.", path)
	}
	data, errMsg := readSettings(path)
	if errMsg != "" {
		return "Cannot uninstall hooks: " + errMsg
	}

	removed := 0
	if hooksVal, ok := data.Get("hooks"); ok {
		if hooksOM, ok := hooksVal.(*omap); ok {
			for _, event := range hooksOM.Keys() {
				groupsVal, _ := hooksOM.Get(event)
				if groups, ok := groupsVal.([]interface{}); ok {
					kept, n := stripEmoteEntries(groups)
					removed += n
					if len(kept) == 0 {
						hooksOM.Delete(event)
					} else {
						hooksOM.Set(event, kept)
					}
				}
			}
			if hooksOM.Len() == 0 {
				data.Delete("hooks")
			}
		}
	}

	if removed > 0 {
		if err := writeSettings(path, data); err != nil {
			return fmt.Sprintf("Cannot uninstall hooks: %v", err)
		}
		return fmt.Sprintf(
			"Removed %d emote hook(s) from %s. Other hooks left intact.", removed, path)
	}
	return fmt.Sprintf("No emote hooks found in %s — nothing removed.", path)
}

func installedEvents(path string) []string {
	data, errMsg := readSettings(path)
	if errMsg != "" || data == nil || data.Len() == 0 {
		return nil
	}
	hooksVal, ok := data.Get("hooks")
	if !ok {
		return nil
	}
	hooksOM, ok := hooksVal.(*omap)
	if !ok {
		return nil
	}
	var events []string
	for _, event := range hooksOM.Keys() {
		groupsVal, _ := hooksOM.Get(event)
		groups, ok := groupsVal.([]interface{})
		if !ok {
			continue
		}
	groupLoop:
		for _, groupVal := range groups {
			group, ok := groupVal.(*omap)
			if !ok {
				continue
			}
			innerVal, _ := group.Get("hooks")
			inner, _ := innerVal.([]interface{})
			for _, hookVal := range inner {
				if hook, ok := hookVal.(*omap); ok {
					if cmd, ok := hook.Get("command"); ok {
						if s, ok := cmd.(string); ok && isOurs(s) {
							events = append(events, event)
							break groupLoop
						}
					}
				}
			}
		}
	}
	return events
}

// Status reports, one line per scope, which emote hooks are installed where.
func Status() string {
	var lines []string
	for _, scope := range []string{"user", "project"} {
		path, err := settingsPath(scope)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%-8s error: %v", scope, err))
			continue
		}
		events := installedEvents(path)
		if len(events) > 0 {
			sorted := append([]string(nil), events...)
			sort.Strings(sorted)
			lines = append(lines, fmt.Sprintf("%-8s %s: %s",
				scope, path, strings.Join(sorted, ", ")))
		} else {
			lines = append(lines, fmt.Sprintf("%-8s %s: not installed", scope, path))
		}
	}
	return strings.Join(lines, "\n")
}

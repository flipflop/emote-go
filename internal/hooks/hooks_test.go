package hooks

// Port of ../roz-emote-cli/tests/test_hooks.py — settings.json
// install/uninstall and the Stop/Notification hook runtime. No network, no
// audio: the speak bridge is patched and quiet mode short-circuits before
// any player is touched. Uses a temp HOME so the real ~/.claude and ~/.config
// are never modified.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/extract"
)

// --- scaffolding -----------------------------------------------------------

type env struct {
	home    string
	project string
}

// tempHome isolates HOME and cwd in a temp directory.
func tempHome(t *testing.T) env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	project := filepath.Join(base, "project")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	// The test binary is not named "emote"; pin the launcher path like the
	// Python suite's deterministic _emote_command (absolute, marker-carrying).
	oldCmd := emoteCommand
	emoteCommand = func() string { return filepath.Join(home, "bin", "emote") }
	t.Cleanup(func() { emoteCommand = oldCmd })
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return env{home: home, project: project}
}

func (e env) settingsPath(scope string) string {
	if scope == "user" {
		return filepath.Join(e.home, ".claude", "settings.json")
	}
	return filepath.Join(e.project, ".claude", "settings.json")
}

func (e env) readSettings(t *testing.T, scope string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(e.settingsPath(scope))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func (e env) writeSettings(t *testing.T, data map[string]interface{}, scope string) {
	t.Helper()
	path := e.settingsPath(scope)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// emoteCommands returns all emote hook commands registered for an event.
func emoteCommands(data map[string]interface{}, event string) []string {
	var found []string
	hooks, _ := data["hooks"].(map[string]interface{})
	groups, _ := hooks[event].([]interface{})
	for _, g := range groups {
		group, _ := g.(map[string]interface{})
		inner, _ := group["hooks"].([]interface{})
		for _, h := range inner {
			hook, _ := h.(map[string]interface{})
			if cmd, _ := hook["command"].(string); strings.Contains(cmd, Marker) {
				found = append(found, cmd)
			}
		}
	}
	return found
}

func baseConfig(over func(*config.Config)) config.Config {
	cfg := config.Config{
		Mode:         "summarised",
		Voice:        nil,
		Emotion:      "auto",
		Interrupt:    true,
		Enabled:      true,
		Server:       "http://localhost:8123",
		MaxSentences: 3,
	}
	if over != nil {
		over(&cfg)
	}
	return cfg
}

// --- install / uninstall / status ------------------------------------------

func TestInstallCreatesSettingsWithBothEvents(t *testing.T) {
	e := tempHome(t)
	summary := Install("user", "")
	if !strings.Contains(summary, "Stop") {
		t.Errorf("summary %q missing Stop", summary)
	}
	data := e.readSettings(t, "user")
	stop := emoteCommands(data, "Stop")
	notif := emoteCommands(data, "Notification")
	if len(stop) != 1 || len(notif) != 1 {
		t.Fatalf("stop=%v notif=%v", stop, notif)
	}
	if !strings.Contains(stop[0], "emote hook stop") ||
		!strings.Contains(notif[0], "emote hook notification") {
		t.Errorf("commands wrong: %v %v", stop, notif)
	}
	// Command must reference the binary by absolute path.
	if !strings.HasPrefix(strings.TrimPrefix(stop[0], `"`), "/") {
		t.Errorf("command not absolute: %q", stop[0])
	}
}

func TestInstallSchemaShape(t *testing.T) {
	e := tempHome(t)
	Install("user", "")
	data := e.readSettings(t, "user")
	hooks := data["hooks"].(map[string]interface{})
	group := hooks["Stop"].([]interface{})[0].(map[string]interface{})
	if len(group) != 1 {
		t.Errorf("group keys = %v, want only 'hooks'", group)
	}
	entry := group["hooks"].([]interface{})[0].(map[string]interface{})
	if entry["type"] != "command" {
		t.Errorf("entry type = %v", entry["type"])
	}
	if !strings.Contains(entry["command"].(string), "emote hook stop") {
		t.Errorf("entry command = %v", entry["command"])
	}
}

func TestInstallPreservesUnrelatedKeysAndOtherHooks(t *testing.T) {
	e := tempHome(t)
	existing := map[string]interface{}{
		"model": "claude-fable-5[1m]",
		"theme": "dark",
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": "say done"},
				}},
			},
			"PostToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "log-it.sh"},
					},
				},
			},
		},
	}
	e.writeSettings(t, existing, "user")
	Install("user", "")
	data := e.readSettings(t, "user")
	if data["model"] != "claude-fable-5[1m]" || data["theme"] != "dark" {
		t.Errorf("unrelated keys touched: %v", data)
	}
	// Foreign Stop hook still present alongside ours.
	var commands []string
	for _, g := range data["hooks"].(map[string]interface{})["Stop"].([]interface{}) {
		for _, h := range g.(map[string]interface{})["hooks"].([]interface{}) {
			commands = append(commands, h.(map[string]interface{})["command"].(string))
		}
	}
	foundForeign := false
	for _, c := range commands {
		if c == "say done" {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Errorf("foreign Stop hook lost: %v", commands)
	}
	if len(emoteCommands(data, "Stop")) != 1 {
		t.Errorf("emote Stop commands: %v", emoteCommands(data, "Stop"))
	}
	// Unrelated event untouched.
	post := data["hooks"].(map[string]interface{})["PostToolUse"].([]interface{})
	cmd := post[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"]
	if cmd != "log-it.sh" {
		t.Errorf("PostToolUse touched: %v", cmd)
	}
}

func TestInstallTwiceIsIdempotent(t *testing.T) {
	e := tempHome(t)
	Install("user", "")
	first := e.readSettings(t, "user")
	Install("user", "")
	second := e.readSettings(t, "user")
	if !reflect.DeepEqual(first, second) {
		t.Errorf("install not idempotent:\n%v\n%v", first, second)
	}
	if len(emoteCommands(second, "Stop")) != 1 ||
		len(emoteCommands(second, "Notification")) != 1 {
		t.Errorf("duplicated entries: %v", second)
	}
}

func TestInstallWithModeFlag(t *testing.T) {
	e := tempHome(t)
	Install("user", "warnings")
	stop := emoteCommands(e.readSettings(t, "user"), "Stop")[0]
	if !strings.Contains(stop, "--mode warnings") {
		t.Errorf("stop command %q missing --mode warnings", stop)
	}
	// Re-install without mode replaces (not duplicates) the entry.
	Install("user", "")
	stops := emoteCommands(e.readSettings(t, "user"), "Stop")
	if len(stops) != 1 || strings.Contains(stops[0], "--mode") {
		t.Errorf("re-install: %v", stops)
	}
}

func TestInstallProjectScope(t *testing.T) {
	e := tempHome(t)
	Install("project", "")
	if _, err := os.Stat(e.settingsPath("project")); err != nil {
		t.Errorf("project settings missing: %v", err)
	}
	if _, err := os.Stat(e.settingsPath("user")); !os.IsNotExist(err) {
		t.Errorf("user settings unexpectedly created")
	}
	data := e.readSettings(t, "project")
	if len(emoteCommands(data, "Stop")) != 1 {
		t.Errorf("project Stop commands: %v", emoteCommands(data, "Stop"))
	}
}

func TestInstallRefusesToClobberInvalidJSON(t *testing.T) {
	e := tempHome(t)
	path := e.settingsPath("user")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary := Install("user", "")
	if !strings.Contains(summary, "not valid JSON") {
		t.Errorf("summary = %q", summary)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not json" {
		t.Errorf("invalid file clobbered: %q", raw)
	}
}

func TestUninstallRemovesOnlyEmote(t *testing.T) {
	e := tempHome(t)
	e.writeSettings(t, map[string]interface{}{
		"theme": "dark",
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": "say done"},
				}},
			},
		},
	}, "user")
	Install("user", "")
	summary := Uninstall("user")
	if !strings.Contains(summary, "Removed 2") {
		t.Errorf("summary = %q", summary)
	}
	data := e.readSettings(t, "user")
	if data["theme"] != "dark" {
		t.Errorf("theme touched: %v", data)
	}
	if len(emoteCommands(data, "Stop")) != 0 || len(emoteCommands(data, "Notification")) != 0 {
		t.Errorf("emote hooks remain: %v", data)
	}
	// The foreign hook survives; the emptied Notification list is gone.
	hooks := data["hooks"].(map[string]interface{})
	cmd := hooks["Stop"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"]
	if cmd != "say done" {
		t.Errorf("foreign hook lost: %v", cmd)
	}
	if _, ok := hooks["Notification"]; ok {
		t.Errorf("empty Notification list kept: %v", hooks)
	}
}

func TestUninstallRoundTripRestoresOriginal(t *testing.T) {
	e := tempHome(t)
	e.writeSettings(t, map[string]interface{}{
		"model": "opus",
		"hooks": map[string]interface{}{"PreToolUse": []interface{}{}},
	}, "user")
	Install("user", "")
	Uninstall("user")
	data := e.readSettings(t, "user")
	if data["model"] != "opus" {
		t.Errorf("model lost: %v", data)
	}
	hooks, _ := data["hooks"].(map[string]interface{})
	if _, ok := hooks["Stop"]; ok {
		t.Errorf("Stop kept: %v", hooks)
	}
	if _, ok := hooks["Notification"]; ok {
		t.Errorf("Notification kept: %v", hooks)
	}
}

func TestUninstallMissingFile(t *testing.T) {
	tempHome(t)
	if summary := Uninstall("user"); !strings.Contains(summary, "nothing to uninstall") {
		t.Errorf("summary = %q", summary)
	}
}

func TestUninstallWhenNotInstalled(t *testing.T) {
	e := tempHome(t)
	e.writeSettings(t, map[string]interface{}{"theme": "dark"}, "user")
	summary := Uninstall("user")
	if !strings.Contains(strings.ToLower(summary), "nothing removed") {
		t.Errorf("summary = %q", summary)
	}
	if data := e.readSettings(t, "user"); !reflect.DeepEqual(data,
		map[string]interface{}{"theme": "dark"}) {
		t.Errorf("settings modified: %v", data)
	}
}

func TestStatusReportsBothScopes(t *testing.T) {
	tempHome(t)
	report := Status()
	if got := strings.Count(report, "not installed"); got != 2 {
		t.Errorf("initial report: %q", report)
	}
	Install("user", "")
	report = Status()
	if !strings.Contains(report, "Notification, Stop") {
		t.Errorf("report after user install: %q", report)
	}
	if got := strings.Count(report, "not installed"); got != 1 {
		t.Errorf("report after user install: %q", report)
	}
	Install("project", "")
	if report = Status(); strings.Contains(report, "not installed") {
		t.Errorf("report after both installs: %q", report)
	}
}

// --- hook runtime: stop ----------------------------------------------------

func entryLine(t *testing.T, role string, content interface{}, msgID string) string {
	t.Helper()
	message := map[string]interface{}{"role": role, "content": content}
	if msgID != "" {
		message["id"] = msgID
	}
	typ := "user"
	if role == "assistant" {
		typ = "assistant"
	}
	raw, err := json.Marshal(map[string]interface{}{"type": typ, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type speakRecorder struct {
	calls []struct {
		utts    []extract.Utterance
		emotion string
		cfg     config.Config
	}
	err error
}

func (r *speakRecorder) speak(utts []extract.Utterance, emotion string, cfg config.Config) error {
	r.calls = append(r.calls, struct {
		utts    []extract.Utterance
		emotion string
		cfg     config.Config
	}{utts, emotion, cfg})
	return r.err
}

func (r *speakRecorder) spokenText(t *testing.T) string {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatal("speak was not called")
	}
	last := r.calls[len(r.calls)-1]
	var parts []string
	for _, u := range last.utts {
		parts = append(parts, u.Text)
	}
	return strings.Join(parts, "\n\n")
}

// patchBridges installs the same fakes the Python suite mock-patches in.
func patchBridges(t *testing.T) *speakRecorder {
	t.Helper()
	rec := &speakRecorder{}
	oldSpeak, oldExtract, oldClassify := speakFn, extractFn, classifyFn
	speakFn = rec.speak
	extractFn = func(text, mode string, cfg config.Config) []extract.Utterance {
		return []extract.Utterance{{Text: text, Kind: "prose"}}
	}
	classifyFn = func(text string) (string, string) { return "happy", "test" }
	t.Cleanup(func() {
		speakFn, extractFn, classifyFn = oldSpeak, oldExtract, oldClassify
	})
	return rec
}

func makeTranscript(t *testing.T, e env, lines []string) string {
	t.Helper()
	path := filepath.Join(e.project, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// multiTurnTranscript: one assistant turn is split across several JSONL lines
// (one content block each, same message.id), with thinking and tool_use
// blocks mixed in.
func multiTurnTranscript(t *testing.T, e env) string {
	return makeTranscript(t, e, []string{
		entryLine(t, "user", "run the tests", ""),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "hmm"}}, "m1"),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "text", "text": "Old reply."}}, "m1"),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "tool_use", "id": "t1", "name": "Bash",
				"input": map[string]interface{}{}}}, "m1"),
		entryLine(t, "user", []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "t1"}}, ""),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "ok"}}, "m2"),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "text", "text": "All tests pass."}}, "m2"),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "text", "text": "Ship it."}}, "m2"),
	})
}

func TestStopSpeaksLastAssistantMessageJoined(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	stdin := map[string]interface{}{"transcript_path": multiTurnTranscript(t, e)}
	if rc := RunHook("stop", stdin, baseConfig(nil)); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("speak called %d times", len(rec.calls))
	}
	if got := rec.spokenText(t); got != "All tests pass.\n\nShip it." {
		t.Errorf("spoken = %q", got)
	}
	if strings.Contains(rec.spokenText(t), "Old reply") {
		t.Errorf("old turn leaked")
	}
	if rec.calls[0].emotion != "happy" { // from the patched classifier
		t.Errorf("emotion = %q", rec.calls[0].emotion)
	}
}

func TestStopTrailingToolUseOnlyTurnFallsBack(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	path := makeTranscript(t, e, []string{
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "text", "text": "Real answer."}}, "m1"),
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "tool_use", "id": "t9", "name": "Bash",
				"input": map[string]interface{}{}}}, "m2"),
	})
	RunHook("stop", map[string]interface{}{"transcript_path": path}, baseConfig(nil))
	if got := rec.spokenText(t); got != "Real answer." {
		t.Errorf("spoken = %q", got)
	}
}

func TestStopPinnedEmotionBypassesClassifier(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	stdin := map[string]interface{}{"transcript_path": multiTurnTranscript(t, e)}
	RunHook("stop", stdin, baseConfig(func(c *config.Config) { c.Emotion = "sad" }))
	if rec.calls[0].emotion != "sad" {
		t.Errorf("emotion = %q", rec.calls[0].emotion)
	}
}

func TestStopLoopGuard(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	stdin := map[string]interface{}{
		"transcript_path":  multiTurnTranscript(t, e),
		"stop_hook_active": true,
	}
	if rc := RunHook("stop", stdin, baseConfig(nil)); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if len(rec.calls) != 0 {
		t.Errorf("speak called despite loop guard")
	}
}

func TestStopDisabledConfig(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	stdin := map[string]interface{}{"transcript_path": multiTurnTranscript(t, e)}
	rc := RunHook("stop", stdin, baseConfig(func(c *config.Config) { c.Enabled = false }))
	if rc != 0 || len(rec.calls) != 0 {
		t.Errorf("rc=%d calls=%d", rc, len(rec.calls))
	}
}

func TestStopMissingTranscriptReturnsZero(t *testing.T) {
	tempHome(t)
	rec := patchBridges(t)
	rc := RunHook("stop",
		map[string]interface{}{"transcript_path": "/nope/nowhere.jsonl"}, baseConfig(nil))
	if rc != 0 || len(rec.calls) != 0 {
		t.Errorf("rc=%d calls=%d", rc, len(rec.calls))
	}
}

func TestStopNoTranscriptPathReturnsZero(t *testing.T) {
	tempHome(t)
	rec := patchBridges(t)
	if rc := RunHook("stop", map[string]interface{}{}, baseConfig(nil)); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if len(rec.calls) != 0 {
		t.Errorf("speak called")
	}
}

func TestStopGarbageLinesAreSkipped(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	path := makeTranscript(t, e, []string{
		"not json at all",
		`{"half": true`,
		`"just a string"`,
		entryLine(t, "assistant", []interface{}{
			map[string]interface{}{"type": "text", "text": "Survived."}}, "m1"),
		"",
	})
	RunHook("stop", map[string]interface{}{"transcript_path": path}, baseConfig(nil))
	if got := rec.spokenText(t); got != "Survived." {
		t.Errorf("spoken = %q", got)
	}
}

func TestStopSpeakFailureStillReturnsZero(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	rec.err = fmt.Errorf("no speakers")
	stdin := map[string]interface{}{"transcript_path": multiTurnTranscript(t, e)}
	if rc := RunHook("stop", stdin, baseConfig(nil)); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
}

func TestUnknownKindReturnsZero(t *testing.T) {
	tempHome(t)
	rec := patchBridges(t)
	if rc := RunHook("wibble", map[string]interface{}{}, baseConfig(nil)); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if len(rec.calls) != 0 {
		t.Errorf("speak called")
	}
}

// --- dir overrides (hook events only) ---------------------------------------

func boolp(b bool) *bool { return &b }

// patchBridgesRecordingMode is patchBridges plus a recorder for the mode the
// extract bridge is invoked with (the effective hook-time mode).
func patchBridgesRecordingMode(t *testing.T) (*speakRecorder, *[]string) {
	t.Helper()
	rec := patchBridges(t)
	var modes []string
	extractFn = func(text, mode string, cfg config.Config) []extract.Utterance {
		modes = append(modes, mode)
		return []extract.Utterance{{Text: text, Kind: "prose"}}
	}
	return rec, &modes
}

func stopPayload(t *testing.T, e env, cwd string) map[string]interface{} {
	t.Helper()
	payload := map[string]interface{}{"transcript_path": multiTurnTranscript(t, e)}
	if cwd != "" {
		payload["cwd"] = cwd
	}
	return payload
}

func TestStopDirOverrideModeBeatsConfigMode(t *testing.T) {
	e := tempHome(t)
	_, modes := patchBridgesRecordingMode(t)
	cfg := baseConfig(func(c *config.Config) {
		c.Mode = "warnings"
		c.DirOverrides = map[string]config.DirOverride{e.project: {Mode: "verbose"}}
	})
	RunHook("stop", stopPayload(t, e, e.project), cfg)
	if !reflect.DeepEqual(*modes, []string{"verbose"}) {
		t.Errorf("modes = %v", *modes)
	}
}

func TestStopDirOverrideAppliesToSubdirectory(t *testing.T) {
	e := tempHome(t)
	_, modes := patchBridgesRecordingMode(t)
	sub := filepath.Join(e.project, "deep", "er")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig(func(c *config.Config) {
		c.DirOverrides = map[string]config.DirOverride{e.project: {Mode: "code"}}
	})
	RunHook("stop", stopPayload(t, e, sub), cfg)
	if !reflect.DeepEqual(*modes, []string{"code"}) {
		t.Errorf("modes = %v", *modes)
	}
}

func TestStopSiblingNamePrefixDoesNotMatch(t *testing.T) {
	e := tempHome(t)
	_, modes := patchBridgesRecordingMode(t)
	// override on <project>/a must not fire for <project>/ab
	a := filepath.Join(e.project, "a")
	ab := filepath.Join(e.project, "ab")
	if err := os.MkdirAll(ab, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig(func(c *config.Config) {
		c.Mode = "summarised"
		c.DirOverrides = map[string]config.DirOverride{a: {Mode: "verbose"}}
	})
	RunHook("stop", stopPayload(t, e, ab), cfg)
	if !reflect.DeepEqual(*modes, []string{"summarised"}) {
		t.Errorf("modes = %v", *modes)
	}
}

func TestStopDirOverrideDisabledSilences(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	cfg := baseConfig(func(c *config.Config) {
		c.DirOverrides = map[string]config.DirOverride{e.project: {Enabled: boolp(false)}}
	})
	RunHook("stop", stopPayload(t, e, e.project), cfg)
	if len(rec.calls) != 0 {
		t.Errorf("speak called despite dir override off")
	}
}

func TestStopDirOverrideOnCannotReviveDisabledConfig(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	cfg := baseConfig(func(c *config.Config) {
		c.Enabled = false
		c.DirOverrides = map[string]config.DirOverride{e.project: {Enabled: boolp(true)}}
	})
	RunHook("stop", stopPayload(t, e, e.project), cfg)
	if len(rec.calls) != 0 {
		t.Errorf("dir override revived a disabled config")
	}
}

func TestStopMissingCwdMeansNoOverride(t *testing.T) {
	e := tempHome(t)
	rec, modes := patchBridgesRecordingMode(t)
	cfg := baseConfig(func(c *config.Config) {
		c.Mode = "warnings"
		c.DirOverrides = map[string]config.DirOverride{
			e.project: {Enabled: boolp(false), Mode: "verbose"}}
	})
	RunHook("stop", stopPayload(t, e, ""), cfg)
	if len(rec.calls) != 1 {
		t.Fatalf("speak calls = %d", len(rec.calls))
	}
	if !reflect.DeepEqual(*modes, []string{"warnings"}) {
		t.Errorf("modes = %v", *modes)
	}
}

func TestStopUnmatchedCwdMeansNoOverride(t *testing.T) {
	e := tempHome(t)
	rec := patchBridges(t)
	cfg := baseConfig(func(c *config.Config) {
		c.DirOverrides = map[string]config.DirOverride{"/nowhere/else": {Enabled: boolp(false)}}
	})
	RunHook("stop", stopPayload(t, e, e.project), cfg)
	if len(rec.calls) != 1 {
		t.Errorf("speak calls = %d", len(rec.calls))
	}
}

func TestNotificationRespectsDirOverride(t *testing.T) {
	e := tempHome(t)
	rec := patchSpeakOnly(t)
	cfg := baseConfig(func(c *config.Config) {
		c.DirOverrides = map[string]config.DirOverride{e.project: {Enabled: boolp(false)}}
	})
	RunHook("notification",
		map[string]interface{}{"message": "hello", "cwd": e.project}, cfg)
	if len(rec.calls) != 0 {
		t.Errorf("notification spoke despite dir override off")
	}
	// but an unmatched cwd still speaks
	cfg2 := baseConfig(func(c *config.Config) {
		c.DirOverrides = map[string]config.DirOverride{"/nowhere": {Enabled: boolp(false)}}
	})
	RunHook("notification",
		map[string]interface{}{"message": "hello", "cwd": e.project}, cfg2)
	if len(rec.calls) != 1 {
		t.Errorf("speak calls = %d", len(rec.calls))
	}
}

// --- quiet mode ------------------------------------------------------------

// Quiet short-circuits inside the default speaker, before any player is
// consulted, so this passes with no engine at all.
func TestQuietPrintsInsteadOfTTS(t *testing.T) {
	tempHome(t)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	speakErr := speakDefault(
		[]extract.Utterance{{Text: "hello there", Kind: "prose"}},
		"happy",
		baseConfig(func(c *config.Config) { c.Quiet = true }))
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if speakErr != nil {
		t.Fatalf("speakDefault error: %v", speakErr)
	}
	if !strings.Contains(string(out), "hello there") {
		t.Errorf("stdout = %q", out)
	}
}

// --- hook runtime: notification --------------------------------------------

func patchSpeakOnly(t *testing.T) *speakRecorder {
	t.Helper()
	rec := &speakRecorder{}
	old := speakFn
	speakFn = rec.speak
	t.Cleanup(func() { speakFn = old })
	return rec
}

func TestNotificationSpeaksMessageCalmly(t *testing.T) {
	tempHome(t)
	rec := patchSpeakOnly(t)
	rc := RunHook("notification",
		map[string]interface{}{"message": "Claude needs your permission to run a command"},
		baseConfig(nil))
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("speak called %d times", len(rec.calls))
	}
	if rec.calls[0].emotion != "calm" {
		t.Errorf("emotion = %q", rec.calls[0].emotion)
	}
	if !strings.Contains(rec.calls[0].utts[0].Text, "permission") {
		t.Errorf("text = %q", rec.calls[0].utts[0].Text)
	}
}

func TestNotificationLongMessageTruncated(t *testing.T) {
	tempHome(t)
	rec := patchSpeakOnly(t)
	RunHook("notification",
		map[string]interface{}{"message": strings.Repeat("blah ", 200)}, baseConfig(nil))
	text := rec.calls[0].utts[0].Text
	if utf8.RuneCountInString(text) > NotificationMaxChars {
		t.Errorf("len = %d > %d", utf8.RuneCountInString(text), NotificationMaxChars)
	}
}

func TestNotificationDisabled(t *testing.T) {
	tempHome(t)
	rec := patchSpeakOnly(t)
	rc := RunHook("notification", map[string]interface{}{"message": "hi"},
		baseConfig(func(c *config.Config) { c.Enabled = false }))
	if rc != 0 || len(rec.calls) != 0 {
		t.Errorf("rc=%d calls=%d", rc, len(rec.calls))
	}
}

func TestNotificationEmptyMessage(t *testing.T) {
	tempHome(t)
	rec := patchSpeakOnly(t)
	if rc := RunHook("notification", map[string]interface{}{}, baseConfig(nil)); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if len(rec.calls) != 0 {
		t.Errorf("speak called")
	}
}

// --- logging ----------------------------------------------------------------

func TestLogWritesOneLine(t *testing.T) {
	tempHome(t)
	logf("hello log")
	content, err := os.ReadFile(logPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "\n") != 1 {
		t.Errorf("content = %q", content)
	}
	if !strings.Contains(string(content), "hello log") {
		t.Errorf("content = %q", content)
	}
}

func TestLogRingTruncatesAt200KB(t *testing.T) {
	tempHome(t)
	line := strings.Repeat("x", 1000)
	for i := 0; i < 250; i++ { // ~250 KB
		logf(line)
	}
	st, err := os.Stat(logPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > LogMaxBytes {
		t.Errorf("size = %d > %d", st.Size(), LogMaxBytes)
	}
	// Newest content survives, and the file still starts at a line boundary.
	raw, err := os.ReadFile(logPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, line+"\n") {
		t.Errorf("newest line lost")
	}
	first := strings.SplitN(text, "\n", 2)[0]
	if !regexp.MustCompile(`^\d{4}-`).MatchString(first) {
		t.Errorf("first line %q not at a timestamp boundary", first)
	}
}

func TestLogNeverFailsOnUnwritableDir(t *testing.T) {
	old := logPath
	logPath = func() string { return "/nonexistent-root/x/y.log" }
	defer func() { logPath = old }()
	logf("should not fail") // must not panic
}

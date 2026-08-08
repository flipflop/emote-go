package main

// Integration tests for the `emote here` subcommand, the un-baked
// install-hooks default, and the full hook-time precedence chain through the
// real cmdHook entrypoint:
//
//   mode:    dir override > baked --mode > EMOTE_MODE > config file > "summarised"
//   enabled: any OFF wins (dir override, EMOTE_ENABLED, config "enabled")
//
// Temp config dir, temp HOME, synthetic stdin hook payloads. No audio: the
// hooks.Player bridge is replaced with a recorder.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/extract"
	"github.com/flipflop/emote-go/internal/hooks"
)

// hereEnv isolates EMOTE_* env, HOME and cwd in temp directories.
func hereEnv(t *testing.T) (project string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(base, "cfg")
	home := filepath.Join(base, "home")
	project = filepath.Join(base, "project")
	for _, d := range []string{cfgDir, home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("EMOTE_CONFIG_DIR", cfgDir)
	t.Setenv("HOME", home)
	os.Unsetenv("EMOTE_MODE")
	os.Unsetenv("EMOTE_ENABLED")
	os.Unsetenv("EMOTE_SERVER")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return project
}

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	rc := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return rc, string(out)
}

// --- `emote here` -----------------------------------------------------------

func TestCmdHereOffRegistersResolvedCwd(t *testing.T) {
	project := hereEnv(t)
	rc, out := captureStdout(t, func() int { return cmdHere([]string{"off"}) })
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(out, "enabled = false for "+project) {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(out, "hook events only") {
		t.Errorf("scope note missing: %q", out)
	}
	got := config.Load().DirOverrides
	if len(got) != 1 || got[project].Enabled == nil || *got[project].Enabled {
		t.Errorf("DirOverrides = %+v", got)
	}
}

func TestCmdHereMergeShowAndParentMatch(t *testing.T) {
	project := hereEnv(t)
	captureStdout(t, func() int { return cmdHere([]string{"off"}) })
	rc, out := captureStdout(t, func() int { return cmdHere([]string{"mode", "important"}) })
	if rc != 0 || !strings.Contains(out, `mode = "important" for `+project) {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	sub := filepath.Join(project, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	rc, out = captureStdout(t, func() int { return cmdHere(nil) })
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	wantHeader := fmt.Sprintf("override for %s (registered at %s):", sub, project)
	if !strings.Contains(out, wantHeader) {
		t.Errorf("out = %q, want header %q", out, wantHeader)
	}
	if !strings.Contains(out, "enabled = false") || !strings.Contains(out, `mode = "important"`) {
		t.Errorf("out = %q", out)
	}
}

func TestCmdHereShowNoOverride(t *testing.T) {
	project := hereEnv(t)
	rc, out := captureStdout(t, func() int { return cmdHere(nil) })
	if rc != 0 || !strings.Contains(out, "no directory override for "+project) {
		t.Errorf("rc=%d out=%q", rc, out)
	}
}

func TestCmdHereClearIsExactKeyOnly(t *testing.T) {
	project := hereEnv(t)
	captureStdout(t, func() int { return cmdHere([]string{"off"}) })
	sub := filepath.Join(project, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	rc, out := captureStdout(t, func() int { return cmdHere([]string{"clear"}) })
	if rc != 0 || !strings.Contains(out, "no override registered at") {
		t.Errorf("rc=%d out=%q", rc, out)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	rc, out = captureStdout(t, func() int { return cmdHere([]string{"clear"}) })
	if rc != 0 || !strings.Contains(out, "cleared override for "+project) {
		t.Errorf("rc=%d out=%q", rc, out)
	}
	if got := config.Load().DirOverrides; len(got) != 0 {
		t.Errorf("overrides remain: %+v", got)
	}
}

func TestCmdHereUsageErrors(t *testing.T) {
	hereEnv(t)
	if rc, _ := captureStdout(t, func() int { return cmdHere([]string{"bogus"}) }); rc != 1 {
		t.Errorf("bogus action rc = %d", rc)
	}
	if rc, _ := captureStdout(t, func() int { return cmdHere([]string{"mode", "bogus"}) }); rc != 1 {
		t.Errorf("bogus mode rc = %d", rc)
	}
}

// --- install-hooks no longer bakes --mode unless explicit -------------------

func readStopCommand(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	stop := data["hooks"].(map[string]interface{})["Stop"].([]interface{})
	group := stop[len(stop)-1].(map[string]interface{})
	entry := group["hooks"].([]interface{})[0].(map[string]interface{})
	return entry["command"].(string)
}

func TestCmdInstallHooksDefaultUnbaked(t *testing.T) {
	hereEnv(t)
	if _, err := config.SetValue("mode", "verbose"); err != nil { // must NOT leak
		t.Fatal(err)
	}
	rc, _ := captureStdout(t, func() int { return cmdInstallHooks(nil) })
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	cmd := readStopCommand(t)
	if !strings.Contains(cmd, " hook stop") || strings.Contains(cmd, "--mode") {
		t.Errorf("default install baked a mode: %q", cmd)
	}
}

func TestCmdInstallHooksExplicitModeBaked(t *testing.T) {
	hereEnv(t)
	rc, _ := captureStdout(t, func() int {
		return cmdInstallHooks([]string{"--mode", "warnings"})
	})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if cmd := readStopCommand(t); !strings.Contains(cmd, "--mode warnings") {
		t.Errorf("explicit mode not baked: %q", cmd)
	}
}

// --- hook-time precedence through the real cmdHook entrypoint ---------------

type playerRecorder struct {
	calls []config.Config
}

func patchPlayer(t *testing.T) *playerRecorder {
	t.Helper()
	rec := &playerRecorder{}
	old := hooks.Player
	hooks.Player = func(utts []extract.Utterance, emotion string, cfg config.Config) error {
		rec.calls = append(rec.calls, cfg)
		return nil
	}
	t.Cleanup(func() { hooks.Player = old })
	return rec
}

func writeTranscript(t *testing.T, project string) string {
	t.Helper()
	line, err := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"role": "assistant", "id": "m1",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done."}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "transcript.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func feedStdin(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
}

func fireHook(t *testing.T, project string, argv []string, cwd string) {
	t.Helper()
	payload := map[string]interface{}{"transcript_path": writeTranscript(t, project)}
	if cwd != "" {
		payload["cwd"] = cwd
	}
	feedStdin(t, payload)
	if rc := cmdHook(argv); rc != 0 {
		t.Fatalf("hook rc = %d", rc)
	}
}

func TestCmdHookModePrecedence(t *testing.T) {
	project := hereEnv(t)
	rec := patchPlayer(t)
	if _, err := config.SetValue("mode", "important"); err != nil {
		t.Fatal(err)
	}

	// config file beats the built-in default
	fireHook(t, project, []string{"stop"}, "")
	// EMOTE_MODE beats config file
	t.Setenv("EMOTE_MODE", "code")
	fireHook(t, project, []string{"stop"}, "")
	// baked --mode beats EMOTE_MODE
	fireHook(t, project, []string{"stop", "--mode", "warnings"}, "")
	// dir override beats baked --mode
	if _, err := config.UpdateDirOverride(project, nil, "verbose"); err != nil {
		t.Fatal(err)
	}
	fireHook(t, project, []string{"stop", "--mode", "warnings"}, project)

	want := []string{"important", "code", "warnings", "verbose"}
	if len(rec.calls) != len(want) {
		t.Fatalf("player calls = %d, want %d", len(rec.calls), len(want))
	}
	for i, mode := range want {
		if rec.calls[i].Mode != mode {
			t.Errorf("call %d mode = %q, want %q", i, rec.calls[i].Mode, mode)
		}
	}
}

func TestCmdHookEnabledAnyOffWins(t *testing.T) {
	project := hereEnv(t)
	rec := patchPlayer(t)

	// EMOTE_ENABLED=0 silences even with config enabled
	t.Setenv("EMOTE_ENABLED", "0")
	fireHook(t, project, []string{"stop"}, "")
	if len(rec.calls) != 0 {
		t.Fatalf("spoke despite EMOTE_ENABLED=0")
	}

	// truthy env cannot revive a config-file off
	t.Setenv("EMOTE_ENABLED", "1")
	if _, err := config.SetValue("enabled", "false"); err != nil {
		t.Fatal(err)
	}
	fireHook(t, project, []string{"stop"}, "")
	if len(rec.calls) != 0 {
		t.Fatalf("truthy env revived a config off")
	}

	// dir override off silences a fully-enabled setup
	if _, err := config.SetValue("enabled", "true"); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := config.UpdateDirOverride(project, &off, ""); err != nil {
		t.Fatal(err)
	}
	fireHook(t, project, []string{"stop"}, project)
	if len(rec.calls) != 0 {
		t.Fatalf("spoke despite dir override off")
	}

	// all gates permitting speaks
	on := true
	if _, err := config.UpdateDirOverride(project, &on, ""); err != nil {
		t.Fatal(err)
	}
	fireHook(t, project, []string{"stop"}, project)
	if len(rec.calls) != 1 {
		t.Fatalf("player calls = %d, want 1", len(rec.calls))
	}
}

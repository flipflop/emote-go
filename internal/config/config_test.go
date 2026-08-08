package config_test

// Port of ../roz-emote-cli/tests/test_config.py: defaults, file merge,
// env overrides, set/coerce, plus cross-binary file-format compatibility.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flipflop/emote-go/internal/config"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("EMOTE_CONFIG_DIR", dir)
	t.Setenv("EMOTE_SERVER", "")
	t.Setenv("EMOTE_MODE", "")
	os.Unsetenv("EMOTE_SERVER")
	os.Unsetenv("EMOTE_MODE")
	os.Unsetenv("EMOTE_ENABLED")
	return dir
}

func write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(config.Path(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsWhenNoFile(t *testing.T) {
	setup(t)
	cfg := config.Load()
	if !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load() = %+v, want defaults", cfg)
	}
	if cfg.Mode != "summarised" || cfg.Voice == nil || *cfg.Voice != "jane" ||
		!cfg.Interrupt || !cfg.Enabled ||
		cfg.Server != "http://localhost:8123" || cfg.MaxSentences != 3 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestFileValuesMergeOverDefaults(t *testing.T) {
	setup(t)
	write(t, `{"mode": "verbose", "voice": "nova", "unknown_key": 1}`)
	cfg := config.Load()
	if cfg.Mode != "verbose" {
		t.Errorf("mode = %q", cfg.Mode)
	}
	if cfg.Voice == nil || *cfg.Voice != "nova" {
		t.Errorf("voice = %v", cfg.Voice)
	}
	if cfg.Server != "http://localhost:8123" { // default kept
		t.Errorf("server = %q", cfg.Server)
	}
}

func TestCorruptFileFallsBackToDefaults(t *testing.T) {
	setup(t)
	write(t, "{not json!!")
	if cfg := config.Load(); !reflect.DeepEqual(cfg, config.Defaults()) {
		t.Errorf("Load() = %+v, want defaults", cfg)
	}
}

func TestEnvOverrides(t *testing.T) {
	setup(t)
	write(t, `{"mode": "verbose", "server": "http://filehost:1"}`)
	t.Setenv("EMOTE_SERVER", "http://envhost:9")
	t.Setenv("EMOTE_MODE", "warnings")
	t.Setenv("EMOTE_ENABLED", "0")
	cfg := config.Load()
	if cfg.Server != "http://envhost:9" || cfg.Mode != "warnings" || cfg.Enabled {
		t.Errorf("env overrides not applied: %+v", cfg)
	}
}

func TestEmoteEnabledTruthyAndFalsy(t *testing.T) {
	setup(t)
	cases := []struct {
		raw      string
		expected bool
	}{
		{"0", false}, {"false", false}, {"no", false}, {"", false},
		{"1", true}, {"true", true},
	}
	for _, c := range cases {
		t.Setenv("EMOTE_ENABLED", c.raw)
		if got := config.Load().Enabled; got != c.expected {
			t.Errorf("EMOTE_ENABLED=%q -> %v, want %v", c.raw, got, c.expected)
		}
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	setup(t)
	cfg := config.Load()
	cfg.Mode = "important"
	cfg.MaxSentences = 5
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	again := config.Load()
	if again.Mode != "important" || again.MaxSentences != 5 {
		t.Errorf("roundtrip lost values: %+v", again)
	}
	// file contains exactly the known keys
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]interface{}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"mode", "voice", "emotion", "interrupt", "enabled",
		"server", "max_sentences"}
	if len(onDisk) != len(wantKeys) {
		t.Errorf("on-disk keys = %v", onDisk)
	}
	for _, k := range wantKeys {
		if _, ok := onDisk[k]; !ok {
			t.Errorf("missing key %q on disk", k)
		}
	}
}

// The persisted file must be byte-identical to what the Python emote writes
// (same key order, indent=2, trailing newline) so binaries can be swapped.
func TestSaveMatchesPythonFileFormat(t *testing.T) {
	setup(t)
	if err := config.Save(config.Defaults()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "mode": "summarised",
  "voice": "jane",
  "emotion": "auto",
  "interrupt": true,
  "enabled": true,
  "server": "http://localhost:8123",
  "max_sentences": 3
}
`
	if string(raw) != want {
		t.Errorf("file format mismatch:\n got: %q\nwant: %q", raw, want)
	}
}

func TestSetValueCoercions(t *testing.T) {
	setup(t)
	if v, err := config.SetValue("enabled", "false"); err != nil || v != false {
		t.Errorf("enabled false: %v, %v", v, err)
	}
	if v, err := config.SetValue("interrupt", "on"); err != nil || v != true {
		t.Errorf("interrupt on: %v, %v", v, err)
	}
	if v, err := config.SetValue("max_sentences", "7"); err != nil || v != 7 {
		t.Errorf("max_sentences 7: %v, %v", v, err)
	}
	cfg := config.Load()
	if cfg.Enabled || !cfg.Interrupt || cfg.MaxSentences != 7 {
		t.Errorf("persisted coercions wrong: %+v", cfg)
	}
}

func TestSetValueVoiceNull(t *testing.T) {
	setup(t)
	if _, err := config.SetValue("voice", "nova"); err != nil {
		t.Fatal(err)
	}
	if v := config.Load().Voice; v == nil || *v != "nova" {
		t.Errorf("voice = %v", v)
	}
	if _, err := config.SetValue("voice", "null"); err != nil {
		t.Fatal(err)
	}
	if v := config.Load().Voice; v != nil {
		t.Errorf("voice = %v, want nil", *v)
	}
}

func TestSetValueIgnoresEnvOverridesWhenPersisting(t *testing.T) {
	setup(t)
	t.Setenv("EMOTE_MODE", "warnings") // env must not leak into the file
	if _, err := config.SetValue("voice", "ash"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]interface{}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["mode"] != "summarised" || onDisk["voice"] != "ash" {
		t.Errorf("on disk: %v", onDisk)
	}
}

func TestSetValueRejectsBadInput(t *testing.T) {
	setup(t)
	if _, err := config.SetValue("nope", "x"); err == nil {
		t.Error("unknown key accepted")
	}
	if _, err := config.SetValue("enabled", "maybe"); err == nil {
		t.Error("bad bool accepted")
	}
}

func TestPathsLiveUnderConfigDir(t *testing.T) {
	dir := setup(t)
	if config.Path() != filepath.Join(dir, "config.json") {
		t.Errorf("Path() = %q", config.Path())
	}
	if config.PidfilePath() != filepath.Join(dir, "afplay.pid") {
		t.Errorf("PidfilePath() = %q", config.PidfilePath())
	}
	if config.LogPath() != filepath.Join(dir, "emote.log") {
		t.Errorf("LogPath() = %q", config.LogPath())
	}
}

// --- dir_overrides ---------------------------------------------------------

func boolp(b bool) *bool { return &b }

func TestDirOverridesRoundtripAndByteFormat(t *testing.T) {
	setup(t)
	if _, err := config.UpdateDirOverride("/a/b", boolp(false), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := config.UpdateDirOverride("/a/b", nil, "verbose"); err != nil {
		t.Fatal(err)
	}
	if _, err := config.UpdateDirOverride("/z", boolp(true), ""); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	want := map[string]config.DirOverride{
		"/a/b": {Enabled: boolp(false), Mode: "verbose"},
		"/z":   {Enabled: boolp(true)},
	}
	if !reflect.DeepEqual(cfg.DirOverrides, want) {
		t.Errorf("DirOverrides = %+v", cfg.DirOverrides)
	}
	// Byte format must match the Python implementation exactly (sorted keys,
	// enabled-then-mode value order, indent=2, trailing newline).
	wantFile := `{
  "mode": "summarised",
  "voice": "jane",
  "emotion": "auto",
  "interrupt": true,
  "enabled": true,
  "server": "http://localhost:8123",
  "max_sentences": 3,
  "dir_overrides": {
    "/a/b": {
      "enabled": false,
      "mode": "verbose"
    },
    "/z": {
      "enabled": true
    }
  }
}
`
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != wantFile {
		t.Errorf("file format mismatch:\n got: %q\nwant: %q", raw, wantFile)
	}
	// A save/load round trip must be byte-stable.
	if err := config.Save(config.Load()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(config.Path())
	if string(raw) != wantFile {
		t.Errorf("round trip not byte-stable:\n got: %q", raw)
	}
}

func TestEmptyDirOverridesOmittedFromFile(t *testing.T) {
	setup(t)
	write(t, `{"mode": "verbose", "dir_overrides": {}}`)
	if err := config.Save(config.Load()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(config.Path())
	var onDisk map[string]interface{}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["dir_overrides"]; ok {
		t.Errorf("empty dir_overrides persisted: %v", onDisk)
	}
	if onDisk["mode"] != "verbose" {
		t.Errorf("mode = %v", onDisk["mode"])
	}
}

func TestMalformedDirOverridesDropped(t *testing.T) {
	setup(t)
	write(t, `{"dir_overrides": {
		"relative/path": {"enabled": false},
		"/ok/": {"enabled": "yes", "mode": 3},
		"/x": "nope",
		"/keep": {"mode": "important", "junk": 1}
	}}`)
	cfg := config.Load()
	want := map[string]config.DirOverride{
		"/keep": {Mode: "important"},
		"/ok":   {},
	}
	if !reflect.DeepEqual(cfg.DirOverrides, want) {
		t.Errorf("DirOverrides = %+v, want %+v", cfg.DirOverrides, want)
	}
}

func TestMatchDirOverrideComponentPrefix(t *testing.T) {
	ovs := map[string]config.DirOverride{
		"/a/b": {Mode: "verbose"},
		"/a":   {Mode: "code"},
	}
	cases := []struct {
		path    string
		want    string
		matched bool
	}{
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/bc", "/a", true}, // component boundary: /a/bc is under /a, NOT /a/b
		{"/ab", "", false},    // /ab is not under /a at all
		{"/other", "", false},
		{"/a/b/deep/est", "/a/b", true}, // longest match wins
		{"/a/b/", "/a/b", true},         // trailing slash tolerated
	}
	for _, c := range cases {
		key, _, ok := config.MatchDirOverride(ovs, c.path)
		if ok != c.matched || key != c.want {
			t.Errorf("Match(%q) = (%q, %v), want (%q, %v)",
				c.path, key, ok, c.want, c.matched)
		}
	}
	// root override applies everywhere
	root := map[string]config.DirOverride{"/": {Enabled: boolp(false)}}
	if key, _, ok := config.MatchDirOverride(root, "/any/where"); !ok || key != "/" {
		t.Errorf("root match = (%q, %v)", key, ok)
	}
}

func TestClearDirOverride(t *testing.T) {
	setup(t)
	if _, err := config.UpdateDirOverride("/a/b", boolp(false), ""); err != nil {
		t.Fatal(err)
	}
	if removed, _ := config.ClearDirOverride("/a/b/child"); removed {
		t.Error("cleared a non-registered child path")
	}
	if removed, _ := config.ClearDirOverride("/a/b"); !removed {
		t.Error("exact key not cleared")
	}
	if removed, _ := config.ClearDirOverride("/a/b"); removed {
		t.Error("double clear reported removal")
	}
	if got := config.Load().DirOverrides; len(got) != 0 {
		t.Errorf("overrides remain: %+v", got)
	}
}

func TestHookEnabledAllGatesMustPermit(t *testing.T) {
	setup(t)
	cases := []struct {
		fileEnabled bool
		env         string // "" means unset
		expected    bool
	}{
		{true, "", true},
		{true, "1", true},
		{true, "0", false},
		{false, "", false},
		{false, "1", false}, // truthy env cannot re-enable a config off
		{false, "0", false},
	}
	for _, c := range cases {
		write(t, fmt.Sprintf(`{"enabled": %v}`, c.fileEnabled))
		os.Unsetenv("EMOTE_ENABLED")
		if c.env != "" {
			t.Setenv("EMOTE_ENABLED", c.env)
		}
		if got := config.HookEnabled(); got != c.expected {
			t.Errorf("file=%v env=%q -> %v, want %v",
				c.fileEnabled, c.env, got, c.expected)
		}
	}
	os.Unsetenv("EMOTE_ENABLED")
}

// Cross-direction compatibility: a file written by the Python emote (any key
// order, extra keys) loads correctly.
func TestLoadsPythonWrittenFile(t *testing.T) {
	setup(t)
	write(t, `{
  "voice": null,
  "mode": "code",
  "emotion": "happy",
  "interrupt": false,
  "enabled": true,
  "server": "http://box:9999",
  "max_sentences": 9,
  "dir_overrides": {
    "/proj/x/": {
      "mode": "warnings",
      "enabled": false
    }
  }
}
`)
	cfg := config.Load()
	if cfg.Voice != nil || cfg.Mode != "code" || cfg.Emotion != "happy" ||
		cfg.Interrupt || !cfg.Enabled || cfg.Server != "http://box:9999" ||
		cfg.MaxSentences != 9 {
		t.Errorf("python-written file mishandled: %+v", cfg)
	}
	want := map[string]config.DirOverride{
		"/proj/x": {Enabled: boolp(false), Mode: "warnings"},
	}
	if !reflect.DeepEqual(cfg.DirOverrides, want) {
		t.Errorf("dir_overrides mishandled: %+v", cfg.DirOverrides)
	}
}

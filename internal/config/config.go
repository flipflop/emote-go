// Package config ports emote_cli/config.py: JSON config at
// ~/.config/emote/config.json, defaults, env overrides, set-value coercions.
//
// The file format is byte-compatible with the Python emote (same keys, same
// key order, 2-space indent, trailing newline, "voice": null for the emotion
// bank), so users can switch binaries without migration.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DirOverride is a partial per-directory override, HOOK events only (see
// `emote here`). Field order (enabled, mode) matches the canonical key order
// the Python emote persists.
type DirOverride struct {
	Enabled *bool  `json:"enabled,omitempty"` // nil -> not overridden
	Mode    string `json:"mode,omitempty"`    // "" -> not overridden
}

// Config mirrors the Python DEFAULTS dict; field order matches its key order
// so the persisted JSON is identical.
type Config struct {
	Mode         string  `json:"mode"`
	Voice        *string `json:"voice"` // nil -> per-emotion voice bank
	Emotion      string  `json:"emotion"`
	Interrupt    bool    `json:"interrupt"`
	Enabled      bool    `json:"enabled"`
	Server       string  `json:"server"`
	MaxSentences int     `json:"max_sentences"`

	// DirOverrides maps absolute paths to partial overrides. Omitted from the
	// file when empty (byte-compat with pre-override files); encoding/json
	// sorts map keys, matching the Python side's sorted persistence.
	DirOverrides map[string]DirOverride `json:"dir_overrides,omitempty"`

	// Quiet is runtime-only (set by the CLI --quiet flag, consumed by hooks);
	// it is never persisted and never read from the file, matching the Python
	// config dict's ad-hoc "quiet" key.
	Quiet bool `json:"-"`
}

// Keys settable via SetValue, mirroring DEFAULTS membership.
var knownKeys = map[string]bool{
	"mode": true, "voice": true, "emotion": true, "interrupt": true,
	"enabled": true, "server": true, "max_sentences": true,
}

var (
	boolKeys = map[string]bool{"interrupt": true, "enabled": true}
	intKeys  = map[string]bool{"max_sentences": true}
	falsy    = map[string]bool{"0": true, "false": true, "no": true, "off": true, "": true}
	truthy   = map[string]bool{"1": true, "true": true, "yes": true, "on": true}
)

// Defaults returns the default configuration (Python DEFAULTS).
func Defaults() Config {
	voice := "jane"
	return Config{
		Mode:         "summarised",
		Voice:        &voice,
		Emotion:      "auto",
		Interrupt:    true,
		Enabled:      true,
		Server:       "http://localhost:8123",
		MaxSentences: 3,
	}
}

// Dir returns the config directory (EMOTE_CONFIG_DIR overrides; used by tests).
func Dir() string {
	if d := os.Getenv("EMOTE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, ".config", "emote")
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "config.json") }

// PidfilePath returns the playback pidfile path.
func PidfilePath() string { return filepath.Join(Dir(), "afplay.pid") }

// LogPath returns the emote log path.
func LogPath() string { return filepath.Join(Dir(), "emote.log") }

// loadFile merges defaults with the config file only (no env) — the basis for
// persisting. Missing or corrupt files fall back to defaults; unknown keys
// are dropped; each known key is merged independently so one malformed value
// cannot discard the rest of the file.
func loadFile() Config {
	cfg := Defaults()
	data, err := os.ReadFile(Path())
	if err != nil {
		return cfg
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return cfg
	}
	if v, ok := raw["mode"]; ok {
		_ = json.Unmarshal(v, &cfg.Mode)
	}
	if v, ok := raw["voice"]; ok {
		_ = json.Unmarshal(v, &cfg.Voice)
	}
	if v, ok := raw["emotion"]; ok {
		_ = json.Unmarshal(v, &cfg.Emotion)
	}
	if v, ok := raw["interrupt"]; ok {
		_ = json.Unmarshal(v, &cfg.Interrupt)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &cfg.Enabled)
	}
	if v, ok := raw["server"]; ok {
		_ = json.Unmarshal(v, &cfg.Server)
	}
	if v, ok := raw["max_sentences"]; ok {
		_ = json.Unmarshal(v, &cfg.MaxSentences)
	}
	if v, ok := raw["dir_overrides"]; ok {
		cfg.DirOverrides = normalizeOverrides(v)
	}
	return cfg
}

// cleanOverrideKey strips trailing slashes ("/" stays "/"), matching Python.
func cleanOverrideKey(path string) string {
	key := strings.TrimRight(path, "/")
	if key == "" {
		key = "/"
	}
	return key
}

// normalizeOverrides mirrors Python _normalize_overrides: absolute-path keys
// only (trailing slash stripped), values limited to a bool "enabled" and a
// non-empty string "mode"; malformed entries are dropped, never errored on.
// Returns nil when nothing survives so empty maps are omitted on save.
func normalizeOverrides(raw json.RawMessage) map[string]DirOverride {
	var entries map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	out := map[string]DirOverride{}
	for path, v := range entries {
		if !strings.HasPrefix(path, "/") {
			continue
		}
		var inner map[string]json.RawMessage
		if json.Unmarshal(v, &inner) != nil {
			continue
		}
		override := DirOverride{}
		if e, ok := inner["enabled"]; ok {
			var b bool
			if json.Unmarshal(e, &b) == nil {
				override.Enabled = &b
			}
		}
		if m, ok := inner["mode"]; ok {
			var s string
			if json.Unmarshal(m, &s) == nil {
				override.Mode = s
			}
		}
		out[cleanOverrideKey(path)] = override
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Load returns the effective config: defaults <- config file <- EMOTE_* env.
func Load() Config {
	cfg := loadFile()
	if v := os.Getenv("EMOTE_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("EMOTE_MODE"); v != "" {
		cfg.Mode = v
	}
	if v, ok := os.LookupEnv("EMOTE_ENABLED"); ok {
		cfg.Enabled = !falsy[strings.ToLower(strings.TrimSpace(v))]
	}
	return cfg
}

// Save persists the known keys of cfg to the config file, matching the
// Python file format byte-for-byte (indent=2, trailing newline, no HTML
// escaping — Python's json.dump leaves & < > literal).
func Save(cfg Config) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil { // Encode appends the trailing \n
		return err
	}
	return os.WriteFile(Path(), buf.Bytes(), 0o644)
}

// HookEnabled is the enabled gate for HOOK events: any OFF wins — the config
// file AND the EMOTE_ENABLED env must BOTH permit (a truthy env cannot
// re-enable a config-file off). Explicit CLI speak invocations never consult
// this. Dir overrides AND on top (see hooks.RunHook).
func HookEnabled() bool {
	envOK := true
	if v, ok := os.LookupEnv("EMOTE_ENABLED"); ok {
		envOK = !falsy[strings.ToLower(strings.TrimSpace(v))]
	}
	return loadFile().Enabled && envOK
}

// MatchDirOverride finds the LONGEST path-prefix match on path components:
// an override registered at /a/b applies to /a/b and /a/b/c, never to /a/bc.
// Returns (registered path, override, true) or ("", zero, false).
func MatchDirOverride(overrides map[string]DirOverride, path string) (string, DirOverride, bool) {
	if len(overrides) == 0 || path == "" {
		return "", DirOverride{}, false
	}
	candidate := cleanOverrideKey(path)
	best := ""
	found := false
	for key := range overrides {
		if key == "/" || candidate == key || strings.HasPrefix(candidate, key+"/") {
			if !found || len(key) > len(best) {
				best, found = key, true
			}
		}
	}
	if !found {
		return "", DirOverride{}, false
	}
	return best, overrides[best], true
}

// UpdateDirOverride merges a partial override for an absolute path into the
// config file (nil enabled / empty mode leave that half untouched). Returns
// the resulting override for the path.
func UpdateDirOverride(path string, enabled *bool, mode string) (DirOverride, error) {
	key := cleanOverrideKey(path)
	cfg := loadFile()
	if cfg.DirOverrides == nil {
		cfg.DirOverrides = map[string]DirOverride{}
	}
	override := cfg.DirOverrides[key]
	if enabled != nil {
		override.Enabled = enabled
	}
	if mode != "" {
		override.Mode = mode
	}
	cfg.DirOverrides[key] = override
	return override, Save(cfg)
}

// ClearDirOverride removes the override registered exactly at path.
// Reports whether anything was removed.
func ClearDirOverride(path string) (bool, error) {
	key := cleanOverrideKey(path)
	cfg := loadFile()
	if _, ok := cfg.DirOverrides[key]; !ok {
		return false, nil
	}
	delete(cfg.DirOverrides, key)
	if len(cfg.DirOverrides) == 0 {
		cfg.DirOverrides = nil
	}
	return true, Save(cfg)
}

// Coerce converts a raw CLI string value to the right type for key:
// bools accept 1/true/yes/on and 0/false/no/off (case-insensitive),
// max_sentences becomes an int, and voice "", "null" or "none" becomes nil.
func Coerce(key, raw string) (interface{}, error) {
	s := strings.TrimSpace(raw)
	if boolKeys[key] {
		low := strings.ToLower(s)
		if truthy[low] {
			return true, nil
		}
		if falsy[low] {
			return false, nil
		}
		return nil, fmt.Errorf("%s expects true/false, got %q", key, raw)
	}
	if intKeys[key] {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("%s expects an integer, got %q", key, raw)
		}
		return n, nil
	}
	if key == "voice" {
		switch strings.ToLower(s) {
		case "", "null", "none":
			return (*string)(nil), nil
		}
		return s, nil
	}
	return s, nil
}

// SetValue coerces and sets one config key in the file, ignoring env
// overrides (they must not leak into the persisted file). Returns the value.
func SetValue(key, raw string) (interface{}, error) {
	if !knownKeys[key] {
		return nil, fmt.Errorf("unknown config key %q", key)
	}
	value, err := Coerce(key, raw)
	if err != nil {
		return nil, err
	}
	cfg := loadFile()
	switch key {
	case "mode":
		cfg.Mode = value.(string)
	case "voice":
		switch v := value.(type) {
		case *string:
			cfg.Voice = v
		case string:
			s := v
			cfg.Voice = &s
		}
	case "emotion":
		cfg.Emotion = value.(string)
	case "interrupt":
		cfg.Interrupt = value.(bool)
	case "enabled":
		cfg.Enabled = value.(bool)
	case "server":
		cfg.Server = value.(string)
	case "max_sentences":
		cfg.MaxSentences = value.(int)
	}
	if err := Save(cfg); err != nil {
		return nil, err
	}
	return value, nil
}

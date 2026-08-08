package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/hooks"
	"github.com/flipflop/emote-go/internal/logo"
	"github.com/flipflop/emote-go/internal/provision"
)

// settableConfigKeys are the keys `emote config KEY VALUE` may set
// (contract: mode, voice, interrupt, enabled).
var settableConfigKeys = []string{"mode", "voice", "interrupt", "enabled"}

// ---------------------------------------------------------------- hook ----

// cmdHook: `emote hook stop|notification` — Claude Code hook entrypoints.
// ALWAYS exits 0 (a broken speaker must never fail a Claude session) and
// never provisions; audio is fire-and-forget via the detached player.
func cmdHook(argv []string) int {
	kind, mode := "", ""
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--mode" && i+1 < len(argv):
			mode = argv[i+1]
			i++
		case argv[i] == "stop" || argv[i] == "notification":
			kind = argv[i]
		}
	}
	if kind == "" {
		// Mirrors Python: the argparse SystemExit is swallowed, still exit 0.
		fmt.Fprintln(os.Stderr, "emote hook: expected 'stop' or 'notification'")
		return 0
	}

	raw := ""
	if !stdinIsTTY() {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			raw = string(data)
		}
	}
	payload := map[string]interface{}{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &payload) // bad JSON -> empty payload
	}

	cfg := config.Load()
	// Hook enabled gate: any OFF wins (config file AND EMOTE_ENABLED must
	// both permit). Dir overrides are applied later, inside RunHook.
	cfg.Enabled = config.HookEnabled()
	if mode != "" && oneOf(mode, modes) {
		cfg.Mode = mode // baked --mode beats EMOTE_MODE/config; dir override beats it
	}
	return hooks.RunHook(kind, payload, cfg) // contract: always 0
}

// ----------------------------------------------- install/uninstall-hooks --

const installHooksUsage = "usage: emote install-hooks [--scope {user,project}] [--mode MODE]"
const uninstallHooksUsage = "usage: emote uninstall-hooks [--scope {user,project}]"

func parseScopeMode(argv []string, usage string, wantMode bool) (scope, mode string, code int, ok bool) {
	scope = "user"
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--scope":
			if i+1 >= len(argv) {
				return "", "", usageErr(usage, "argument --scope: expected one argument"), false
			}
			scope = argv[i+1]
			if scope != "user" && scope != "project" {
				return "", "", usageErr(usage, fmt.Sprintf(
					"argument --scope: invalid choice: %q (choose from user, project)", scope)), false
			}
			i++
		case "--mode":
			if !wantMode {
				return "", "", usageErr(usage, fmt.Sprintf("unrecognized arguments: %s", argv[i])), false
			}
			if i+1 >= len(argv) {
				return "", "", usageErr(usage, "argument --mode: expected one argument"), false
			}
			mode = argv[i+1]
			if !oneOf(mode, modes) {
				return "", "", usageErr(usage, fmt.Sprintf(
					"argument --mode: invalid choice: %q", mode)), false
			}
			i++
		default:
			return "", "", usageErr(usage, fmt.Sprintf("unrecognized arguments: %s", argv[i])), false
		}
	}
	return scope, mode, 0, true
}

func cmdInstallHooks(argv []string) int {
	scope, mode, code, ok := parseScopeMode(argv, installHooksUsage, true)
	if !ok {
		return code
	}
	// Bake --mode into the installed command ONLY when passed explicitly;
	// otherwise the command is plain `... hook stop` and mode resolves at
	// fire time (dir override > baked > EMOTE_MODE > config > "summarised").
	fmt.Println(hooks.Install(scope, mode))
	return 0
}

func cmdUninstallHooks(argv []string) int {
	scope, _, code, ok := parseScopeMode(argv, uninstallHooksUsage, false)
	if !ok {
		return code
	}
	fmt.Println(hooks.Uninstall(scope))
	return 0
}

// -------------------------------------------------------------- doctor ----

func cmdDoctor(_ []string) int {
	fmt.Println(logo.Render(logo.ShouldColor(os.Stdout), version))
	fmt.Println()
	cfg := config.Load()

	serverUp := httpHealth(cfg.Server, 2*time.Second)
	if serverUp {
		fmt.Printf("  server     ok — %s\n", cfg.Server)
	} else {
		fmt.Println("  server     DOWN")
		fmt.Printf("             %s\n", serverHelp)
	}

	provisioned := provision.Provisioned()
	if provisioned {
		fmt.Printf("  engine     provisioned — %s\n", provision.CacheDir())
	} else {
		fmt.Println("  engine     not provisioned — run `emote provision` for on-device speech")
	}

	if afplay, err := exec.LookPath("afplay"); err == nil {
		fmt.Printf("  playback   oto (built-in); afplay fallback — %s\n", afplay)
	} else {
		fmt.Println("  playback   oto (built-in); afplay NOT FOUND")
	}

	fmt.Printf("  hooks      %s\n", hooks.Status())

	if len(cfg.DirOverrides) > 0 {
		matched, _, _ := config.MatchDirOverride(cfg.DirOverrides, resolvedCwd())
		keys := make([]string, 0, len(cfg.DirOverrides))
		for k := range cfg.DirOverrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		prefix := "  overrides  "
		for _, path := range keys {
			override := cfg.DirOverrides[path]
			var parts []string
			if override.Enabled != nil {
				parts = append(parts, fmt.Sprintf("enabled=%v", *override.Enabled))
			}
			if override.Mode != "" {
				parts = append(parts, "mode="+override.Mode)
			}
			desc := strings.Join(parts, " ")
			if desc == "" {
				desc = "(empty)"
			}
			marker := ""
			if path == matched {
				marker = "   <- applies here"
			}
			fmt.Printf("%s%s: %s%s\n", prefix, path, desc, marker)
			prefix = strings.Repeat(" ", 13)
		}
	} else {
		fmt.Println("  overrides  (none)")
	}

	voice := "emotion bank"
	if cfg.Voice != nil && *cfg.Voice != "" {
		voice = *cfg.Voice
	}
	fmt.Printf("  enabled    %v   mode: %s   voice: %s\n", cfg.Enabled, cfg.Mode, voice)
	fmt.Printf("  config     %s\n", config.Path())

	backendUp := serverUp || provisioned
	if backendUp && cfg.Enabled {
		fmt.Println("\n  speaking a test line…")
		err := speak([]spoken{{Text: "Emote is up and running", Emotion: "happy"}}, cfg, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  test line failed: %v\n", err)
		}
	}
	if backendUp {
		return 0
	}
	return 2
}

// -------------------------------------------------------------- config ----

func cmdConfig(argv []string) int {
	cfg := config.Load()
	if len(argv) == 0 {
		fmt.Printf("# %s\n", config.Path())
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	key := argv[0]
	if !oneOf(key, settableConfigKeys) {
		fmt.Fprintf(os.Stderr, "emote: unknown config key %q (settable: mode, voice, interrupt, enabled)\n", key)
		return 1
	}
	if len(argv) == 1 {
		// Single-key get: marshal the struct once and pick the field.
		var asMap map[string]interface{}
		data, _ := json.Marshal(cfg)
		_ = json.Unmarshal(data, &asMap)
		out, _ := json.Marshal(asMap[key])
		fmt.Println(string(out))
		return 0
	}
	value := argv[1]
	if key == "mode" && !oneOf(value, modes) {
		fmt.Fprintln(os.Stderr, "emote: mode must be one of verbose, summarised, important, warnings, code")
		return 1
	}
	newVal, err := config.SetValue(key, value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "emote: %v\n", err)
		return 1
	}
	out, _ := json.Marshal(newVal)
	fmt.Printf("%s = %s\n", key, string(out))
	return 0
}

// ---------------------------------------------------------------- here ----

const hereUsage = "usage: emote here [on|off|mode <mode>|clear]"
const hereNote = "(applies to Claude Code hook events only)"

// resolvedCwd returns the symlink-resolved current directory (the key
// `emote here` registers, mirroring Python's os.path.realpath(os.getcwd())).
func resolvedCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = r
	}
	return cwd
}

// cmdHere: `emote here` — per-directory override for HOOK events, keyed by
// the symlink-resolved cwd. Explicit CLI speak invocations always speak.
func cmdHere(argv []string) int {
	cwd := resolvedCwd()
	if cwd == "" {
		fmt.Fprintln(os.Stderr, "emote: cannot determine current directory")
		return 1
	}
	jsonStr := func(v interface{}) string {
		out, _ := json.Marshal(v)
		return string(out)
	}
	if len(argv) == 0 {
		overrides := config.Load().DirOverrides
		matched, override, ok := config.MatchDirOverride(overrides, cwd)
		if !ok {
			fmt.Printf("no directory override for %s\n", cwd)
			return 0
		}
		fmt.Printf("override for %s (registered at %s):\n", cwd, matched)
		if override.Enabled != nil {
			fmt.Printf("  enabled = %s\n", jsonStr(*override.Enabled))
		}
		if override.Mode != "" {
			fmt.Printf("  mode = %s\n", jsonStr(override.Mode))
		}
		return 0
	}
	action := argv[0]
	switch {
	case (action == "on" || action == "off") && len(argv) == 1:
		value := action == "on"
		if _, err := config.UpdateDirOverride(cwd, &value, ""); err != nil {
			fmt.Fprintf(os.Stderr, "emote: %v\n", err)
			return 1
		}
		fmt.Printf("enabled = %s for %s\n", jsonStr(value), cwd)
		fmt.Println(hereNote)
		return 0
	case action == "mode":
		if len(argv) != 2 || !oneOf(argv[1], modes) {
			fmt.Fprintf(os.Stderr, "emote: here mode expects one of %s\n",
				strings.Join(modes, ", "))
			return 1
		}
		if _, err := config.UpdateDirOverride(cwd, nil, argv[1]); err != nil {
			fmt.Fprintf(os.Stderr, "emote: %v\n", err)
			return 1
		}
		fmt.Printf("mode = %s for %s\n", jsonStr(argv[1]), cwd)
		fmt.Println(hereNote)
		return 0
	case action == "clear" && len(argv) == 1:
		removed, err := config.ClearDirOverride(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "emote: %v\n", err)
			return 1
		}
		if removed {
			fmt.Printf("cleared override for %s\n", cwd)
		} else {
			fmt.Printf("no override registered at %s\n", cwd)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, hereUsage)
	fmt.Fprintf(os.Stderr, "emote: error: unrecognized arguments: %s\n",
		strings.Join(argv, " "))
	return 1
}

// ------------------------------------------------------------- on / off ---

func cmdToggle(enabled bool) int {
	if _, err := config.SetValue("enabled", fmt.Sprintf("%v", enabled)); err != nil {
		fmt.Fprintf(os.Stderr, "emote: %v\n", err)
		return 1
	}
	if enabled {
		fmt.Println("emote is on")
	} else {
		fmt.Println("emote is off")
	}
	return 0
}

// ----------------------------------------------------------- provision ----

// cmdProvision: interactive first-run model + reference download to
// ~/.cache/emote (sha256-verified, resumable). Never reached from hooks.
func cmdProvision(_ []string) int {
	if provision.Provisioned() {
		fmt.Printf("emote: engine already provisioned — %s\n", provision.CacheDir())
		return 0
	}
	if err := provision.Provision(true); err != nil {
		fmt.Fprintf(os.Stderr, "emote: %v\n", err)
		return 1
	}
	fmt.Println("emote: provisioning complete — on-device speech is ready")
	return 0
}

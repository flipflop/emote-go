// Command emote speaks text (especially Claude Code responses) aloud with
// emotional tone. Go port of the Python emote-cli (see ../../DESIGN.md and
// ../roz-emote-cli/DESIGN.md — the latter is authoritative for the CLI
// surface, modes, sentiment mapping, hook semantics, config schema and exit
// codes).
//
// Exit codes: 0 ok; 1 usage error; 2 no TTS backend reachable (printed
// nicely, never a traceback). `emote hook ...` ALWAYS exits 0.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/extract"
	"github.com/flipflop/emote-go/internal/hooks"
	"github.com/flipflop/emote-go/internal/logo"
	"github.com/flipflop/emote-go/internal/play"
	"github.com/flipflop/emote-go/internal/sentiment"
)

const version = "0.1.0"

var modes = []string{"verbose", "summarised", "important", "warnings", "code"}
var emotions = []string{"auto", "neutral", "happy", "sad", "calm", "excited"}

// spoken is one utterance headed for the TTS backend: cleaned text plus the
// emotion chosen for it (pinned or classified).
type spoken struct {
	Text    string
	Emotion string
}

func main() {
	// Notifications yield to speech already in progress (never interrupt a
	// reading mid-sentence to announce "waiting for input").
	hooks.SpeechBusy = func() bool {
		_, busy := play.CurrentlyPlaying(config.PidfilePath())
		return busy
	}
	// Hooks fire-and-forget through the same tiered speak path as the CLI.
	hooks.Player = func(utts []extract.Utterance, emotion string, cfg config.Config) error {
		items := make([]spoken, 0, len(utts))
		for _, u := range utts {
			items = append(items, spoken{Text: u.Text, Emotion: emotion})
		}
		return speak(items, cfg, cfg.Quiet)
	}
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	if len(argv) > 0 {
		cmd, rest := argv[0], argv[1:]
		switch cmd {
		case "hook":
			return cmdHook(rest)
		case "install-hooks":
			return cmdInstallHooks(rest)
		case "uninstall-hooks":
			return cmdUninstallHooks(rest)
		case "doctor":
			return cmdDoctor(rest)
		case "config":
			return cmdConfig(rest)
		case "here":
			return cmdHere(rest)
		case "logo":
			fmt.Println(logo.Render(logo.ShouldColor(os.Stdout), version))
			return 0
		case "on":
			return cmdToggle(true)
		case "off":
			return cmdToggle(false)
		case "provision":
			return cmdProvision(rest)
		case "__play": // hidden: detached playback child (see internal/play)
			return play.RunChild(rest)
		}
	}
	return cmdSpeak(argv)
}

// ---------------------------------------------------------------- speak ----

type speakOpts struct {
	text     []string
	mode     string
	emotion  string
	voice    string
	voiceSet bool
	quiet    bool
	digest   string
}

const speakUsage = `usage: emote [-h] [--mode {verbose,summarised,important,warnings,code}]
             [--emotion {auto,neutral,happy,sad,calm,excited}] [--voice VOICE]
             [-q] [--digest {llm}] [--version]
             [TEXT ...]`

const speakHelp = speakUsage + `

Speak text aloud with emotional tone (emote-chat TTS or the embedded engine).

positional arguments:
  TEXT                  text to speak (or pipe it on stdin)

options:
  -h, --help            show this help message and exit
  --mode MODE           extraction mode (default: config)
  --emotion EMOTION     pin an emotion, or 'auto' for the sentiment classifier
  --voice VOICE         pin a single voice (bypasses emotion bank)
  -q, --quiet           print '[emotion] text' lines instead of playing audio
  --digest llm          reserved (v1 stub)
  --version             show program's version number and exit

Subcommands: hook, install-hooks, uninstall-hooks, doctor, config, here,
logo, on, off, provision`

// usageErr prints an argparse-style usage error and returns exit code 1.
func usageErr(usage, msg string) int {
	fmt.Fprintln(os.Stderr, usage)
	fmt.Fprintf(os.Stderr, "emote: error: %s\n", msg)
	return 1
}

func oneOf(value string, choices []string) bool {
	for _, c := range choices {
		if value == c {
			return true
		}
	}
	return false
}

// parseSpeakArgs allows flags and positional TEXT to interleave (argparse
// behavior), so `emote hello --quiet` works like `emote --quiet hello`.
func parseSpeakArgs(argv []string) (*speakOpts, int, bool) {
	opts := &speakOpts{}
	i := 0
	for i < len(argv) {
		arg := argv[i]
		name, inline, hasInline := arg, "", false
		if j := strings.IndexByte(arg, '='); j >= 0 && strings.HasPrefix(arg, "--") {
			name, inline, hasInline = arg[:j], arg[j+1:], true
		}
		value := func() (string, int, bool) {
			if hasInline {
				return inline, 0, true
			}
			if i+1 >= len(argv) {
				return "", 0, false
			}
			return argv[i+1], 1, true
		}
		switch name {
		case "--":
			opts.text = append(opts.text, argv[i+1:]...)
			return opts, 0, true
		case "-h", "--help":
			fmt.Println(speakHelp)
			return nil, 0, false
		case "--version":
			fmt.Printf("emote %s\n", version)
			return nil, 0, false
		case "-q", "--quiet":
			opts.quiet = true
		case "--mode":
			v, skip, ok := value()
			if !ok {
				return nil, usageErr(speakUsage, "argument --mode: expected one argument"), false
			}
			if !oneOf(v, modes) {
				return nil, usageErr(speakUsage, fmt.Sprintf(
					"argument --mode: invalid choice: %q (choose from %s)", v, strings.Join(modes, ", "))), false
			}
			opts.mode = v
			i += skip
		case "--emotion":
			v, skip, ok := value()
			if !ok {
				return nil, usageErr(speakUsage, "argument --emotion: expected one argument"), false
			}
			if !oneOf(v, emotions) {
				return nil, usageErr(speakUsage, fmt.Sprintf(
					"argument --emotion: invalid choice: %q (choose from %s)", v, strings.Join(emotions, ", "))), false
			}
			opts.emotion = v
			i += skip
		case "--voice":
			v, skip, ok := value()
			if !ok {
				return nil, usageErr(speakUsage, "argument --voice: expected one argument"), false
			}
			opts.voice, opts.voiceSet = v, true
			i += skip
		case "--digest":
			v, skip, ok := value()
			if !ok {
				return nil, usageErr(speakUsage, "argument --digest: expected one argument"), false
			}
			if v != "llm" {
				return nil, usageErr(speakUsage, fmt.Sprintf(
					"argument --digest: invalid choice: %q (choose from llm)", v)), false
			}
			opts.digest = v
			i += skip
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" && len(arg) > 1 {
				return nil, usageErr(speakUsage, fmt.Sprintf("unrecognized arguments: %s", arg)), false
			}
			opts.text = append(opts.text, arg)
		}
		i++
	}
	return opts, 0, true
}

func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func cmdSpeak(argv []string) int {
	opts, code, ok := parseSpeakArgs(argv)
	if !ok {
		return code // help/version printed, or usage error
	}
	cfg := config.Load()

	text := strings.TrimSpace(strings.Join(opts.text, " "))
	if text == "" && !stdinIsTTY() {
		data, _ := io.ReadAll(os.Stdin)
		text = strings.TrimSpace(string(data))
	}
	if text == "" {
		return usageErr(speakUsage, "nothing to speak — pass TEXT or pipe stdin")
	}

	if opts.digest == "llm" {
		fmt.Fprintln(os.Stderr, "emote: --digest llm is coming soon")
	}

	mode := opts.mode
	if mode == "" {
		mode = cfg.Mode
	}
	emotionSetting := opts.emotion
	if emotionSetting == "" {
		emotionSetting = cfg.Emotion
	}
	if emotionSetting == "" {
		emotionSetting = "auto"
	}
	if opts.voiceSet {
		v := opts.voice
		cfg.Voice = &v
	}

	utterances := extract.Extract(text, mode, cfg.MaxSentences)
	items := make([]spoken, 0, len(utterances))
	for _, u := range utterances {
		emotion := emotionSetting
		if emotion == "auto" {
			emotion, _ = sentiment.Classify(u.Text)
		}
		items = append(items, spoken{Text: u.Text, Emotion: emotion})
	}

	if err := speak(items, cfg, opts.quiet); err != nil {
		fmt.Fprintf(os.Stderr, "emote: %v\n", err)
		return 2
	}
	return 0
}

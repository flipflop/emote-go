package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flipflop/emote-go/internal/config"
	"github.com/flipflop/emote-go/internal/engine"
	"github.com/flipflop/emote-go/internal/play"
	"github.com/flipflop/emote-go/internal/provision"
	"github.com/flipflop/emote-go/internal/speech"
)

// serverHelp matches the Python tts.SERVER_HELP wording.
const serverHelp = "emote-chat server is not running — start it with run.sh in your " +
	"emote-chat checkout (e.g. ../roz-emote-chat/run.sh)"

// noBackendHelp is shown when neither the server nor the embedded engine is
// available (backend tier 3 in DESIGN.md).
const noBackendHelp = "no TTS backend available — run `emote provision` to download\n" +
	"       the on-device voice, or start the emote-chat server (run.sh in\n" +
	"       your emote-chat checkout, github.com/flipflop/emote-chat)"

var errNoBackend = errors.New(noBackendHelp)

// httpHealth GETs {server}/api/health; true iff 2xx within timeout.
func httpHealth(server string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(strings.TrimRight(server, "/") + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// httpFetch POSTs {server}/api/tts with {"text","emotion","voice"?} (voice
// omitted when null/empty, per the emote-chat API) and streams the
// response to a temp WAV; returns its path.
func httpFetch(server, text, emotion, voice string) (string, error) {
	payload := map[string]string{"text": text, "emotion": emotion}
	if voice != "" {
		payload["voice"] = voice
	}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(strings.TrimRight(server, "/")+"/api/tts",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%s", serverHelp)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("TTS server error: HTTP %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	tmp, err := os.CreateTemp("", "emote-*.wav")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("%s", serverHelp)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// ttsRun is one merged same-emotion utterance run (one TTS call).
type ttsRun struct {
	emotion string
	text    string
}

// mergeRuns ports tts.merge_runs: adjacent same-emotion utterances merge
// into one text with sentence spacing; texts get terminal punctuation.
func mergeRuns(items []spoken) []ttsRun {
	var runs []ttsRun
	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		r := []rune(text)
		if !strings.ContainsRune(".!?…:;,", r[len(r)-1]) {
			text += "."
		}
		emotion := it.Emotion
		if emotion == "" {
			emotion = "neutral"
		}
		if len(runs) > 0 && runs[len(runs)-1].emotion == emotion {
			runs[len(runs)-1].text += " " + text
		} else {
			runs = append(runs, ttsRun{emotion, text})
		}
	}
	return runs
}

// engineFetch renders the runs through the embedded engine (tier 2):
// per-sentence pipeline via internal/speech, one temp WAV per run.
// A pinned voice other than the default "jane" is used as the reference
// name directly; "" or "jane" selects the emotion bank (jane-*-synth refs).
func engineFetch(runs []ttsRun, voice string) ([]string, error) {
	eng, err := engine.New(provision.CacheDir())
	if err != nil {
		return nil, err
	}
	defer eng.Close()

	var paths []string
	cleanup := func() {
		for _, p := range paths {
			os.Remove(p)
		}
	}
	for _, r := range runs {
		var audio engine.Audio
		if voice != "" && voice != "jane" {
			audio, err = speech.RenderRef(eng, r.text, voice, nil)
		} else {
			audio, err = speech.Render(eng, r.text, r.emotion)
		}
		if err != nil {
			cleanup()
			return nil, err
		}
		tmp, err := os.CreateTemp("", "emote-*.wav")
		if err != nil {
			cleanup()
			return nil, err
		}
		tmp.Close()
		if err := speech.WriteWAV(tmp.Name(), audio); err != nil {
			os.Remove(tmp.Name())
			cleanup()
			return nil, err
		}
		paths = append(paths, tmp.Name())
	}
	return paths, nil
}

// speak voices the items (or prints "[emotion] text" lines when quiet).
// Backend tiers per DESIGN.md: healthy :8123 server → HTTP /api/tts; else
// the embedded engine when provisioned; else errNoBackend (exit code 2 for
// interactive runs — hooks swallow it). Honors cfg.Interrupt via the shared
// pidfile (~/.config/emote/afplay.pid, cross-compatible with Python emote).
func speak(items []spoken, cfg config.Config, quiet bool) error {
	runs := mergeRuns(items)
	if len(runs) == 0 {
		return nil
	}
	if quiet {
		for _, r := range runs {
			fmt.Printf("[%s] %s\n", r.emotion, r.text)
		}
		return nil
	}

	pidfile := config.PidfilePath()
	if _, playing := play.CurrentlyPlaying(pidfile); playing {
		if cfg.Interrupt {
			play.Stop(pidfile)
		} else {
			return nil // something is playing and we must not interrupt
		}
	}

	voice := ""
	if cfg.Voice != nil {
		voice = *cfg.Voice
	}

	var paths []string
	var err error
	switch {
	case httpHealth(cfg.Server, 2*time.Second):
		for _, r := range runs {
			var p string
			p, err = httpFetch(cfg.Server, r.text, r.emotion, voice)
			if err != nil {
				for _, done := range paths {
					os.Remove(done)
				}
				return err
			}
			paths = append(paths, p)
		}
	case provision.Provisioned():
		paths, err = engineFetch(runs, voice)
		if err != nil {
			return err
		}
	default:
		return errNoBackend
	}

	exe, err := os.Executable()
	if err == nil {
		_, err = play.Start(exe, pidfile, paths)
	}
	if err != nil {
		for _, p := range paths {
			os.Remove(p)
		}
		return err
	}
	return nil
}

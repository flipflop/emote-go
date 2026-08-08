// Package engine wraps sherpa-onnx pocket-tts with the locked P2 ship
// configuration (DESIGN.md, "Engine ship config"): fp32 bundle by default with
// the mixed int8+fp32-lm_flow bundle as low-disk fallback, num_steps 5,
// temperature 0.7, seed 42, 4 threads, SilenceScale 0.6, and a per-call
// max_frames guard of 3*chars+40.
//
// The engine performs zero-shot voice cloning from reference wavs resolved by
// name from the provisioned refs directory. It renders one text unit per call;
// the per-sentence pipeline (split, trim, fade, gap stitching) lives in
// internal/speech.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Locked ship configuration, per DESIGN.md / P1.7. Do not make these
// user-tunable without revisiting the P1 findings: SilenceScale 0 is a
// landmine (the C API maps it to 0.2 and squashes every pause), temp 0.3
// flattens prosody, steps 10 buys nothing.
const (
	numSteps     = 5
	temperature  = 0.7
	seed         = 42
	numThreads   = 4
	silenceScale = 0.6
	// maxRefLenSec bounds the reference audio fed to the voice encoder.
	// The Mimi encoder ignores material past ~10 s in-graph (P1.5 §3); 30 s
	// merely guarantees the whole ref is offered, matching the P1 renders.
	maxRefLenSec = 30
	// Engine sentence-split config is left at upstream defaults (P1.5 §4:
	// min_char_in_sentence=1 is a trap; the merge bug is bypassed by the
	// per-sentence pipeline in internal/speech, not by tuning these).
	minCharInSentence = 30
	maxCharInSentence = 200
	// maxFramesCap is the engine's own hard limit (~40 s of audio).
	maxFramesCap = 500
)

// Model bundle directories probed under <root>/models, in preference order.
// fp32 is the ship default; mixed (int8 + fp32 lm_flow) is the low-disk
// fallback tier; plain int8 is accepted as a last resort so a partially
// provisioned cache still speaks (with reduced pitch variance, P1.5 §2).
var modelBundleDirs = []string{
	"sherpa-onnx-pocket-tts-2026-01-26",      // fp32 (ship default)
	"pocket-mixed-flowfp32",                  // int8 + fp32 lm_flow fallback
	"sherpa-onnx-pocket-tts-int8-2026-01-26", // int8 last resort
}

// Audio is mono PCM produced by the engine.
type Audio struct {
	Samples    []float32
	SampleRate int
}

// Duration returns the audio length in seconds.
func (a Audio) Duration() float64 {
	if a.SampleRate == 0 {
		return 0
	}
	return float64(len(a.Samples)) / float64(a.SampleRate)
}

// Engine is a loaded pocket-tts model plus a cache of reference voice wavs.
// It is safe for concurrent use; generation calls are serialized internally.
type Engine struct {
	mu       sync.Mutex
	tts      *sherpa.OfflineTts
	modelDir string
	refsDir  string
	refs     map[string]*refWave // by canonical name, e.g. "jane-bright-synth"
	closed   bool
}

type refWave struct {
	samples []float32
	rate    int
}

// New loads the pocket-tts model from the provisioned cache root. root is the
// directory that contains models/ and refs/ (normally ~/.cache/emote, see
// internal/provision; the repo checkout also satisfies the layout for dev).
// Model tier is autodetected by what is provisioned: fp32 bundle first, then
// the mixed int8+fp32-lm_flow bundle, then plain int8.
func New(root string) (*Engine, error) {
	modelsRoot := filepath.Join(root, "models")
	refsDir := filepath.Join(root, "refs")

	var modelDir string
	for _, d := range modelBundleDirs {
		cand := filepath.Join(modelsRoot, d)
		if bundleComplete(cand) {
			modelDir = cand
			break
		}
	}
	if modelDir == "" {
		return nil, fmt.Errorf("engine: no usable pocket-tts bundle under %s (looked for %s); run provisioning first", modelsRoot, strings.Join(modelBundleDirs, ", "))
	}
	if fi, err := os.Stat(refsDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("engine: refs directory %s missing; run provisioning first", refsDir)
	}

	cfg := sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Pocket: sherpa.OfflineTtsPocketModelConfig{
				LmFlow:          modelPath(modelDir, "lm_flow"),
				LmMain:          modelPath(modelDir, "lm_main"),
				Encoder:         modelPath(modelDir, "encoder"),
				Decoder:         modelPath(modelDir, "decoder"),
				TextConditioner: modelPath(modelDir, "text_conditioner"),
				VocabJson:       filepath.Join(modelDir, "vocab.json"),
				TokenScoresJson: filepath.Join(modelDir, "token_scores.json"),
			},
			NumThreads: numThreads,
			Provider:   "cpu",
			Debug:      0,
		},
	}
	tts := sherpa.NewOfflineTts(&cfg)
	if tts == nil {
		return nil, fmt.Errorf("engine: failed to load pocket-tts model from %s", modelDir)
	}
	return &Engine{
		tts:      tts,
		modelDir: modelDir,
		refsDir:  refsDir,
		refs:     map[string]*refWave{},
	}, nil
}

// ModelDir reports which model bundle was selected (fp32/mixed/int8 tier).
func (e *Engine) ModelDir() string { return e.modelDir }

// Synthesize renders text as speech cloned from the named reference wav.
// refName is resolved against the provisioned refs dir ("jane-bright-synth"
// or "jane-bright-synth.wav" both work). The per-call max_frames guard
// (3*chars+40, capped at the engine max) prevents the short-sentence EOS
// runaway (P1.5 §4). Callers should pass one sentence at a time — the
// engine's own sentence merge garbles joins (use internal/speech).
func (e *Engine) Synthesize(text, refName string) (Audio, error) {
	return e.SynthesizeStream(text, refName, nil)
}

// SynthesizeStream is Synthesize with an optional per-chunk callback, fired
// for every Mimi-decoder chunk as it is generated (2-6 chunks per sentence at
// typical lengths). Return false from the callback to abort generation.
// Note: chunk sample counts do not sum exactly to the final sample count —
// the SilenceScale pause squash runs after the callbacks (P1.5 §1).
func (e *Engine) SynthesizeStream(text, refName string, onChunk func(samples []float32) bool) (Audio, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Audio{}, fmt.Errorf("engine: empty text")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Audio{}, fmt.Errorf("engine: closed")
	}
	ref, err := e.loadRefLocked(refName)
	if err != nil {
		return Audio{}, err
	}

	gen := sherpa.GenerationConfig{
		SilenceScale:        silenceScale,
		Speed:               1.0,
		ReferenceAudio:      ref.samples,
		ReferenceSampleRate: ref.rate,
		NumSteps:            numSteps,
		Extra:               extraJSON(text),
	}
	var cb func([]float32, float32) bool
	if onChunk != nil {
		cb = func(samples []float32, _ float32) bool { return onChunk(samples) }
	}
	audio := e.tts.GenerateWithConfig(text, &gen, cb)
	if audio == nil || len(audio.Samples) == 0 {
		return Audio{}, fmt.Errorf("engine: generation produced no audio for %q", text)
	}
	return Audio{Samples: audio.Samples, SampleRate: audio.SampleRate}, nil
}

// Close releases the model. The Engine must not be used afterwards.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	sherpa.DeleteOfflineTts(e.tts)
	e.tts = nil
	e.refs = nil
}

// loadRefLocked resolves and caches a reference wav by name. The reader is
// tolerant of bogus RIFF/data chunk lengths (server-streamed wavs declare
// wrong sizes; DESIGN.md requires header repair before use).
func (e *Engine) loadRefLocked(refName string) (*refWave, error) {
	name := strings.TrimSuffix(refName, ".wav")
	if name == "" {
		return nil, fmt.Errorf("engine: empty reference name")
	}
	if r, ok := e.refs[name]; ok {
		return r, nil
	}
	path := filepath.Join(e.refsDir, name+".wav")
	samples, rate, err := readWaveFile(path)
	if err != nil {
		return nil, fmt.Errorf("engine: reference %q: %w", refName, err)
	}
	// Bound what we offer to the voice encoder (it ignores >~10 s anyway).
	if max := rate * maxRefLenSec; len(samples) > max {
		samples = samples[:max]
	}
	r := &refWave{samples: samples, rate: rate}
	e.refs[name] = r
	return r, nil
}

// extraJSON builds the per-call Extra config: determinism knobs plus the
// max_frames runaway guard sized to the text.
func extraJSON(text string) []byte {
	frames := len([]rune(text))*3 + 40
	if frames > maxFramesCap {
		frames = maxFramesCap
	}
	m := map[string]any{
		"seed":                    seed,
		"temperature":             temperature,
		"max_reference_audio_len": maxRefLenSec,
		"min_char_in_sentence":    minCharInSentence,
		"max_char_in_sentence":    maxCharInSentence,
		"max_frames":              frames,
	}
	b, _ := json.Marshal(m)
	return b
}

// modelPath returns dir/name.int8.onnx if present, else dir/name.onnx, so a
// bundle dir may mix int8 and fp32 files (the "mixed" tier does exactly that).
func modelPath(dir, name string) string {
	int8 := filepath.Join(dir, name+".int8.onnx")
	if _, err := os.Stat(int8); err == nil {
		return int8
	}
	return filepath.Join(dir, name+".onnx")
}

// bundleComplete reports whether dir contains a loadable pocket-tts bundle
// (each model component in either int8 or fp32 form, plus the vocab files).
func bundleComplete(dir string) bool {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false
	}
	for _, name := range []string{"lm_flow", "lm_main", "encoder", "decoder", "text_conditioner"} {
		if _, err := os.Stat(modelPath(dir, name)); err != nil {
			return false
		}
	}
	for _, f := range []string{"vocab.json", "token_scores.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

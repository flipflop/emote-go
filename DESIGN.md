# emote-go — P2 design contract

Standalone Go port of `emote` (behavioral spec: `../roz-emote-cli/DESIGN.md`, which
remains authoritative for CLI surface, modes, sentiment mapping, hook semantics, config
schema, exit codes). This file defines the Go-specific structure and the P1-derived
engine decisions. North star: self-contained executable, low end-user complexity.

## Package layout

```
emote-go/
├─ cmd/emote/main.go        # CLI wiring (stdlib flag), subcommands per Python DESIGN.md
├─ internal/engine/         # sherpa-onnx pocket-tts wrapper (ship config below)
├─ internal/provision/      # first-run model+ref download to ~/.cache/emote (sha256, resume, progress; NEVER from hooks)
├─ internal/speech/         # utterance merging, per-sentence pipeline, playback dispatch
├─ internal/play/           # oto v3 playback via detached `emote __play` self-exec; pidfile interrupt; afplay fallback
├─ internal/config/         # JSON config, same schema/file as Python (~/.config/emote/config.json — shared!)
├─ internal/extract/        # port of extract.py (modes, preprocessing)
├─ internal/sentiment/      # port of sentiment.py (lexicon, weights, escalation)
├─ internal/hooks/          # port of hooks.py (settings.json merge, transcript JSONL accumulation by message.id)
├─ internal/logo/           # port of logo.py (same art, truecolor gradient)
├─ testdata/                # shared fixtures + goldens generated from the Python reference
└─ build.sh                 # go build + mandatory `codesign -f -s -` (Darwin 25 SIGKILL quirk)
```

## Engine ship config (locked by P1.5–P1.7; see P1-REPORT.md)

- Model: fp32 bundle default; `pocket-mixed-flowfp32` (int8 + fp32 lm_flow) as the
  low-disk fallback tier. steps 5, temp 0.7, 4 threads, seed 42.
- Pipeline: per-sentence generation (engine merge garbles joins), gap 350 ms,
  SilenceScale 0.6, lead-pad 120 ms, trail-pad 20 ms, trim gate 0.010, per-call
  `max_frames = 3*chars+40` guard. Bare text priming stays rejected (P1.7: primer
  words leak into the audio) — instead, sentences opening with a weak onset
  (/h/ /w/ /y/, incl. "one"/"once") use PRIMER-EXCISION: synthesized as
  "Okay: " + sentence, primer audio excised at the colon-pause seam, with an
  un-primed re-render as the safety fallback (internal/speech/onset.go;
  whisper-verified 2026-08-06, 24/24 seams across bright/sad/excited).
- References (server-rendered jane synth wavs, self-hosted, all CC-BY-derived):
  default voice = `jane-bright-synth.wav` (user-approved 2026-08-05); emotion bank:
  sad = `jane-sad-synth.wav`, excited = `jane-excited-synth.wav`; neutral/calm map to
  bright until dedicated refs pass QA. Any new ref must pass the onset QA gate
  (render bare "Hello!" + A/B sentence → transcription-clean + human listen).
- RIFF header repair before ReadWave for any server-streamed wav.

## Behavior parity

`testdata/goldens/` holds recorded Python outputs: for each fixture text × mode,
the exact `[emotion] text` lines from `../roz-emote-cli/emote --quiet`. Go tests
replay fixtures through the ported extract+sentiment and must match byte-for-byte
(document any deliberate divergence in this file). Config file and settings.json hook
entries are FORMAT-COMPATIBLE with the Python emote — a user can switch binaries
without migration; installed hook commands point at whichever binary installed them.

Generated tables are twinned: `internal/extract/emoji_names.go` and the Python
`emote_cli/emoji_names.py` are BOTH emitted by
`../roz-emote-cli/scripts/gen_emoji_table.py` from Unicode's `emoji-test.txt` +
Extended_Pictographic ranges (Emoji 17.0 at last generation) — never hand-edit either;
regenerate both together so emoji→name parity holds by construction.

## Per-session / per-directory control (HOOK events only)

Identical to the Python reference (implemented in `internal/config` +
`internal/hooks.applyDirOverride`, surfaced by `cmd/emote` `here`/`doctor`):

- Mode precedence at hook fire time: **dir override > baked `--mode` flag
  (from `install-hooks --mode`) > `EMOTE_MODE` env > config file >
  `"summarised"`**. `install-hooks` bakes `--mode` into the settings.json
  command ONLY when passed explicitly; the default install writes a plain
  `... hook stop` and mode resolves at fire time.
- Enabled: **any OFF wins** — dir override, `EMOTE_ENABLED` env, and config
  `"enabled"` must ALL permit (`config.HookEnabled` + override AND).
- Scope rule: overrides and enabled gates apply to HOOK events only; explicit
  CLI speak invocations always speak (user intent is explicit).
- Matching: the hook payload's `cwd` field (absent -> no override),
  symlink-resolved, LONGEST path-prefix match on path components (an override
  on `/a/b` applies to `/a/b/c`, never `/a/bc`).
- Config: optional `"dir_overrides": {"<abs path>": {"enabled": bool?,
  "mode": str?}}`, byte-compatible with the Python emote (sorted keys —
  encoding/json map ordering matches Python's explicit sort — enabled-then-
  mode value order, key omitted when empty).

## Backend tiers (runtime autodetect, invisible)

1. emote-chat server on :8123 healthy → use /api/tts (full local bank, warm model).
2. Else embedded engine, if provisioned (`~/.cache/emote/`).
3. Else: friendly provisioning prompt on interactive runs; hooks stay silent no-ops.

## Distribution shape (P3 preview, constrains P2)

Artifact = binary + `lib/` (libsherpa-onnx-c-api.dylib + versioned libonnxruntime) with
`@loader_path/lib` rpath, all re-signed. Engine loading must therefore never assume
module-cache paths. Models/refs are cache downloads, never build inputs; Expresso/EARS
material must never enter this repo or its artifacts.

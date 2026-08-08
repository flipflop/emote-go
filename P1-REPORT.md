# P1 Engine Spike Report — Go + sherpa-onnx pocket-tts

Date: 2026-08-04. Machine: Apple M1 Max, macOS (Darwin 25.5.0), Go 1.21.1,
sherpa-onnx-go v1.13.4, pocket-tts int8 (2026-01-26 bundle), 4 threads, num_steps=5,
seed=42, jane = zero-shot clone of `kyutai/tts-voices vctk/p339_023_enhanced.wav`
(32 kHz source, resampled to 24 kHz by sherpa internally).

> Historical note: `findings/NN` and `RECOMMENDATION.md` cited below are the
> P0 research documents, published in the emote-chat repo at
> [github.com/flipflop/emote-chat](https://github.com/flipflop/emote-chat)
> under `research/`. The `ab*/` listening-candidate directories were spike
> artifacts and are not tracked in this repo.

## Verdict: **GO for P2.** Engine works from Go exactly as findings/01 predicted; timings
match the Python-API benchmarks within noise; the dylib relocation fix is proven concrete
and small. One macOS-version surprise (codesign, below) — solved, must be baked into P3.

## Timings (Go spike vs findings/01 Python-API numbers)

Each spike run is a fresh process, so the voice embedding is recomputed every run
(findings' "embed cached" rows were same-process second calls — the persistent Go daemon
/ hook process will get that benefit; the LRU cache is per-process).

| case | Go spike | findings/01 (Python API) |
|---|---|---|
| model load (cold) | **0.48–0.53 s** | 0.64 s |
| short sentence — first audio / total gen (1.6 s audio) | **0.35 s / 0.37 s** | 0.25–0.32 s / 0.28–0.34 s |
| A/B sentence, ~5.4 s audio — first audio / total gen | **0.83 s / 1.07 s** | (~7 s sentence) 0.78 s / 0.99 s |
| RTF | **0.19–0.23** | 0.17–0.23 |

First audio 0.35–0.83 s — ~4x inside the 2 s hook budget, matching Python. The streaming
callback (`GenerateWithConfig(text, cfg, func(samples []float32, progress float32) bool)`)
fires per Mimi-decoder chunk as documented; 2–6 chunks per sentence at these lengths.

## A/B render (do not autoplay — for human listening)

Same sentence both: "Hello! This is jane speaking. The quick brown fox jumps over the
lazy dog."

- Go (zero-shot clone from VCTK wav): `ab/go-jane.wav` (spike artifact, untracked)
- Python server (state-based jane, `POST :8123/api/tts`, emotion=neutral): `ab/py-jane.wav` (spike artifact, untracked)

Both 24 kHz / 16-bit / mono (verified with afinfo); py 5.3 s, go 5.4 s. Server was
reachable and returned HTTP 200. **Listening comparison is the open human task** (risk
#2 in RECOMMENDATION.md).

## Portability audit + the concrete packaging fix

`go build ./spike` produces a **2 MB** dynamically-linked binary:

- `otool -L`: `@rpath/libsherpa-onnx-c-api.dylib` (4.6 MB) + `@rpath/libonnxruntime.1.27.0.dylib` (27 MB), plus system libs only.
- Exactly one `LC_RPATH`: `~/go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-macos@v1.13.4/lib/aarch64-apple-darwin` — the **build machine's module cache**, confirming findings/03 risk #1. Copied anywhere without the module cache, dyld would fail.
- `libsherpa-onnx-c-api.dylib` itself depends on `@rpath/libonnxruntime.1.27.0.dylib`, and both dylibs have `@rpath/...` install names — so a single rpath rewrite on the main binary fixes the whole chain. Note the linked name is the **versioned** `libonnxruntime.1.27.0.dylib` (ship that file, not the `libonnxruntime.dylib` alias).

**Relocation test (passed):** copied binary + the 2 dylibs into an isolated scratchpad
`dist/` (`dist/emote-spike`, `dist/lib/*.dylib`), then:

```
install_name_tool -delete_rpath <module-cache-path> -add_rpath @loader_path/lib dist/emote-spike
codesign -f -s - dist/lib/*.dylib dist/emote-spike   # install_name_tool invalidates the signature
```

Ran it from `/` (different cwd) with `DYLD_PRINT_LIBRARIES=1` (module cache untouched):
dyld log shows both sherpa dylibs loading **from `dist/lib/`**, not the module cache, and
generation completed end-to-end (RTF 0.19). A distributable macOS artifact must contain:

1. `emote` binary with rpath rewritten to `@loader_path/lib`
2. `lib/libsherpa-onnx-c-api.dylib` + `lib/libonnxruntime.1.27.0.dylib` (~31 MB, per-arch)
3. re-sign **binary and both dylibs** after `install_name_tool` (ad-hoc minimum; quill sign+notarize for direct downloads per findings/03 §5)
4. models are NOT in the artifact — first-run download (98 MB int8 + jane wav, sha256s in `models/MANIFEST.txt`)

## Surprises vs findings/01

1. **macOS kills Go 1.21 linker-signed binaries** (new, not in findings): the freshly
   built binary died instantly with SIGKILL, crash report `CODESIGNING / Invalid Page`
   in the main executable — Go 1.21.1's ad-hoc *linker-signed* signature is rejected by
   this macOS version. `codesign -f -s -` on the binary fixes it completely. Since the
   packaging step re-signs everything anyway (see above), this folds into the same fix,
   but **every local dev build needs the re-sign too** — P2 should add a `go build &&
   codesign -f -s -` wrapper (Makefile/taskfile), or bump to a newer Go toolchain, which
   likely signs acceptably.
2. **No toolchain friction otherwise**: sherpa-onnx-go v1.13.4 built first try on Go
   1.21.1; no GOTOOLCHAIN needed.
3. API exactly as findings/01 documented (`OfflineTtsPocketModelConfig`,
   `GenerationConfig{ReferenceAudio, ReferenceSampleRate, NumSteps, Extra}`,
   per-chunk callback, `NumSpeakers==1`/sid ignored). `ReadWave` + `audio.Save` cover
   WAV I/O with no extra deps. sherpa logs one C++ info line (resampler creation) to
   stdout/stderr on first generation — cosmetic; silence via Debug=0 is default, may
   need stderr filtering for `--quiet` parity in P2.
4. Sizes: `go mod tidy` pulls all three per-OS native modules (~84 MB of module zips;
   macos module 123 MB unpacked). Total spike downloads ≈ 183 MB — inside the 250 MB
   budget.

## Go/no-go

**GO.** Proceed to P2 (port), with two carried actions: (a) human A/B listen of
`ab/go-jane.wav` vs `ab/py-jane.wav` before locking the zero-shot-jane decision;
(b) fold the codesign re-sign into the standard build path from day one.

## P1.5 tuning (2026-08-05)

Human A/B of the round-1 renders flagged the Go output as (1) fragmented between
sentences, (2) monotone, (3) slightly depressed in tone. All three were chased to
root causes; six labeled candidates + objective metrics are in `ab2/` (see
`ab2/NOTES.md` for the full matrix and per-candidate scoring).

**What moved the needle**

1. **Fragmentation was our config bug, not the engine.** The spike left
   `GenerationConfig.SilenceScale` unset (0); sherpa's C API maps 0 to a default
   of **0.2** — every pause > 0.2 s was compressed to 20% of its length after
   generation (measured: 1.13 s of pauses stripped from the A/B sentence;
   sentence gaps of ~140 ms instead of ~450 ms). Setting `SilenceScale`
   explicitly fixes it; **0.6** empirically matches the Python render's pause
   profile (int8/fp32 pocket over-generates sentence pauses at 1.0). P2 must
   always set this field — 0 is a landmine, and it also explains why callback
   sample count ≠ saved sample count (the squash runs after the callbacks).
2. **Monotony was int8 quantization of `lm_flow`.** Pitch variance (objective
   proxy: F0 std-dev over voiced frames) is 2.5-2.6 semitones on the int8
   bundle vs 4.84 on the Python reference; the fp32 bundle restores 4.7-4.8.
   Swapping **only `lm_flow.onnx` to fp32** (10 MB → 39 MB, dir
   `models/pocket-mixed-flowfp32/`) restores 4.50 st at int8 speed. Decode
   steps 10 (vs 5) did not help any metric; temperature 0.3 (pocket-tts's
   Python default) made speech slower, pausier and flatter — keep sherpa's 0.7.
3. **Tone/timbre:** the Mimi voice encoder emits a fixed-size embedding and
   ignores reference audio beyond ~10 s in-graph (verified bit-identical
   output), so richer/longer references are moot; no additional CC-BY p339
   audio exists on HF short of the 11 GB VCTK zip. Two levers remain: the RAW
   (un-enhanced) `p339_023.wav` — the very file the Python jane state was
   exported from — as reference (candidate 06), and seed choice (median F0
   varies ~±2 Hz/seed). Trimming+normalizing the enhanced ref made things
   worse (tested, rejected).
4. **Engine sentence handling gotchas for P2** (from source,
   `offline-tts-pocket-impl.h` v1.13.4): text is split on `.!?` and re-merged
   below `min_char_in_sentence` (30) — the merge drops the space after
   punctuation (`Hello!This` → tokens `! T hi s`), an upstream bug; and
   `min_char_in_sentence=1` is a trap (a bare "Hello!" never hits EOS and
   generates to max_frames — 44 s of audio). Leave the split config at
   defaults; the whole A/B sentence already runs as one LM pass.

**Timings impact** (A/B sentence, M1 Max, 4 threads, cold process)

| stack | model load | first audio | total gen | RTF |
|---|---|---|---|---|
| int8 (P1 baseline) | 0.48-0.55 s | 0.86 s | 1.10 s | 0.17 |
| **int8 + fp32 lm_flow (cand. 05)** | **0.49 s** | **0.86 s** | **1.05 s** | **0.21** |
| fp32 full (cand. 02/03) | 0.64 s | 1.10 s | 1.32 s | 0.23-0.28 |
| fp32, steps=10 (cand. 04) | 0.64 s | 1.36 s | 1.58 s | 0.28 |

All still ~2x+ inside the 2 s first-audio budget. fp32 full costs +0.25 s first
audio and +254 MB unpacked; the mixed dir costs +29 MB and ~nothing in latency.

**Recommendation:** pending the human listen (rank `ab2/03` vs `05` vs `06`
against `ab2/py-jane.wav`), ship P2 with **int8 + fp32 `lm_flow` + explicit
`SilenceScale: 0.6`** (candidate 05) — py-equivalent pause profile, 93% of the
Python render's pitch variance, int8-class latency and download size (first-run
fetch: 98 MB int8 tarball + 39 MB fp32 lm_flow + jane wav). If timbre still
reads "depressed", switch the reference to the raw `p339_023.wav` (cand. 06
mixes into this cleanly) before considering full fp32.

## P1.6 — garble fix, reference mining, synthetic-jane emotion probe (2026-08-05)

Human verdict on ab2: candidate 06 (fp32, sil 0.6, raw ref) "pretty decent,
potentially shippable", but the opening "Hello!" garbled ("Thoughtha") and the
tone still a bit flat. Five new candidates + full metrics in `ab3/`
(`ab3/NOTES.md`); all use candidate 06's base config (fp32, steps 5, temp 0.7,
sil 0.6, seed 42) plus the new garble fix.

1. **Garble is FIXED — ship `--per-sentence`.** The garble was the upstream
   `NeedSpaceBetween` merge bug (P1.5 §4): token dump shows BOTH joins were
   destroyed (`▁Hello ! T hi s … . T he ▁quick …`). `spike/main.go` now has
   `--per-sentence` (+`--gap`, default 350 ms): sentences split in Go, one
   engine call each, edge-silence trim + 8 ms fades, stitched with our own
   silence. Tokens verified clean per segment. Three surprises, all good:
   the P1.5 "bare Hello! runaway" trap does NOT fire when the short sentence
   is its own engine call (0.88 s, clean; a per-call `max_frames = 3×chars+40`
   guard is set anyway); **first-audio drops to 0.18-0.38 s** (first pass =
   first sentence only, vs 0.83-1.10 s single-pass); and prosody gets livelier
   (same config/ref as ab2/06: F0 sd 3.57 → 4.71 st) because every sentence
   gets a fresh energetic onset. The minimal text workaround (`--normalize`,
   `.! ?` → `; ` separator) is token-correct but measurably worse (sd 3.23,
   815 ms first pause) — per-sentence is the robust fix. P2 must adopt the
   per-sentence pipeline (it is also the natural streaming unit for the hook).
2. **Reference-window mining: dead end.** The raw p339 wav is 11.86 s, so a
   10 s window can slide ≤1.9 s; best window sd 2.60 vs default 2.59 st.
   Best 6 s sub-window (2.92 st) = candidate 02: highest output sd (4.92) but
   pitch drifts high and energy lowest. The source audio is simply flat.
3. **Synthetic jane references WORK — and emotion transfer is real.** The
   Python server rendered jane saying lively / overtly-sad / overtly-excited
   text (`refs/jane-{lively,sad,excited}-synth.wav`, 24 kHz; note: server
   streams a bogus RIFF length — rewrite headers before `ReadWave`). Used as
   zero-shot references in Go on the SAME neutral sentence:
   - lively ref (cand. 03): pause profile is py-jane's closest twin, sd 4.46 —
     a fully self-hosted, VCTK-free ship path if the listen test agrees.
   - **emotion probe (cands. 04/05): sad ref → 187.5 Hz / sd 3.06 / RMS −29%;
     excited ref → 224.3 Hz / sd 5.26 / RMS +29%** vs the neutral-ref
     baseline. Pitch level, pitch variance and energy all shift decisively
     toward the reference's affect on identical text; tempo alone does not
     follow (use `Speed`/text for that). **A commercially-clean jane emotion
     bank — one server-rendered reference wav per emotion, selected at
     runtime in Go — is objectively viable.** Human listen owed: timbre-drift
     check on 04/05.

Timings (per-sentence, fp32): first audio 0.18-0.38 s, RTF 0.22-0.25; the
voice-embedding LRU cache is hash-keyed and per-process, so only the first
segment pays embedding cost. Recommended P2 shape: per-sentence pipeline +
sil 0.6 + fp32 lm_flow (or full fp32) + reference selected per emotion from
the synth bank, pending the ab3 listen (01 = safe default, 03 = license-clean
bet, 04/05 = emotion-bank proof).

## P1.7 — onset fix, warm/bright references, ship config (2026-08-05)

Human verdict on ab3: 04/05 (sad/excited refs) liveliest, 05 best overall and
affect transfer confirmed by ear — but "Hello" garbled to "No lo"/"The no" in
every candidate except 05. Four new candidates + full diagnosis in `ab4/`
(`ab4/NOTES.md`).

1. **Onset root cause: NOT the stitcher trim (hypothesis refuted) — the
   model itself skips utterance-initial weak onsets with some reference
   embeddings.** Instrumentation (`--dump-seg-dir`, per-segment lead/trail
   cut logging) shows 0 ms of leading trim on the garbled "Hello!" segments:
   their raw engine output starts mid-vowel at sample 0 — no /h/, no leading
   silence, at every seed tried. sherpa's pocket impl gives the LM no warm-up
   runway (audio latents start at step 0; the Python stack's renders open
   with ~170 ms of silence). Whisper-confirmed: bare "Hello!" with the raw
   ref transcribes "No, no, no.". Two-part fix, both shipped in
   `spike/main.go`: **(a) conservative leading trim** (`--lead-pad 120` ms
   pre-roll before first above-gate sample; ab3's 20 ms pad was also cutting
   the sad ref's sub-gate murmured /h/), and **(b) reference onset QA** — a
   ref enters the emotion bank only if bare "Hello!" + the A/B sentence
   render a whisper-clean "Hello". Text priming (comma prefixes) was tested
   and REJECTED: it restores /h/ on some refs but makes the model repeat
   short segments ("Hello! Hello!"); NBSP-joins and silence-padded refs also
   fail (see ab4/NOTES.md). `jane-lively-synth` (ab3/03's ref) and direct
   raw-p339 refs fail QA at every seed/prime — retired from the ship path.
2. **Warm + bright default candidates work.** Two new server-rendered refs
   (RIFF headers repaired before `ReadWave`): `jane-warm-synth` (emotion
   happy, friendly script) and `jane-bright-synth` v2 (emotion excited,
   calmer script; v1 failed onset QA — the QA gate earns its keep). Outputs
   on the A/B sentence: warm 250 Hz / sd 3.45 / RMS 0.099 (energetic, pitch
   sits high — judge by ear); **bright 198.3 Hz / sd 4.79 / RMS 0.085 — the
   closest objective twin to py-jane (197.5 / 4.84 / 0.107) the Go stack has
   produced**. "Hello" is whisper-clean in all four ab4 candidates; the
   emotion probe (sad vs excited: 187.5 vs 224.3 Hz, sd 3.06 vs 5.26, RMS
   0.060 vs 0.108) is intact through the fix.
3. **Recommended ship config (P2/P3):**
   - Model: **fp32 bundle** (every ear-approved render used it), steps 5,
     temp 0.7, 4 threads; `models/pocket-mixed-flowfp32` (int8 + fp32
     lm_flow) is the metrics-equivalent fallback if the 254 MB delta matters
     — give it one confirming listen first.
   - Pipeline flags: `--per-sentence --gap 350 --silence-scale 0.6
     --seed 42 --lead-pad 120` (trail pad 20 ms, trim gate 0.010, prime
     OFF); per-call `max_frames = 3*chars+40` guard stays.
   - Reference set (all server-rendered jane synth, self-hosted, VCTK-free):
     **default = warm or bright per the ab4 listen**; emotion bank = sad /
     excited (+ the loser of warm/bright as a mid state if wanted). Every
     future ref must pass the onset QA gate before shipping.
   - Build: `go build && codesign -f -s -` (unchanged; ab4 binary re-signed).

Timings (per-sentence, fp32, cold): first audio 0.20-0.35 s, RTF 0.24-0.29.
Open human task: listen-rank ab4/01 (warm) vs 02 (bright) vs 03 (excited)
for the default voice; confirm every "Hello!" carries its /h/; sanity-check
04 still reads sad.

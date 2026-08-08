# emote-go

**emote** — a self-contained CLI that gives Claude Code an emotional voice.
One binary, zero dependencies, fully local TTS.

Standalone Go port of the Python [`emote-cli`](https://github.com/flipflop/emote-cli)
reference. Same CLI surface, config file (`~/.config/emote/config.json`,
byte-compatible — switch binaries freely) and hook semantics; adds an
**embedded on-device TTS engine** (sherpa-onnx pocket-tts) so nothing else
needs to be installed or running. See `DESIGN.md` and `PARITY.md` for the
contract, and `P1-REPORT.md` for the engine spike that shaped it.

## Goals & motivation

The Python reference needs a running TTS server; the web app needs a
browser. The point of emote-go is a binary that **just talks**: download one
artifact, run one provisioning command, and Claude Code speaks — no Python,
no server, no package manager. North star: **one artifact, zero
prerequisites**. Speech is generated locally on your machine; no text ever
leaves it.

## Capabilities

- **Embedded local TTS** — sherpa-onnx pocket-tts (fp32 bundle), rendering
  at ~0.2–0.35 s to first audio on Apple Silicon, entirely offline after a
  one-time `emote provision` download (~160 MB, sha256-verified, resumable).
- **Emotional voice bank** — the `jane` voice with per-emotion zero-shot
  references (bright / sad / excited synth renders); sentiment is classified
  from the text (or pinned with `--emotion`).
- **Claude Code hooks** — `emote install-hooks` wires Stop/Notification
  hooks so Claude's responses are read aloud with appropriate tone; hooks
  are silent no-ops when nothing is available or you turn them off.
- **Per-session and per-directory control** — env vars for the session,
  `emote here` for the project (details below).
- **Text that reads well aloud** — extraction modes (`verbose`,
  `summarised`, `important`, `warnings`, `code`), markdown/URL/path
  cleanup, minimal number normalization, and emoji spoken names generated
  from Unicode's emoji tables ("🎉" → "party popper").
- **Three-tier backend, autodetected** — a healthy
  [emote-chat](https://github.com/flipflop/emote-chat) server on `:8123`
  when present (full voice catalog, warm model), else the embedded engine,
  else a friendly provisioning prompt (hooks stay silent).

## Install (macOS arm64)

Download the compiled binary bundle —
[emote-go-v0.1.0-darwin-arm64.tar.gz](https://github.com/flipflop/emote-go/releases/download/v0.1.0/emote-go-v0.1.0-darwin-arm64.tar.gz)
(12 MB: the `emote-go` binary plus the two dylibs it needs) — or browse the
[releases page](https://github.com/flipflop/emote-go/releases):

```sh
curl -LO https://github.com/flipflop/emote-go/releases/download/v0.1.0/emote-go-v0.1.0-darwin-arm64.tar.gz
tar xzf emote-go-v0.1.0-darwin-arm64.tar.gz
cd emote-go-v0.1.0-darwin-arm64
xattr -d com.apple.quarantine emote-go lib/*.dylib   # ad-hoc signed; see Limitations
./emote-go provision      # one-time model + voice download (~160 MB)
./emote-go doctor         # backends, hooks, config, spoken test line
./emote-go install-hooks  # give Claude Code a voice (user scope)
./emote-go "Hello! It is nice to finally talk."
```

## Build from source

```sh
./build.sh              # go build + mandatory codesign -> ./emote-go
./build.sh test         # go vet + full test suite
```

The `codesign -f -s -` step is not optional on recent macOS — see
`build.sh` and `P1-REPORT.md`.

## Per-session and per-project control

Hooks decide *at fire time* whether and how to speak, so you can steer them
per terminal session (env vars) or per directory (`emote here`) without
touching the installed hook commands.

Per **session** — launch Claude Code with an env override:

```sh
EMOTE_ENABLED=0 claude          # this session stays silent
EMOTE_MODE=important claude     # this session speaks only errors/warnings/successes
```

Per **project** — register a directory override (keyed by the
symlink-resolved current directory; applies to that directory and everything
under it):

```sh
emote-go here                 # what applies here, and which registered path matched
emote-go here off             # silence hooks in this project
emote-go here on              # re-enable them
emote-go here mode verbose    # this project gets the full reading
emote-go here clear           # remove the override registered at this directory
```

From *inside* a Claude Code session, use the `!` bash prefix:

```
!emote-go here off
!emote-go here mode important
```

Precedence for the mode a hook uses:
**dir override > baked `--mode` (from `install-hooks --mode`) > `EMOTE_MODE`
env > config file > `summarised`**. `install-hooks` bakes `--mode` into the
hook command only when you pass it explicitly. For enabled, **any OFF wins**:
the dir override, `EMOTE_ENABLED`, and `emote-go config enabled` must all
permit.

Scope: overrides and the enabled gates apply to Claude Code **hook events
only** — an explicit `emote-go ...` invocation always speaks, because you
asked it to. `emote-go doctor` lists all registered overrides and flags the
one that applies to the current directory. Matching is per path component: an
override on `/a/b` covers `/a/b/c` but never `/a/bc`.

## Limitations (today)

- **macOS arm64 only.** The release bundle ships Apple-Silicon dylibs; other
  platforms build from source at their own risk (untested).
- **Ad-hoc signed, not notarized.** Gatekeeper will block the downloaded
  binary: either right-click → Open once, or
  `xattr -d com.apple.quarantine emote-go lib/*.dylib`.
- **English only** — the pocket-tts model and the text pipeline are English.
- One shipped voice (`jane`) with a three-emotion reference bank.

## Roadmap

- **P3** — Homebrew tap, Developer-ID signing + notarization (no quarantine
  dance), CI release builds.
- **Cross-platform** — the engine (sherpa-onnx) and playback (oto) already
  have Linux/Windows ports; packaging and testing are the work.
- **P4** — voices UX: catalog listing, per-voice emotion banks, custom
  reference onboarding (with the onset QA gate).

## Family

| Project | What it is |
| --- | --- |
| [emote-chat](https://github.com/flipflop/emote-chat) | Web app + TTS/emotion server (the full voice catalog; backend tier 1) |
| [emote-cli](https://github.com/flipflop/emote-cli) | Python reference CLI (authoritative behavioral spec; server-backed) |
| **emote-go** (this repo) | Self-contained Go binary with the embedded engine |

The engine research (pocket-tts benchmarks, portability, licensing) lives in
the emote-chat repo under `research/`.

## Credits

- **[pocket-tts](https://huggingface.co/kyutai/pocket-tts)** (Kyutai) — the
  TTS model; weights CC BY 4.0.
- **[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx)** (k2-fsa) — the
  inference engine; Apache-2.0. The pocket-tts ONNX conversion follows
  KevinAHM's export work.
- **CSTR VCTK Corpus** — speaker p339, the source of the `jane` voice;
  CC BY 4.0 (via [kyutai/tts-voices](https://huggingface.co/kyutai/tts-voices)).
- **[oto](https://github.com/ebitengine/oto)** (Ebitengine) — audio
  playback; Apache-2.0.
- **Unicode CLDR / emoji data** — emoji spoken names are generated from
  Unicode's `emoji-test.txt` (Unicode License v3).
- **[emote-cli](https://github.com/flipflop/emote-cli)** — the Python
  reference this port is held byte-for-byte to (see `PARITY.md`).

See `NOTICE.md` for the full third-party notices.

## Usage considerations

- **Voice licensing.** The shipped voice references are **synthetic** jane
  renders (pocket-tts output conditioned on VCTK p339, CC BY 4.0) — no raw
  human recordings are redistributed, and everything shipped is
  CC-BY-derived with attribution in `NOTICE.md`. Non-commercial datasets
  (Expresso, EARS) are **never** part of this repo or its release artifacts.
- **Voice cloning.** pocket-tts supports zero-shot cloning; per its authors'
  terms, do not clone a voice without the speaker's consent.
- The model bundle and references are downloaded at `provision` time from
  this repo's GitHub release (override with `EMOTE_ASSET_BASE`), verified
  by sha256, and cached in `~/.cache/emote/` (`EMOTE_CACHE_DIR` overrides).

## Tests

```sh
./build.sh test
```

Includes byte-for-byte parity goldens recorded from the Python reference
(extract × mode, sentiment) and cross-binary config/settings compatibility
tests — see `PARITY.md`.

## License

MIT — see `LICENSE`. Third-party models, data and libraries retain their own
licenses — see `NOTICE.md`.

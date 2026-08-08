# PARITY.md — Go port vs Python reference (extract / sentiment / config / hooks)

Status (P2 text/logic ports, verified 2026-08-06):

- **Goldens: 90/90 extract×mode goldens matched byte-for-byte, 18/18 sentiment
  goldens matched** (re-verified 2026-08-08 after the emoji-name preprocessing
  landed and all goldens were regenerated from the updated Python reference).
  Goldens are recorded from the Python reference first
  (`../roz-emote-cli/emote --quiet --mode <mode> < fixture` with an empty
  `EMOTE_CONFIG_DIR`, plus a script dumping `sentiment.classify(text)`), then
  the Go port is held to them. 18 fixtures live in
  `testdata/fixtures/` (12 lifted from the Python test suites, 4 realistic
  Claude-response fixtures: `refactor_walkthrough`, `test_failure_report`,
  `deploy_success`, `mixed_notes`, plus 2 emoji-bearing fixtures:
  `emoji_celebration`, `emoji_mixed`); goldens in `testdata/goldens/`.
- **Emoji -> spoken-name preprocessing is table-generated on both sides.**
  Both ports consume the same generated data
  (`emote_cli/emoji_names.py` / `internal/extract/emoji_names.go`, emitted
  together by `../roz-emote-cli/scripts/gen_emoji_table.py` from Unicode
  emoji-test.txt + emoji-data.txt, Unicode/Emoji 17.0), and the replacement
  algorithm is a line-for-line port (longest match counted in code points —
  Go walks a `[]rune` slice so lengths align with Python string slicing;
  same repeat-collapse, spacing/punctuation-attach and unknown-pictograph
  drop rules). No divergence: parity holds on the emoji goldens and the
  emoji unit-test table is duplicated in both suites.
  Parity tests: `internal/extract/parity_test.go` (replicates the Python
  `--quiet` pipeline: strip stdin → extract → per-utterance auto sentiment →
  `tts.merge_runs` join → `[emotion] text` lines) and
  `internal/sentiment/parity_test.go`.
- An additional 27-case differential sweep (italic/URL/path/HR/sentence edge
  strings, all 5 modes + sentiment, Go vs live Python) produced identical
  output.
- Cross-binary file compatibility verified: Go `config.Save` re-writes a
  Python-written `~/.config/emote/config.json` byte-identically (same key
  order, indent=2, trailing newline, `"voice": null`); Go `hooks.Install` over
  a Python-emote-installed `settings.json` replaces the old marker entries
  without duplicating them and preserves unrelated keys, foreign hooks, and
  original key order (ordered-map JSON round-trip in
  `internal/hooks/omap.go`).
- **Per-directory voice controls (2026-08-08) are twinned.** Optional config
  key `dir_overrides` round-trips byte-identically (both sides assert the
  same on-disk fixture: sorted absolute-path keys, enabled-then-mode value
  order, key omitted when empty — Go's `encoding/json` sorted map keys match
  Python's explicit `sorted()`; live cross-check: Python `emote here off` +
  `here mode verbose` then Go `config voice jane` re-save left the file
  byte-identical). The `here` subcommand prints identical output in both
  binaries; hook-time precedence (dir override > baked `--mode` >
  `EMOTE_MODE` > config > "summarised"; enabled = any OFF wins) and the
  component-boundary prefix matcher (`/a/b` covers `/a/b/c`, never `/a/bc`)
  are line-for-line ports with mirrored precedence-table tests.

## RE2 restructurings (Python regex features Go's regexp lacks)

All in `internal/extract/extract.go`; verified equivalent by the goldens and
the differential sweep.

| Python regex | Feature | Go restructuring |
| --- | --- | --- |
| `_URL_RE` lazy path + `(?=[)\].,;:!?"']*(?:\s\|$))` | lookahead | Match the URL greedily to the next whitespace, then give back the maximal trailing run of `)].,;:!?"'` (`urlRepl`). Equivalent because the lazy match stops exactly where the remainder is all trailing punctuation. |
| `_ITALIC_RE` / `_ITALIC_U_RE` `(?<!\w)\*(...)\*(?!\w)` | lookbehind + lookahead | Explicit left-to-right scanner `replaceItalic` with the same semantics (delimiter not preceded by a word rune; body starts with non-space/non-delimiter, cannot contain the delimiter; closer not followed by a word rune). |
| `_PATH_RE` `(?<![\w./\-])` | lookbehind | Match one boundary char (or `^`) explicitly and re-emit it in the replacement. |
| `_HR_RE` `^\s*([-*_])\s*(?:\1\s*){2,}$` | backreference | `isHR`: strip all whitespace, require >= 3 copies of a single char from `-*_`. |
| `_SENT_SPLIT_RE` `(?<=[.!?])\s+` | lookbehind | Manual scanner `sentences`: split at whitespace runs whose preceding rune is `.` `!` or `?`. |

Related: Go's `\w`/`\s` are ASCII-only while Python's are Unicode-aware, so
the path and URL regexes spell out `\w` as `[\p{L}\p{N}_]` (e.g.
`café/menu.txt` must reduce to `menu.txt`, matching Python). `\s`, `\b` and
the whitespace-collapse regex remain ASCII; all cleaned text at those points
is ASCII-whitespace-separated, and no divergence appeared in any golden or
sweep case.

No restructuring was needed in `internal/sentiment` (the lexicon uses only
`\b`, alternation and non-capturing groups, all RE2-safe).

## Python quirks replicated on purpose (NOT divergences)

- **Hook extract bridge ignores `max_sentences` config.** Python's
  `hooks._extract` calls `extract.extract(text, mode)`, so hooks always use
  the module default budget of 3 even if the config says otherwise. The Go
  `extractBridge` does the same (`extract.Extract(text, mode, 3)`).
- **Empty extraction in hooks falls back to the full text.** Python's bridge
  returns `[{"text": text, "kind": "prose"}]` when extract returns an empty
  list (e.g. `warnings` mode with nothing to warn about), so the stop hook
  speaks the whole reply rather than staying silent. Replicated.
- **Hook log path is hard-wired** to `~/.config/emote/emote.log` and does not
  honor `EMOTE_CONFIG_DIR` (Python `hooks._log_path` behaves the same;
  `config.LogPath()` does honor the override).
- **Ring truncation** keeps the newest `LOG_MAX_BYTES/2` bytes aligned to the
  next line start, exactly as in Python.
- **Hook `cwd` symlink resolution edge.** Python uses `os.path.realpath`
  (resolves non-existent paths lexically); Go uses `filepath.EvalSymlinks`
  and falls back to the raw path when it errors (path missing). Divergence is
  only reachable when a hook payload's `cwd` no longer exists AND contains a
  symlinked component — negligible, and both sides then still degrade to
  plain string matching.

## Deliberate divergences

1. **Unknown extraction mode: panic instead of ValueError.** The task-locked
   signature is `extract.Extract(text, mode string, maxSentences int)
   []Utterance` (no error return), so the Python `raise ValueError` maps to a
   Go panic. The hooks bridge `recover`s it into the same raw-text fallback
   Python's `except Exception` produces; `cmd/` should validate mode against
   `extract.Modes` before calling (the Python CLI's argparse `choices` did the
   equivalent).
2. **Errors instead of exceptions in config.** `config.SetValue` returns an
   error for unknown keys / bad coercions where Python raised
   `KeyError`/`ValueError`; same conditions, idiomatic Go surface.
3. **Wrong-typed values in config.json fall back to that key's default.**
   Python's dict-based config keeps e.g. `"max_sentences": "x"` as the string
   `"x"` in memory; the typed Go struct ignores the malformed value and keeps
   the default (per-key, so one bad value cannot discard the rest of the
   file). Only reachable with a hand-corrupted file; well-formed files written
   by either binary round-trip identically.
4. **Installed hook command path.** Python points at its `emote` launcher
   script; Go points at `os.Executable()` — intended per DESIGN.md
   ("installed hook commands point at whichever binary installed them"). The
   `emote hook` marker logic is unchanged either way.
5. **Speech-side number normalization is a Go-only addition.**
   `internal/speech/norm.go` (`NormalizeForSpeech`, applied inside
   `RenderRef` before sentence splitting) converts number tokens to spoken
   words because the embedded sherpa-onnx pocket-tts tokenizer mispronounces
   raw digits (whisper-verified 2026-08-06: clipped "1", stuttered "300",
   dropped "1000"). This runs only on text sent to the speech engine; the
   `--quiet`/extract/sentiment output — the surface parity is measured on —
   is untouched, and the Python server tier does its own text handling, so
   this is not a parity divergence on any compared output.
6. **Non-ASCII in written JSON stays literal.** Python's `json.dump` escapes
   non-ASCII (`é` → `é`, `ensure_ascii=True`); Go writes UTF-8 directly.
   HTML escaping is disabled on the Go side so `& < >` stay literal like
   Python's output; for pure-ASCII content (the normal case for config and
   settings) the two binaries produce byte-identical files, and either form
   is valid JSON to both readers.

## Test inventory

`./build.sh test` — 164 test functions repo-wide (most table-driven, wrapping
several hundred cases), all green (no codesign step was needed on this
machine; the `codesign -f -s -` fallback from DESIGN.md remains available if
test binaries ever SIGKILL). The Python reference suite is 148 tests
(`python3 -m unittest discover tests`).

- `internal/extract`: golden parity + full port of `test_extract.py`
  (preprocessing incl. the emoji-name table, code summaries, tables, kinds,
  all five modes).
- `internal/sentiment`: golden parity + full port of `test_sentiment.py`
  (classification table, contract, weighting/consumption/escalation).
- `internal/config`: port of `test_config.py` (defaults, file merge, corrupt
  file, env overrides incl. `EMOTE_ENABLED` presence semantics, save/load
  roundtrip, coercions, voice null, env-not-persisted, path helpers) plus a
  byte-format fixture matching Python's file output; dir_overrides
  round-trip/byte fixture, malformed-entry dropping, component-prefix
  matching (incl. `/a/bc` non-match and root override), clear-exact-key,
  `HookEnabled` all-gates table.
- `internal/hooks`: port of `test_hooks.py` (temp HOME + cwd isolation,
  install schema/idempotence/mode flag/project scope/invalid-JSON refusal,
  partial uninstall + round-trip, status, multi-line JSONL transcript
  accumulation by `message.id`, tool_use-only turn fallback, loop guard,
  enabled gate, garbage lines, speak-failure exit 0, notification truncation,
  quiet mode, log ring truncation); dir-override runtime tests (override mode
  beats config, subdirectory match, sibling-prefix non-match, override-off
  silences stop AND notification, override-on cannot revive a disabled
  config, missing/unmatched cwd -> no override).
- `cmd/emote`: `here` subcommand integration (temp config dir/HOME/cwd,
  register/merge/show/parent-match/clear-exact/usage errors), un-baked
  install-hooks default vs explicit `--mode`, and full precedence through the
  real `emote hook stop` entrypoint with synthetic stdin payloads (mode
  chain + enabled any-OFF-wins). Mirrored by the Python `tests/test_here.py`.

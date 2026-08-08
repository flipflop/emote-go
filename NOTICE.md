# NOTICE

emote-go is released under the MIT License (see [LICENSE](LICENSE)).
It builds on the following third-party software and data, whose licenses
and attribution requirements apply to redistributions of this project and
its release artifacts.

## Software

### sherpa-onnx (k2-fsa)

Speech inference engine, linked via the `sherpa-onnx-go` bindings; the
release bundle redistributes `libsherpa-onnx-c-api.dylib` and the ONNX
Runtime it depends on.
License: Apache License 2.0. Copyright the k2-fsa/sherpa-onnx authors.
<https://github.com/k2-fsa/sherpa-onnx>

### ONNX Runtime (Microsoft)

Neural-network runtime (`libonnxruntime`), redistributed inside the release
bundle's `lib/`.
License: MIT. Copyright Microsoft Corporation.
<https://github.com/microsoft/onnxruntime>

### oto (Ebitengine)

Cross-platform audio playback library.
License: Apache License 2.0. Copyright the Ebitengine authors.
<https://github.com/ebitengine/oto>

## Models

### pocket-tts (Kyutai)

Text-to-speech model. The ONNX export bundle
(`sherpa-onnx-pocket-tts-2026-01-26.tar.bz2`, distributed via this repo's
GitHub release and downloaded at `emote provision` time) packages the
Kyutai pocket-tts weights converted to ONNX (conversion by KevinAHM,
packaged by the sherpa-onnx project).
License: CC BY 4.0 (model weights). Copyright Kyutai.
<https://huggingface.co/kyutai/pocket-tts>

Note: pocket-tts supports zero-shot voice cloning; its authors prohibit
cloning a voice without the speaker's consent. The bundled `test_wavs/`
demo audio inside the upstream tarball is treated as non-redistributable
demo material and is not used by emote-go.

## Voice data

### jane reference bank — synthetic, CC-BY-derived

The shipped voice references (`jane-bright-synth.wav`, `jane-sad-synth.wav`,
`jane-excited-synth.wav`, in `refs/` and the GitHub release) are **synthetic
renders**: pocket-tts output conditioned on the `jane` voice, which derives
from speaker p339 of the CSTR VCTK Corpus (CC BY 4.0), obtained via
[kyutai/tts-voices](https://huggingface.co/kyutai/tts-voices). No raw human
recordings are redistributed by this repository or its releases.

This project uses voice data derived from the CSTR VCTK Corpus, CC BY 4.0.

### Expresso and EARS — NOT used, NOT distributed

The Expresso and EARS datasets (CC BY-NC 4.0, non-commercial) power parts of
the sibling emote-chat server's local voice bank. **No material derived from
them enters this repository or any of its release artifacts.**

## Data

### Unicode emoji data

Emoji spoken names (`internal/extract/emoji_names.go`) are generated from
Unicode's `emoji-test.txt` and `emoji-data.txt` (Emoji 17.0).
License: Unicode License v3. Copyright Unicode, Inc.
<https://www.unicode.org/license.txt>

## Reference implementation

### emote-cli

The Python reference CLI whose behavior this port replicates
byte-for-byte (see `PARITY.md`). MIT.
<https://github.com/flipflop/emote-cli>

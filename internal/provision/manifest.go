package provision

// Asset is one downloadable/copied file, verified by sha256.
type Asset struct {
	// Name is the file name at the destination, the lookup name in a seed
	// dir, and the relative URL path under the asset base URL.
	Name   string
	SHA256 string
	Size   int64
}

// Manifest describes everything first-run provisioning must install.
type Manifest struct {
	// ModelTarball is the fp32 pocket-tts bundle (ship default tier).
	ModelTarball Asset
	// ModelDirName is the directory the tarball unpacks to; it is installed
	// under <cache>/models/.
	ModelDirName string
	// ModelFiles must all exist inside the installed model dir for the
	// model to count as provisioned.
	ModelFiles []string
	// Refs are the reference voice wavs, installed under <cache>/refs/.
	Refs []Asset
}

// Default is the P2 ship manifest. Hashes were recorded 2026-08-05 from the
// P1-verified artifacts:
//   - tarball: models/MANIFEST.txt in the source repo (upstream sherpa-onnx
//     release sherpa-onnx-pocket-tts-2026-01-26.tar.bz2, CC BY 4.0)
//   - refs: Emote-Go/refs/jane-{bright,sad,excited}-synth.wav — the
//     server-rendered jane synth references that passed the P1.7 onset QA
//     gate (RIFF headers already repaired on disk).
var Default = Manifest{
	ModelTarball: Asset{
		Name:   "sherpa-onnx-pocket-tts-2026-01-26.tar.bz2",
		SHA256: "61422a478ef09d9b2c067261fb822e11786e0c86842c76d8633b7069968be7b5",
		Size:   168148625,
	},
	ModelDirName: "sherpa-onnx-pocket-tts-2026-01-26",
	ModelFiles: []string{
		"lm_flow.onnx",
		"lm_main.onnx",
		"encoder.onnx",
		"decoder.onnx",
		"text_conditioner.onnx",
		"vocab.json",
		"token_scores.json",
	},
	Refs: []Asset{
		{
			Name:   "jane-bright-synth.wav",
			SHA256: "17ddb5d90977c30f08340ab3d65e5481f7fd7ff6933ffb9ffe826355bfd4acc5",
			Size:   455084,
		},
		{
			Name:   "jane-sad-synth.wav",
			SHA256: "7087eb331aac0500355b6da201d9496ebbe1f6a7e366d88d024d4d29956504b5",
			Size:   416684,
		},
		{
			Name:   "jane-excited-synth.wav",
			SHA256: "09efadd1c33902ea758c79e74cc69d226ad47961ed70a256a3afcc7159dd825d",
			Size:   497324,
		},
	},
}

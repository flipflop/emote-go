//go:build darwin

package provision

// Darwin 25's dyld aborts Go 1.21 internally-linked binaries ("missing
// LC_UUID load command") — the sibling of the P1 codesign SIGKILL quirk.
// Importing the (cgo) engine package forces external (clang) linking for this test
// binary, which emits LC_UUID and an acceptable ad-hoc signature, so a
// plain `go test ./internal/provision` runs without extra ldflags.
import _ "github.com/flipflop/emote-go/internal/engine"

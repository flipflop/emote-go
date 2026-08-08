#!/bin/sh
# build.sh — build (and MANDATORILY re-sign) the emote binary.
#
#   ./build.sh          build ./cmd/emote -> ./emote-go, then `codesign -f -s -`
#   ./build.sh test     go vet + go test over cmd/ and internal/
#
# The codesign step is NOT optional on macOS: this Darwin (25.x) SIGKILLs
# Go 1.21 linker-signed binaries on launch (CODESIGNING / Invalid Page —
# see P1-REPORT.md "Surprises" #1). Every local build must be re-signed.
set -eu
cd "$(dirname "$0")"

if [ "${1:-}" = "test" ]; then
    go vet ./cmd/... ./internal/...
    if [ "$(uname -s)" = "Darwin" ]; then
        # Test binaries need the same LC_UUID fix as the main build (below).
        go test -ldflags "-linkmode=external" ./cmd/... ./internal/...
    else
        go test ./cmd/... ./internal/...
    fi
    exit 0
fi

if [ "$(uname -s)" = "Darwin" ]; then
    # -linkmode=external: Go 1.21's internal Mach-O linker emits no LC_UUID
    # and this Darwin's dyld aborts such binaries; clang linking adds it.
    # (Once internal/engine lands, cgo forces external linking anyway.)
    go build -ldflags "-linkmode=external" -o emote-go ./cmd/emote
    codesign -f -s - emote-go
else
    go build -o emote-go ./cmd/emote
fi

echo "built ./emote-go"

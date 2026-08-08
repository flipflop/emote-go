package provision

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipflop/emote-go/internal/engine"
	"github.com/flipflop/emote-go/internal/speech"
)

// TestProvisionRealSeedEndToEnd provisions a fresh cache from the source
// checkout (models/ + refs/) using the REAL Default manifest — verifying the
// recorded sha256s against the actual P1 artifacts and the real tarball
// extraction — then boots the engine from that cache and renders a word.
//
// It extracts the 168 MB fp32 tarball (Go's bzip2 is slow, ~1-2 min), so it
// only runs when explicitly requested:
//
//	EMOTE_TEST_REAL_PROVISION=1 go test -run RealSeed -v ./internal/provision
func TestProvisionRealSeedEndToEnd(t *testing.T) {
	if os.Getenv("EMOTE_TEST_REAL_PROVISION") != "1" {
		t.Skip("set EMOTE_TEST_REAL_PROVISION=1 to run (slow: real 168 MB tarball extraction)")
	}
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	if _, err := os.Stat(filepath.Join(repo, "models", Default.ModelTarball.Name)); err != nil {
		t.Skipf("real tarball not in %s/models", repo)
	}
	for _, r := range Default.Refs {
		if _, err := os.Stat(filepath.Join(repo, "refs", r.Name)); err != nil {
			t.Skipf("ref %s not in %s/refs", r.Name, repo)
		}
	}

	cache := t.TempDir()
	o := options{
		cacheDir: cache,
		seedDir:  repo,
		manifest: Default,
		client:   http.DefaultClient,
	}
	if err := provision(o); err != nil {
		t.Fatalf("provision from real seed: %v (a sha256 mismatch here means manifest.go hashes are stale)", err)
	}
	if !provisionedIn(cache, Default) {
		t.Fatal("cache not provisioned")
	}

	e, err := engine.New(cache)
	if err != nil {
		t.Fatalf("engine from provisioned cache: %v", err)
	}
	defer e.Close()
	t.Logf("model dir: %s", e.ModelDir())
	a, err := speech.Render(e, "Hello!", "bright")
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleRate != 24000 || a.Duration() < 0.2 {
		t.Errorf("render: %.2fs @ %d Hz", a.Duration(), a.SampleRate)
	}
	t.Logf("rendered %.2fs @ %d Hz from provisioned cache", a.Duration(), a.SampleRate)
}

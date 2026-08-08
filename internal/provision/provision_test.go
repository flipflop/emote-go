package provision

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixtureTarball is a tiny tar.bz2 containing mini-model/ with the same file
// names as the real bundle (testdata/engine/, generated once for P2 tests).
const fixtureTarball = "../../testdata/engine/mini-model.tar.bz2"

var fixtureModelFiles = []string{
	"lm_flow.onnx", "lm_main.onnx", "encoder.onnx", "decoder.onnx",
	"text_conditioner.onnx", "vocab.json", "token_scores.json",
}

// testAssets builds a source dir (tarball + fake refs) and the matching
// manifest with real sha256s computed at runtime.
func testAssets(t *testing.T) (srcDir string, m Manifest) {
	t.Helper()
	srcDir = t.TempDir()

	tarBytes, err := os.ReadFile(fixtureTarball)
	if err != nil {
		t.Fatalf("missing fixture (regenerate per testdata/engine): %v", err)
	}
	tarName := "mini-model.tar.bz2"
	if err := os.WriteFile(filepath.Join(srcDir, tarName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	m = Manifest{
		ModelTarball: Asset{Name: tarName, SHA256: mustSHA(t, filepath.Join(srcDir, tarName)), Size: int64(len(tarBytes))},
		ModelDirName: "mini-model",
		ModelFiles:   fixtureModelFiles,
	}
	for _, name := range []string{"jane-bright-synth.wav", "jane-sad-synth.wav", "jane-excited-synth.wav"} {
		body := []byte("fake-ref-audio:" + name + strings.Repeat("x", 100))
		p := filepath.Join(srcDir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		m.Refs = append(m.Refs, Asset{Name: name, SHA256: mustSHA(t, p), Size: int64(len(body))})
	}
	return srcDir, m
}

func mustSHA(t *testing.T, path string) string {
	t.Helper()
	s, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// rangeRecorder serves srcDir with Range support and records Range headers.
type rangeRecorder struct {
	mu     sync.Mutex
	ranges []string
	inner  http.Handler
}

func (r *rangeRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if rg := req.Header.Get("Range"); rg != "" {
		r.mu.Lock()
		r.ranges = append(r.ranges, rg)
		r.mu.Unlock()
	}
	r.inner.ServeHTTP(w, req)
}

func serveDir(t *testing.T, dir string) (*httptest.Server, *rangeRecorder) {
	t.Helper()
	rec := &rangeRecorder{inner: http.FileServer(http.Dir(dir))}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv, rec
}

func opts(t *testing.T, m Manifest) options {
	return options{
		cacheDir: t.TempDir(),
		manifest: m,
		client:   http.DefaultClient,
	}
}

func TestProvisionFromServer(t *testing.T) {
	src, m := testAssets(t)
	srv, _ := serveDir(t, src)
	o := opts(t, m)
	o.baseURL = srv.URL

	if provisionedIn(o.cacheDir, m) {
		t.Fatal("fresh cache must not be provisioned")
	}
	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	if !provisionedIn(o.cacheDir, m) {
		t.Fatal("cache should be provisioned")
	}
	// Model files landed with contents.
	got, err := os.ReadFile(filepath.Join(o.cacheDir, "models", "mini-model", "vocab.json"))
	if err != nil || !strings.Contains(string(got), "vocab.json") {
		t.Errorf("model file wrong: %q err=%v", got, err)
	}
	// Refs sha-exact.
	for _, ref := range m.Refs {
		if !fileSHA256Matches(filepath.Join(o.cacheDir, "refs", ref.Name), ref.SHA256) {
			t.Errorf("ref %s sha mismatch after install", ref.Name)
		}
	}
	// Downloaded tarball reclaimed after extraction.
	if _, err := os.Stat(filepath.Join(o.cacheDir, "tmp", m.ModelTarball.Name)); !os.IsNotExist(err) {
		t.Error("tarball not cleaned up")
	}
	// Idempotent: second run with no server changes is a fast no-op.
	if err := provision(o); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
}

func TestProvisionFromSeedDir(t *testing.T) {
	src, m := testAssets(t)
	// Lay the seed out like a source checkout: models/ + refs/ subdirs.
	seed := t.TempDir()
	os.MkdirAll(filepath.Join(seed, "models"), 0o755)
	os.MkdirAll(filepath.Join(seed, "refs"), 0o755)
	mv := func(name, sub string) {
		b, _ := os.ReadFile(filepath.Join(src, name))
		os.WriteFile(filepath.Join(seed, sub, name), b, 0o644)
	}
	mv(m.ModelTarball.Name, "models")
	for _, r := range m.Refs {
		mv(r.Name, "refs")
	}

	o := opts(t, m)
	o.seedDir = seed // no baseURL: seed must be sufficient
	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	if !provisionedIn(o.cacheDir, m) {
		t.Fatal("cache should be provisioned from seed")
	}
	// Seed tarball must survive (only downloaded tarballs are deleted).
	if _, err := os.Stat(filepath.Join(seed, "models", m.ModelTarball.Name)); err != nil {
		t.Error("seed tarball was removed")
	}
}

func TestProvisionNoSource(t *testing.T) {
	_, m := testAssets(t)
	o := opts(t, m) // neither seedDir nor baseURL
	err := provision(o)
	if !errors.Is(err, ErrNoSource) {
		t.Fatalf("err = %v, want ErrNoSource", err)
	}
	if !strings.Contains(err.Error(), "EMOTE_ASSET_BASE") {
		t.Errorf("error should tell the user what to set: %v", err)
	}
}

func TestProvisionShaMismatch(t *testing.T) {
	src, m := testAssets(t)
	srv, _ := serveDir(t, src)
	m.Refs[0].SHA256 = strings.Repeat("0", 64) // sabotage
	o := opts(t, m)
	o.baseURL = srv.URL

	err := provision(o)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want sha256 mismatch", err)
	}
	// Nothing installed, no poisoned partial left to resume onto.
	if _, statErr := os.Stat(filepath.Join(o.cacheDir, "refs", m.Refs[0].Name)); !os.IsNotExist(statErr) {
		t.Error("corrupt ref must not be installed")
	}
	if _, statErr := os.Stat(filepath.Join(o.cacheDir, "tmp", m.Refs[0].Name+".partial")); !os.IsNotExist(statErr) {
		t.Error("corrupt partial must be deleted")
	}
}

func TestProvisionTarballShaMismatch(t *testing.T) {
	src, m := testAssets(t)
	srv, _ := serveDir(t, src)
	m.ModelTarball.SHA256 = strings.Repeat("f", 64)
	o := opts(t, m)
	o.baseURL = srv.URL

	err := provision(o)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want sha256 mismatch", err)
	}
	if modelInstalled(o.cacheDir, m) {
		t.Error("model must not be installed on sha mismatch")
	}
}

func TestDownloadResumesViaRange(t *testing.T) {
	src, m := testAssets(t)
	srv, rec := serveDir(t, src)
	o := opts(t, m)
	o.baseURL = srv.URL

	// Prepare the cache dirs, then plant a half-finished tarball download.
	for _, d := range []string{"models", "refs", "tmp"} {
		os.MkdirAll(filepath.Join(o.cacheDir, d), 0o755)
	}
	full, _ := os.ReadFile(fixtureTarball)
	half := len(full) / 2
	partial := filepath.Join(o.cacheDir, "tmp", m.ModelTarball.Name+".partial")
	if err := os.WriteFile(partial, full[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	if !provisionedIn(o.cacheDir, m) {
		t.Fatal("cache should be provisioned after resume")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	found := false
	for _, rg := range rec.ranges {
		if strings.HasPrefix(rg, "bytes=") {
			found = true
		}
	}
	if !found {
		t.Errorf("no Range request seen (got %v) — resume path not exercised", rec.ranges)
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	src, m := testAssets(t)
	// Server that always ignores Range and replies 200 with the full body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(src, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	o := opts(t, m)
	o.baseURL = srv.URL

	for _, d := range []string{"models", "refs", "tmp"} {
		os.MkdirAll(filepath.Join(o.cacheDir, d), 0o755)
	}
	// Poison the partial: server will ignore Range, so the file must be
	// truncated and rewritten, and the sha must still come out right.
	partial := filepath.Join(o.cacheDir, "tmp", m.ModelTarball.Name+".partial")
	os.WriteFile(partial, []byte("garbage-that-must-be-discarded"), 0o644)

	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	if !provisionedIn(o.cacheDir, m) {
		t.Fatal("cache should be provisioned")
	}
}

func TestProvisionLockExcludesConcurrent(t *testing.T) {
	src, m := testAssets(t)
	srv, _ := serveDir(t, src)
	o := opts(t, m)
	o.baseURL = srv.URL

	os.MkdirAll(o.cacheDir, 0o755)
	unlock, err := acquireLock(filepath.Join(o.cacheDir, ".provision.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// flock locks belong to the open file description, so a second open+
	// flock conflicts even within one process — this exercises the same
	// path a concurrent process would hit.
	err = provision(o)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent provision: err = %v, want ErrAlreadyRunning", err)
	}
	unlock()
	if err := provision(o); err != nil {
		t.Fatalf("provision after unlock: %v", err)
	}
}

func TestProvisionedFalseOnPartialRefs(t *testing.T) {
	src, m := testAssets(t)
	srv, _ := serveDir(t, src)
	o := opts(t, m)
	o.baseURL = srv.URL
	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	// Truncate one ref: Provisioned must notice via the size check.
	os.WriteFile(filepath.Join(o.cacheDir, "refs", m.Refs[1].Name), []byte("short"), 0o644)
	if provisionedIn(o.cacheDir, m) {
		t.Error("truncated ref should fail provisioned check")
	}
	// Re-provision heals it.
	if err := provision(o); err != nil {
		t.Fatal(err)
	}
	if !provisionedIn(o.cacheDir, m) {
		t.Error("re-provision should heal the truncated ref")
	}
}

func TestCacheDirEnvOverride(t *testing.T) {
	t.Setenv("EMOTE_CACHE_DIR", "/some/where")
	if got := CacheDir(); got != "/some/where" {
		t.Errorf("CacheDir = %q", got)
	}
	t.Setenv("EMOTE_CACHE_DIR", "")
	if got := CacheDir(); !strings.HasSuffix(got, filepath.Join(".cache", "emote")) {
		t.Errorf("CacheDir = %q, want ~/.cache/emote", got)
	}
}

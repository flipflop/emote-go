// Package provision installs the pocket-tts model bundle and the jane
// reference wavs into ~/.cache/emote on first run.
//
// Sources, in order: a local seed directory (EMOTE_SEED_DIR — the dev path,
// e.g. a checkout containing models/ and refs/), else HTTP download from
// the asset base URL (EMOTE_ASSET_BASE when set, else DefaultAssetBase —
// the GitHub release that hosts the model tarball and reference wavs).
// Every asset is sha256-verified and installed by atomic rename; the model
// tarball download resumes via HTTP Range; concurrent provisioning is
// excluded with flock.
//
// Provisioning is NEVER triggered automatically from a hook path — callers
// check Provisioned() and only call Provision from interactive commands.
package provision

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultAssetBase is the release URL assets are downloaded from when
// EMOTE_ASSET_BASE is not set (env still overrides for mirrors/testing).
const DefaultAssetBase = "https://github.com/flipflop/emote-go/releases/download/v0.1.0"

// Errors callers are expected to branch on.
var (
	// ErrAlreadyRunning means another process holds the provisioning lock.
	ErrAlreadyRunning = errors.New("provision: another provisioning run is in progress")
	// ErrNoSource means an asset is neither in the seed dir nor downloadable
	// (no base URL — only reachable in tests that exercise a sourceless
	// options struct; real runs always fall back to DefaultAssetBase).
	ErrNoSource = errors.New("provision: no asset source: set EMOTE_ASSET_BASE to the release asset URL, or EMOTE_SEED_DIR to a directory containing the model tarball and reference wavs")
)

// CacheDir returns the emote cache root: $EMOTE_CACHE_DIR if set, else
// ~/.cache/emote (the layout DESIGN.md fixes; deliberately not UserCacheDir
// so the path matches the Python emote's docs on macOS too).
func CacheDir() string {
	if d := os.Getenv("EMOTE_CACHE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".cache", "emote")
	}
	return filepath.Join(home, ".cache", "emote")
}

// ModelsDir returns <cache>/models, the directory internal/engine probes.
func ModelsDir() string { return filepath.Join(CacheDir(), "models") }

// RefsDir returns <cache>/refs, where reference voice wavs live.
func RefsDir() string { return filepath.Join(CacheDir(), "refs") }

// Provisioned reports whether the embedded engine can run from the cache:
// the fp32 model bundle (or an equivalent previously installed tier) is
// complete and every manifest reference wav is present with the right size.
// It is cheap (stat-only) — safe to call from hook paths.
func Provisioned() bool {
	return provisionedIn(CacheDir(), Default)
}

// Provision installs any missing assets into CacheDir. interactive enables
// the TTY progress bar and informational output on stderr; non-interactive
// runs are silent. It never runs concurrently with itself across processes
// (flock); a second caller gets ErrAlreadyRunning immediately.
func Provision(interactive bool) error {
	return provision(options{
		cacheDir:    CacheDir(),
		seedDir:     os.Getenv("EMOTE_SEED_DIR"),
		baseURL:     assetBase(),
		manifest:    Default,
		interactive: interactive,
		client:      &http.Client{Timeout: 0}, // long downloads; no global timeout
	})
}

// assetBase returns EMOTE_ASSET_BASE when set (mirror/test override), else
// DefaultAssetBase (the GitHub release hosting the model tarball and refs).
func assetBase() string {
	if b := os.Getenv("EMOTE_ASSET_BASE"); b != "" {
		return b
	}
	return DefaultAssetBase
}

// options carries everything provision needs, so tests can substitute a
// temp cache, a fixture manifest and an httptest server.
type options struct {
	cacheDir    string
	seedDir     string
	baseURL     string
	manifest    Manifest
	interactive bool
	client      *http.Client
}

func provisionedIn(cacheDir string, m Manifest) bool {
	if !modelInstalled(cacheDir, m) {
		return false
	}
	for _, ref := range m.Refs {
		fi, err := os.Stat(filepath.Join(cacheDir, "refs", ref.Name))
		if err != nil || fi.Size() != ref.Size {
			return false
		}
	}
	return true
}

func modelInstalled(cacheDir string, m Manifest) bool {
	dir := filepath.Join(cacheDir, "models", m.ModelDirName)
	for _, f := range m.ModelFiles {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

func provision(o options) error {
	for _, d := range []string{o.cacheDir, filepath.Join(o.cacheDir, "models"), filepath.Join(o.cacheDir, "refs"), filepath.Join(o.cacheDir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
	}

	unlock, err := acquireLock(filepath.Join(o.cacheDir, ".provision.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	for _, ref := range o.manifest.Refs {
		if err := o.installRef(ref); err != nil {
			return err
		}
	}
	if err := o.installModel(); err != nil {
		return err
	}
	o.sayf("emote: provisioning complete (%s)\n", o.cacheDir)
	return nil
}

// installRef puts one reference wav in place (skip if already valid).
func (o *options) installRef(ref Asset) error {
	dest := filepath.Join(o.cacheDir, "refs", ref.Name)
	if fileSHA256Matches(dest, ref.SHA256) {
		return nil
	}
	tmp := filepath.Join(o.cacheDir, "tmp", ref.Name)
	if seed := o.findSeed(ref.Name, "refs"); seed != "" {
		o.sayf("emote: copying %s from seed\n", ref.Name)
		if err := copyFile(seed, tmp); err != nil {
			return fmt.Errorf("provision: %s: %w", ref.Name, err)
		}
	} else if o.baseURL != "" {
		if err := o.download(ref, tmp); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("%w (missing %s)", ErrNoSource, ref.Name)
	}
	if err := verifySHA256(tmp, ref.SHA256); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("provision: %s: %w", ref.Name, err)
	}
	return os.Rename(tmp, dest)
}

// installModel obtains the fp32 tarball (seed copy or resumable download),
// verifies it, and unpacks it atomically under models/.
func (o *options) installModel() error {
	m := o.manifest
	if modelInstalled(o.cacheDir, m) {
		return nil
	}
	tarPath := filepath.Join(o.cacheDir, "tmp", m.ModelTarball.Name)
	fromSeed := false
	if seed := o.findSeed(m.ModelTarball.Name, "models"); seed != "" {
		o.sayf("emote: using model tarball from seed: %s\n", seed)
		tarPath = seed
		fromSeed = true
	} else if !fileSHA256Matches(tarPath, m.ModelTarball.SHA256) {
		if o.baseURL == "" {
			return fmt.Errorf("%w (missing %s)", ErrNoSource, m.ModelTarball.Name)
		}
		if err := o.download(m.ModelTarball, tarPath); err != nil {
			return err
		}
	}
	if err := verifySHA256(tarPath, m.ModelTarball.SHA256); err != nil {
		if !fromSeed {
			os.Remove(tarPath)
		}
		return fmt.Errorf("provision: %s: %w", m.ModelTarball.Name, err)
	}

	o.sayf("emote: unpacking model (~450 MB, one-time)...\n")
	extractTmp := filepath.Join(o.cacheDir, "tmp", fmt.Sprintf(".extract-%d", os.Getpid()))
	defer os.RemoveAll(extractTmp)
	if err := extractTarBz2(tarPath, extractTmp); err != nil {
		return fmt.Errorf("provision: extracting %s: %w", m.ModelTarball.Name, err)
	}
	unpacked := filepath.Join(extractTmp, m.ModelDirName)
	if fi, err := os.Stat(unpacked); err != nil || !fi.IsDir() {
		return fmt.Errorf("provision: tarball did not contain expected dir %s", m.ModelDirName)
	}
	dest := filepath.Join(o.cacheDir, "models", m.ModelDirName)
	os.RemoveAll(dest) // stale partial install, if any
	if err := os.Rename(unpacked, dest); err != nil {
		return fmt.Errorf("provision: installing model dir: %w", err)
	}
	if !fromSeed {
		os.Remove(tarPath) // reclaim the 168 MB download
	}
	return nil
}

// findSeed looks for name in the seed dir, both flat and under subdir
// (so EMOTE_SEED_DIR can point at a source checkout with models/ + refs/).
func (o *options) findSeed(name, subdir string) string {
	if o.seedDir == "" {
		return ""
	}
	for _, p := range []string{
		filepath.Join(o.seedDir, subdir, name),
		filepath.Join(o.seedDir, name),
	} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// download fetches asset into dest, resuming any dest+".partial" via HTTP
// Range, with a TTY progress bar when interactive.
func (o *options) download(a Asset, dest string) error {
	url := strings.TrimSuffix(o.baseURL, "/") + "/" + a.Name
	partial := dest + ".partial"

	var offset int64
	if fi, err := os.Stat(partial); err == nil {
		offset = fi.Size()
	}
	if offset > a.Size {
		os.Remove(partial)
		offset = 0
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("provision: downloading %s: %w", a.Name, err)
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		flags |= os.O_APPEND
		o.sayf("emote: resuming %s at %s\n", a.Name, fmtBytes(offset))
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC // server ignored/never got Range: start over
		offset = 0
	default:
		return fmt.Errorf("provision: downloading %s: HTTP %s", a.Name, resp.Status)
	}

	f, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	bar := newProgress(o.progressWriter(), a.Name, a.Size, offset)
	_, copyErr := io.Copy(io.MultiWriter(f, bar), resp.Body)
	closeErr := f.Close()
	bar.finish(copyErr == nil && closeErr == nil)
	if copyErr != nil {
		return fmt.Errorf("provision: downloading %s (partial kept for resume): %w", a.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("provision: %w", closeErr)
	}
	if err := verifySHA256(partial, a.SHA256); err != nil {
		os.Remove(partial) // corrupt: do not resume onto bad bytes
		return fmt.Errorf("provision: %s: %w", a.Name, err)
	}
	return os.Rename(partial, dest)
}

func (o *options) sayf(format string, args ...any) {
	if o.interactive {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// progressWriter returns stderr when a live progress bar is wanted
// (interactive + stderr is a terminal), else nil (silent).
func (o *options) progressWriter() io.Writer {
	if !o.interactive {
		return nil
	}
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return os.Stderr
	}
	return nil
}

// --- small file helpers ----------------------------------------------------

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256Matches(path, want string) bool {
	got, err := fileSHA256(path)
	return err == nil && got == want
}

func verifySHA256(path, want string) error {
	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// extractTarBz2 unpacks a .tar.bz2 into destDir, refusing path traversal.
func extractTarBz2(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bzip2.NewReader(f))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(destDir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o400)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// symlinks/others are not expected in the model tarball; skip.
		}
	}
}

// --- progress bar ----------------------------------------------------------

type progress struct {
	w        io.Writer
	name     string
	total    int64
	done     int64
	lastDraw time.Time
}

func newProgress(w io.Writer, name string, total, alreadyDone int64) *progress {
	return &progress{w: w, name: name, total: total, done: alreadyDone}
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.w != nil && (time.Since(p.lastDraw) > 100*time.Millisecond || p.done == p.total) {
		p.lastDraw = time.Now()
		p.draw()
	}
	return len(b), nil
}

func (p *progress) draw() {
	const width = 28
	frac := 0.0
	if p.total > 0 {
		frac = float64(p.done) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
	}
	filled := int(frac * width)
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
	fmt.Fprintf(p.w, "\r  %s [%s] %3.0f%%  %s / %s", p.name, bar, frac*100, fmtBytes(p.done), fmtBytes(p.total))
}

func (p *progress) finish(ok bool) {
	if p.w == nil {
		return
	}
	p.draw()
	if ok {
		fmt.Fprintln(p.w)
	} else {
		fmt.Fprintln(p.w, "  (interrupted)")
	}
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

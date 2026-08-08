// Package play handles audio playback for emote.
//
// Parent side: a finished utterance is one or more temp WAV files; Start
// spawns a detached self-exec child (`emote __play <wav>...`) in its own
// session and records its pid in the SAME pidfile the Python emote uses
// (~/.config/emote/afplay.pid), so the two binaries can interrupt each
// other's playback. Child side (RunChild): decode WAVs and play them
// through oto/v3 (CoreAudio), falling back to `afplay` via EMOTE_PLAYER
// or whenever oto cannot handle the file; delete the temp files when done.
package play

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultPidfile mirrors emote_cli.config.pidfile_path():
// $EMOTE_CONFIG_DIR/afplay.pid, else ~/.config/emote/afplay.pid.
// The name is kept for cross-compatibility with the Python emote even
// though the player is usually oto here.
func DefaultPidfile() string {
	dir := os.Getenv("EMOTE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".config", "emote")
	}
	return filepath.Join(dir, "afplay.pid")
}

// CurrentlyPlaying returns the pid recorded in pidfile if that process is
// still alive. A stale pidfile is removed.
func CurrentlyPlaying(pidfile string) (int, bool) {
	data, err := os.ReadFile(pidfile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.EPERM {
			return pid, true // alive, owned by someone else
		}
		os.Remove(pidfile) // stale
		return 0, false
	}
	return pid, true
}

// Stop kills any playback recorded in the pidfile (whole process group
// first — the child runs in its own session — then the process itself),
// and removes the pidfile.
func Stop(pidfile string) {
	if pid, ok := CurrentlyPlaying(pidfile); ok {
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	os.Remove(pidfile)
}

// Start launches `exe __play wavs...` detached (own session, null stdio),
// records the child's pid in pidfile, and returns immediately
// (fire-and-forget). The child deletes the wav files when it is done.
func Start(exe, pidfile string, wavs []string) (int, error) {
	if len(wavs) == 0 {
		return 0, nil
	}
	cmd := exec.Command(exe, append([]string{"__play"}, wavs...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detached, killable via killpg
	// Stdin/Stdout/Stderr nil => /dev/null (os/exec contract).
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start player: %w", err)
	}
	go cmd.Wait() // reap if the parent happens to outlive the child

	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		return cmd.Process.Pid, err
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return cmd.Process.Pid, err
	}
	return cmd.Process.Pid, nil
}

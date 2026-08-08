package play

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/ebitengine/oto/v3"
)

// RunChild is the implementation of the hidden `emote __play <wav>...`
// subcommand: play each WAV in order, then delete them all. It runs in a
// detached session started by Start, so a Stop from any emote process
// (Go or Python) SIGTERMs this whole process group.
//
// Backend: oto/v3 (CoreAudio) by default; `afplay` when EMOTE_PLAYER=afplay,
// or per-file whenever oto cannot handle the file (init failure, non-PCM16,
// sample-rate change mid-batch — oto allows one context per process).
// Returns a process exit code.
func RunChild(wavs []string) int {
	if len(wavs) == 0 {
		return 0
	}

	// Delete the temp files even if we are interrupted mid-playback.
	cleanup := func() {
		for _, w := range wavs {
			os.Remove(w)
		}
	}
	defer cleanup()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigc
		cleanup()
		os.Exit(1)
	}()

	forceAfplay := os.Getenv("EMOTE_PLAYER") == "afplay"

	var ctx *oto.Context
	ctxRate, ctxCh := 0, 0
	ok := true
	for _, path := range wavs {
		if err := playOne(path, forceAfplay, &ctx, &ctxRate, &ctxCh); err != nil {
			fmt.Fprintf(os.Stderr, "emote __play: %s: %v\n", path, err)
			ok = false
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// playOne plays a single WAV, preferring oto and falling back to afplay.
// A single oto context is lazily created for the process (*ctx); files
// whose format the existing context cannot take go through afplay.
func playOne(path string, forceAfplay bool, ctx **oto.Context, ctxRate, ctxCh *int) error {
	if !forceAfplay {
		info, err := parseWav(path)
		if err == nil && info.audioFormat == 1 && info.bits == 16 &&
			(info.channels == 1 || info.channels == 2) {
			if *ctx == nil {
				c, ready, err := oto.NewContext(&oto.NewContextOptions{
					SampleRate:   info.sampleRate,
					ChannelCount: info.channels,
					Format:       oto.FormatSignedInt16LE,
				})
				if err == nil {
					<-ready
					*ctx, *ctxRate, *ctxCh = c, info.sampleRate, info.channels
				}
			}
			if *ctx != nil && *ctxRate == info.sampleRate && *ctxCh == info.channels {
				return otoPlay(*ctx, path, info)
			}
		}
	}
	return afplayPlay(path)
}

func otoPlay(ctx *oto.Context, path string, info wavInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(info.dataOffset, io.SeekStart); err != nil {
		return err
	}
	p := ctx.NewPlayer(io.LimitReader(f, info.dataLen))
	p.Play()
	for p.IsPlaying() {
		time.Sleep(10 * time.Millisecond)
	}
	// IsPlaying() reports false once all samples are SUBMITTED to the device,
	// while the hardware buffer still holds the tail; exiting now clips the
	// final word ("Say something nice to—"). 300 ms proved too tight on this
	// hardware — drain generously; a detached child has nowhere to hurry to.
	time.Sleep(700 * time.Millisecond)
	return p.Close()
}

func afplayPlay(path string) error {
	afplay, err := exec.LookPath("afplay")
	if err != nil {
		return fmt.Errorf("no oto playback and afplay not found")
	}
	cmd := exec.Command(afplay, path)
	return cmd.Run() // same process group: killpg interrupts it too
}

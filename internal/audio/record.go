package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RecordWav records from the default audio source to a 16kHz mono wav.
// source: "" = system default; maxDur 0 = until ctx cancelled.
// Returns path to the wav file.
func RecordWav(ctx context.Context, source string, maxDur time.Duration) (string, error) {
	dir := filepath.Join(os.TempDir(), "devin-speech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := wavPathIn(dir)

	// ALSA device syntax (contains ':') bypasses PipeWire/Pulse entirely;
	// needed for hot-plugged cards like snd-aloop that PipeWire mis-binds.
	if strings.Contains(source, ":") {
		args := []string{"-D", source, "-f", "S16_LE", "-r", "16000", "-c", "1", "-t", "wav"}
		if maxDur > 0 {
			args = append(args, "-d", fmt.Sprint(int(maxDur.Seconds())))
		}
		args = append(args, out)
		cmd := exec.CommandContext(ctx, "arecord", args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if _, statErr := os.Stat(out); statErr != nil {
				return "", fmt.Errorf("arecord failed: %w", err)
			}
		}
		if _, err := os.Stat(out); err != nil {
			return "", fmt.Errorf("recorder produced no file: %w", err)
		}
		fixWavHeader(out)
		return out, nil
	}

	bin := "pw-record"
	args := []string{"--rate", "16000", "--channels", "1", out}
	if source != "" {
		args = append([]string{"--target", source}, args...)
	}
	if _, err := exec.LookPath(bin); err != nil {
		bin = "parec"
		args = []string{"--format=s16le", "--rate=16000", "--channels=1", "--file-format=wav"}
		if source != "" {
			args = append([]string{"--device=" + source}, args...)
		}
		args = append(args, out)
		if _, err := exec.LookPath(bin); err != nil {
			return "", fmt.Errorf("no recorder found (pw-record/parec): %w", err)
		}
	}

	if maxDur > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxDur)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// context deadline = normal stop for maxDur; real errors otherwise
		if ctx.Err() == context.DeadlineExceeded && maxDur > 0 {
			// fallthrough: file should exist
		} else if _, statErr := os.Stat(out); statErr != nil {
			return "", fmt.Errorf("%s failed: %w", bin, err)
		}
	}
	if _, err := os.Stat(out); err != nil {
		return "", fmt.Errorf("recorder produced no file: %w", err)
	}
	fixWavHeader(out)
	return out, nil
}

// fixWavHeader patches RIFF/data sizes that pw-record and parec leave at 0
// when killed mid-write; whisper.cpp's dr_wav rejects those files.
func fixWavHeader(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return
	}
	st, err := f.Stat()
	if err != nil || st.Size() < 44 {
		return
	}
	size := uint32(st.Size())
	binary.LittleEndian.PutUint32(hdr[4:8], size-8)
	if _, err := f.WriteAt(hdr[4:8], 4); err != nil {
		return
	}
	// data chunk size sits at offset 40 for the canonical 44-byte PCM header
	var ds [4]byte
	binary.LittleEndian.PutUint32(ds[:], size-44)
	f.WriteAt(ds[:], 40)
}

func wavPathIn(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("cap-%d.wav", time.Now().UnixNano()))
}

// EnsureSource checks a default audio source exists.
func EnsureSource(ctx context.Context) error {
	for _, bin := range []string{"pactl", "wpctl"} {
		if _, err := exec.LookPath(bin); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no pactl/wpctl found; cannot verify audio source")
}

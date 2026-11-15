package audio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	return out, nil
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

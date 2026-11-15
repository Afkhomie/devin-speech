package inject

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

// TypeText types text into the focused X11 window via xdotool XTEST.
func TypeText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	return run(ctx, "xdotool", "type", "--delay", "12", "--", text)
}

// Key sends a key combo, e.g. Key(ctx, "Return") or Key(ctx, "ctrl+a").
func Key(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := append([]string{"key"}, keys...)
	return run(ctx, "xdotool", args...)
}

// EraseSpaces sends n BackSpace presses.
func EraseSpaces(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	return run(ctx, "xdotool", "key", "--repeat", strconv.Itoa(n), "--delay", "20", "BackSpace")
}

// NormalizeCaps toggles Caps_Lock off when caps is currently on,
// so injected text isn't uppercase.
func NormalizeCaps(ctx context.Context, capsOn bool) error {
	if !capsOn {
		return nil
	}
	return run(ctx, "xdotool", "key", "Caps_Lock")
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, out)
	}
	return nil
}

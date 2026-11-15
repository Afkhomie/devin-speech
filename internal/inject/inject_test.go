package inject

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func fakeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "` + dir + `/calls.log"
`
	os.WriteFile(dir+"/xdotool", []byte(script), 0o755)
	os.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Cleanup(func() { os.Setenv("PATH", os.Getenv("PATH")) })
	return dir + "/calls.log"
}

func readCalls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestTypeText(t *testing.T) {
	log := fakeBin(t)
	ctx := context.Background()
	if err := TypeText(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}
	got := readCalls(t, log)
	want := "type --delay 12 -- hello world\n"
	if got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestKey(t *testing.T) {
	log := fakeBin(t)
	ctx := context.Background()
	if err := Key(ctx, "Return"); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, log); got != "key Return\n" {
		t.Fatalf("calls = %q", got)
	}
}

func TestEraseSpaces(t *testing.T) {
	log := fakeBin(t)
	ctx := context.Background()
	if err := EraseSpaces(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, log); got != "key --repeat 2 --delay 20 BackSpace\n" {
		t.Fatalf("calls = %q", got)
	}
}

func TestNormalizeCapsOn(t *testing.T) {
	log := fakeBin(t)
	ctx := context.Background()
	if err := NormalizeCaps(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, log); got != "key Caps_Lock\n" {
		t.Fatalf("calls = %q", got)
	}
}

func TestNormalizeCapsOffDoesNothing(t *testing.T) {
	log := fakeBin(t)
	ctx := context.Background()
	if err := NormalizeCaps(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, log); got != "" {
		t.Fatalf("expected no calls, got %q", got)
	}
}

var _ = exec.LookPath

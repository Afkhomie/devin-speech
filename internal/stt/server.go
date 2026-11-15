package stt

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// EnsureServer starts whisper-server if nothing answers on serverURL.
// Blocks until the server responds or ctx is done.
func EnsureServer(ctx context.Context, serverURL, bin, modelPath string) error {
	if serverUp(ctx, serverURL) {
		return nil
	}
	args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", portOf(serverURL)}
	cmd := exec.CommandContext(context.Background(), bin, args...)
	// detached: outlive caller ctx; killed on process exit via Kill on daemon shutdown is fine for v1
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start whisper-server: %w", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if serverUp(ctx, serverURL) {
			return nil
		}
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	cmd.Process.Kill()
	return fmt.Errorf("whisper-server did not become ready in 90s")
}

func serverUp(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(url, "/")+"/", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func portOf(url string) string {
	// serverURL like http://127.0.0.1:8080
	i := strings.LastIndex(url, ":")
	if i < 0 {
		return "8080"
	}
	return strings.TrimRight(url[i+1:], "/")
}

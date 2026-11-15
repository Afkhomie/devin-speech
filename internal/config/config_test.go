package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nonexistent.toml"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if cfg.WhisperURL != "http://127.0.0.1:8080" {
		t.Fatalf("default WhisperURL = %q", cfg.WhisperURL)
	}
	if cfg.PermissionMode != "bypass" {
		t.Fatalf("default PermissionMode = %q", cfg.PermissionMode)
	}
}

func TestLoadMalformedReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(p, []byte("this is = = not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for malformed toml")
	}
}

func TestLoadOverrides(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.toml")
	content := `
whisper_url = "http://127.0.0.1:9999"
project_dir = "/tmp/somewhere"
[tts]
voice = "en_US-amy-medium.onnx"
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WhisperURL != "http://127.0.0.1:9999" {
		t.Fatalf("WhisperURL = %q", cfg.WhisperURL)
	}
	if cfg.ProjectDir != "/tmp/somewhere" {
		t.Fatalf("ProjectDir = %q", cfg.ProjectDir)
	}
	if cfg.TTS.Voice != "en_US-amy-medium.onnx" {
		t.Fatalf("TTS.Voice = %q", cfg.TTS.Voice)
	}
}

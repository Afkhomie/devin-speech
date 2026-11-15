package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Keys struct {
	CapsLock string `toml:"capslock"`
	Spacebar string `toml:"spacebar"`
}

type STT struct {
	ModelPath string `toml:"model_path"`
	ServerBin string `toml:"server_bin"`
	AccentPrompt string `toml:"accent_prompt"`
}

type TTS struct {
	Bin   string `toml:"bin"`
	Voice string `toml:"voice"`
	Player string `toml:"player"`
}

type Tray struct {
	Enabled bool `toml:"enabled"`
}

type Config struct {
	WhisperURL     string `toml:"whisper_url"`
	ProjectDir     string `toml:"project_dir"`
	PermissionMode string `toml:"permission_mode"`
	Keys           Keys   `toml:"keys"`
	STT            STT    `toml:"stt"`
	TTS            TTS    `toml:"tts"`
	Tray           Tray   `toml:"tray"`
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		WhisperURL:     "http://127.0.0.1:8080",
		ProjectDir:     "",
		PermissionMode: "bypass",
		Keys: Keys{
			CapsLock: "KEY_CAPSLOCK",
			Spacebar: "KEY_SPACE",
		},
		STT: STT{
			ModelPath:    filepath.Join(home, "tools/models/ggml-tiny.en-q8_0.bin"),
			ServerBin:    filepath.Join(home, "tools/whisper.cpp/build/bin/whisper-server"),
			AccentPrompt: "The speaker has a Nepali-English accent.",
		},
		TTS: TTS{
			Bin:    filepath.Join(home, "tools/piper/piper"),
			Voice:  filepath.Join(home, "tools/voices/en_US-lessac-medium.onnx"),
			Player: "paplay",
		},
		Tray: Tray{Enabled: true},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}
	if cfg.ProjectDir == "" {
		cwd, _ := os.Getwd()
		cfg.ProjectDir = cwd
	}
	return cfg, nil
}

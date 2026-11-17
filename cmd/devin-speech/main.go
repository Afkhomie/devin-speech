package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Afkhomie/devin-speech/internal/config"
	"github.com/Afkhomie/devin-speech/internal/keys"
	"github.com/Afkhomie/devin-speech/internal/orchestrator"
	"github.com/Afkhomie/devin-speech/internal/tray"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debugKeys := flag.Bool("debug-keys", false, "log raw key events")
	flag.Parse()

	if *configPath == "" {
		home, _ := os.UserHomeDir()
		*configPath = filepath.Join(home, ".config", "devin-speech", "config.toml")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{
		WhisperURL:     cfg.WhisperURL,
		WhisperBin:     cfg.STT.ServerBin,
		WhisperModel:   cfg.STT.ModelPath,
		AccentPrompt:   cfg.STT.AccentPrompt,
		STTBackend:     cfg.STT.Backend,
		STTBaseURL:     cfg.STT.BaseURL,
		STTModel:       cfg.STT.Model,
		STTAPIKey:      cfg.STT.APIKey,
		PiperBin:       cfg.TTS.Bin,
		PiperVoice:     cfg.TTS.Voice,
		PiperPlayer:    cfg.TTS.Player,
		ProjectDir:     cfg.ProjectDir,
		PermissionMode: cfg.PermissionMode,
		AudioSource:    cfg.Audio.Source,
		Keys:           keys.MachineConfig{},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Tray.Enabled {
		go tray.Run(ctx, orch.StateCh())
	}
	log.Printf("devin-speech started (project=%s source=%q)", cfg.ProjectDir, cfg.Audio.Source)
	if err := orch.Run(ctx, *debugKeys); err != nil {
		log.Fatalf("run: %v", err)
	}
}

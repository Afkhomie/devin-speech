package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Afkhomie/devin-speech/internal/config"
	"github.com/Afkhomie/devin-speech/internal/keys"
	"github.com/Afkhomie/devin-speech/internal/orchestrator"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debugKeys := flag.Bool("debug-keys", false, "log raw key events")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{
		WhisperURL:     cfg.WhisperURL,
		WhisperBin:     cfg.STT.ServerBin,
		WhisperModel:   cfg.STT.ModelPath,
		AccentPrompt:   cfg.STT.AccentPrompt,
		PiperBin:       cfg.TTS.Bin,
		PiperVoice:     cfg.TTS.Voice,
		PiperPlayer:    cfg.TTS.Player,
		ProjectDir:     cfg.ProjectDir,
		PermissionMode: cfg.PermissionMode,
		Keys:           keys.MachineConfig{},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("devin-speech started (project=%s)", cfg.ProjectDir)
	if err := orch.Run(ctx, *debugKeys); err != nil {
		log.Fatalf("run: %v", err)
	}
}

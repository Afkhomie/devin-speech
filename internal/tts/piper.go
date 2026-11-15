package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Speaker interface {
	Speak(ctx context.Context, text string) error
}

type Piper struct {
	bin    string
	voice  string
	player string
	tmpDir string
}

func NewPiperSpeaker(bin, voice, player string) *Piper {
	return &Piper{bin: bin, voice: voice, player: player, tmpDir: filepath.Join(os.TempDir(), "devin-speech")}
}

// Speak synthesizes one sentence to wav and plays it via player.
func (p *Piper) Speak(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if err := os.MkdirAll(p.tmpDir, 0o755); err != nil {
		return err
	}
	wav := filepath.Join(p.tmpDir, fmt.Sprintf("tts-%d.wav", time.Now().UnixNano()))
	defer os.Remove(wav)

	cmd := exec.CommandContext(ctx, p.bin, "--model", p.voice, "--output-file", wav)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("piper: %w", err)
	}
	play := exec.CommandContext(ctx, p.player, wav)
	play.Stdout = os.Stderr
	play.Stderr = os.Stderr
	return play.Run()
}

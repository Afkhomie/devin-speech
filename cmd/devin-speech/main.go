package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Afkhomie/devin-speech/internal/config"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debugKeys := flag.Bool("debug-keys", false, "log raw key events")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("devin-speech started (debug-keys=%v, project=%s)", *debugKeys, cfg.ProjectDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Println("shutting down")
}

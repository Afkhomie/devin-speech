package tray

import (
	"context"
	"log"

	"github.com/remko/go-sni"

	"github.com/Afkhomie/devin-speech/internal/orchestrator"
)

var stateIcons = map[orchestrator.State]struct{ icon, tip string }{
	orchestrator.StateIdle:            {"dialog-information", "devin-speech: idle"},
	orchestrator.StateDictating:       {"media-record", "devin-speech: dictating (release capslock)"},
	orchestrator.StateDictationLocked: {"media-record", "devin-speech: dictating LOCKED (tap capslock to send)"},
	orchestrator.StateVoiceListening:  {"audio-input-microphone", "devin-speech: voice mode - listening"},
	orchestrator.StateVoiceThinking:   {"emblem-synchronizing", "devin-speech: devin is thinking"},
	orchestrator.StateVoiceSpeaking:   {"audio-speakers", "devin-speech: speaking"},
}

// Run shows a tray icon reflecting orchestrator state until ctx ends.
func Run(ctx context.Context, stateCh <-chan orchestrator.State) {
	item, err := sni.NewItem(sni.ItemConfig{
		Category: "ApplicationStatus",
		ID:       "devin-speech",
		Title:    "devin-speech",
		Status:   "Active",
		Icon:     sni.Icon{Name: "dialog-information"},
		Tooltip:  sni.Tooltip{Title: "devin-speech", Text: "idle"},
	})
	if err != nil {
		log.Printf("tray: %v (continuing without tray)", err)
		return
	}
	defer item.Close()

	item.Activate = func(x, y int) {}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("tray: %v (no StatusNotifier host, continuing without tray)", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case st := <-stateCh:
			m, ok := stateIcons[st]
			if !ok {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				item.SetIcon(sni.Icon{Name: m.icon})
				item.SetTooltip(sni.Tooltip{Title: "devin-speech", Text: m.tip})
			}()
		}
	}
}

package orchestrator

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Afkhomie/devin-speech/internal/acp"
	"github.com/Afkhomie/devin-speech/internal/audio"
	"github.com/Afkhomie/devin-speech/internal/inject"
	"github.com/Afkhomie/devin-speech/internal/keys"
	"github.com/Afkhomie/devin-speech/internal/stt"
	"github.com/Afkhomie/devin-speech/internal/switcher"
	"github.com/Afkhomie/devin-speech/internal/tts"
)

type Deps struct {
	WhisperURL     string
	WhisperBin     string
	WhisperModel   string
	AccentPrompt   string
	PiperBin       string
	PiperVoice     string
	PiperPlayer    string
	ProjectDir     string
	PermissionMode string
	Keys           keys.MachineConfig
}

type State int

const (
	StateIdle State = iota
	StateDictating
	StateDictationLocked
	StateVoiceListening
	StateVoiceThinking
	StateVoiceSpeaking
)

type Orchestrator struct {
	d      Deps
	machine *keys.Machine
	sw     *switcher.Switcher
	speaker tts.Speaker
	stateCh chan State
	mu      sync.Mutex
	state   State

	recCancel context.CancelFunc
	recDone   chan string

	voiceClient *acp.Client
	voiceSID    string
	voiceActive bool
	voiceCancel context.CancelFunc
}

func New(d Deps) *Orchestrator {
	return &Orchestrator{
		d:        d,
		machine:  keys.NewMachine(d.Keys),
		sw:       switcher.New(d.ProjectDir, d.PermissionMode),
		speaker:  tts.NewPiperSpeaker(d.PiperBin, d.PiperVoice, d.PiperPlayer),
		stateCh:  make(chan State, 8),
		recDone:  make(chan string, 1),
	}
}

func (o *Orchestrator) StateCh() <-chan State { return o.stateCh }

func (o *Orchestrator) setState(s State) {
	o.mu.Lock()
	o.state = s
	o.mu.Unlock()
	select {
	case o.stateCh <- s:
	default:
	}
}

func (o *Orchestrator) Run(ctx context.Context, debugKeys bool) error {
	events := make(chan keys.Event, 64)
	go keys.Listen(ctx, o.machine, events, debugKeys)

	// VAD for voice mode: lazily started when voice activates
	vadCtx, vadCancel := context.WithCancel(ctx)
	defer vadCancel()
	vadOut := make(chan string, 4)
	go func() {
		v := audio.DefaultVad()
		if err := v.Listen(vadCtx, vadOut); err != nil && ctx.Err() == nil {
			log.Printf("vad: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			o.shutdownVoice(ctx)
			return nil
		case ev := <-events:
			o.handleKey(ev)
		case wav := <-vadOut:
			if o.voiceActive {
				go o.handleUtterance(ctx, wav)
			}
		}
	}
}

func (o *Orchestrator) handleKey(ev keys.Event) {
	switch ev {
	case keys.EvDictationBegin:
		if o.state == StateDictationLocked {
			return // already recording locked; presses suppressed anyway
		}
		o.startRecording()
	case keys.EvDictationCommit:
		if o.state == StateDictating {
			o.stopRecordingAndSend()
		}
	case keys.EvDictationTap:
		if o.state == StateDictating {
			o.discardRecording()
		}
	case keys.EvDictationLockOn:
		if o.state == StateDictationLocked || o.state == StateDictating {
			// promote current hold-recording to locked, or just lock
			if o.recCancel != nil {
				// keep recording: swap done channel semantics - simplest: restart locked recording
				o.recCancel()
			}
			o.startLockedRecording()
		}
	case keys.EvDictationLockOff:
		if o.state == StateDictationLocked {
			o.stopRecordingAndSend()
		}
	case keys.EvVoiceToggle:
		if o.voiceActive {
			o.exitVoice()
		} else {
			o.enterVoice()
		}
	case keys.EvTyping:
		if o.voiceActive {
			o.exitVoice()
		}
	}
}

func (o *Orchestrator) startRecording() {
	ctx, cancel := context.WithCancel(context.Background())
	o.recCancel = cancel
	o.setState(StateDictating)
	go func() {
		path, err := audio.RecordWav(ctx, "", 0)
		if err != nil && ctx.Err() == nil {
			log.Printf("record: %v", err)
		}
		select {
		case o.recDone <- path:
		default:
		}
	}()
}

func (o *Orchestrator) startLockedRecording() {
	// continuous record until LockOff; reuse same mechanism with longer cap
	ctx, cancel := context.WithCancel(context.Background())
	o.recCancel = cancel
	o.setState(StateDictationLocked)
	go func() {
		path, err := audio.RecordWav(ctx, "", 10*time.Minute)
		if err != nil && ctx.Err() == nil {
			log.Printf("record locked: %v", err)
		}
		select {
		case o.recDone <- path:
		default:
		}
	}()
}

func (o *Orchestrator) stopRecordingAndSend() {
	if o.recCancel != nil {
		o.recCancel()
	}
	select {
	case path := <-o.recDone:
		go o.transcribeAndInject(path)
	case <-time.After(3 * time.Second):
		log.Println("recording did not finish in time")
	}
}

func (o *Orchestrator) discardRecording() {
	if o.recCancel != nil {
		o.recCancel()
	}
	o.setState(StateIdle)
}

func (o *Orchestrator) transcribeAndInject(wav string) {
	if err := stt.EnsureServer(context.Background(), o.d.WhisperURL, o.d.WhisperBin, o.d.WhisperModel); err != nil {
		log.Printf("whisper server: %v", err)
		return
	}
	text, err := stt.Transcribe(context.Background(), o.d.WhisperURL, wav, o.d.AccentPrompt, "en")
	if err != nil {
		log.Printf("stt: %v", err)
		return
	}
	if strings.TrimSpace(text) == "" {
		log.Println("stt: empty transcript, skipping")
		return
	}
	ctx := context.Background()
	inject.NormalizeCaps(ctx, o.machine.CapsActive())
	inject.TypeText(ctx, text)
	inject.Key(ctx, "Return")
	o.setState(StateIdle)
}

func (o *Orchestrator) enterVoice() {
	ctx := context.Background()
	id, err := o.sw.EnterVoice(ctx)
	if err != nil {
		log.Printf("voice enter: %v", err)
		return
	}
	if id == "" {
		// no existing session: create one
		var err2 error
		id, err2 = o.sw.NewVoiceSession(ctx)
		if err2 != nil {
			log.Printf("voice new session: %v", err2)
			return
		}
	}
	o.voiceClient = o.sw.Client()
	o.voiceSID = id
	o.machine.SetVoiceActive(true)
	o.voiceActive = true
	o.setState(StateVoiceListening)
	go o.drainUpdates()
	log.Printf("voice mode ON session=%s", id)
}

func (o *Orchestrator) drainUpdates() {
	var pending strings.Builder
	for u := range o.voiceClient.Updates() {
		if u.SessionID != o.voiceSID || u.Kind != "agent_message_chunk" {
			continue
		}
		pending.WriteString(u.Text)
		// sentence-boundary flush
		if strings.ContainsAny(u.Text, ".!?") || len(pending.String()) > 400 {
			s := strings.TrimSpace(pending.String())
			pending.Reset()
			if s != "" {
				o.setState(StateVoiceSpeaking)
				for _, sent := range tts.SplitSentences(s) {
					o.speaker.Speak(context.Background(), sent)
				}
				o.setState(StateVoiceListening)
			}
		}
	}
	// flush remainder
	if s := strings.TrimSpace(pending.String()); s != "" {
		for _, sent := range tts.SplitSentences(s) {
			o.speaker.Speak(context.Background(), sent)
		}
	}
}

func (o *Orchestrator) handleUtterance(ctx context.Context, wav string) {
	if !o.voiceActive || o.voiceClient == nil {
		return
	}
	o.setState(StateVoiceThinking)
	if err := stt.EnsureServer(ctx, o.d.WhisperURL, o.d.WhisperBin, o.d.WhisperModel); err != nil {
		log.Printf("whisper server: %v", err)
		return
	}
	text, err := stt.Transcribe(ctx, o.d.WhisperURL, wav, o.d.AccentPrompt, "en")
	if err != nil {
		log.Printf("voice stt: %v", err)
		return
	}
	if strings.TrimSpace(text) == "" {
		log.Println("voice stt: empty, ignoring")
		o.setState(StateVoiceListening)
		return
	}
	log.Printf("voice heard: %s", text)
	stop, err := o.voiceClient.Prompt(ctx, o.voiceSID, text)
	if err != nil {
		log.Printf("acp prompt: %v", err)
	}
	log.Printf("voice turn done stopReason=%s", stop)
	if o.voiceActive {
		o.setState(StateVoiceListening)
	}
}

func (o *Orchestrator) exitVoice() {
	o.voiceActive = false
	o.machine.SetVoiceActive(false)
	if o.voiceCancel != nil {
		o.voiceCancel()
	}
	ctx := context.Background()
	if err := o.sw.ExitVoice(ctx); err != nil {
		log.Printf("voice exit: %v", err)
	}
	o.voiceClient = nil
	o.setState(StateIdle)
	log.Println("voice mode OFF")
}

func (o *Orchestrator) shutdownVoice(ctx context.Context) {
	if o.voiceActive {
		o.exitVoice()
	}
}

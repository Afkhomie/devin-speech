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
	AudioSource    string
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
	d       Deps
	machine *keys.Machine
	sw      *switcher.Switcher
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
	vadCancel   context.CancelFunc
	vadOut      chan string
}

func New(d Deps) *Orchestrator {
	return &Orchestrator{
		d:       d,
		machine: keys.NewMachine(d.Keys),
		sw:      switcher.New(d.ProjectDir, d.PermissionMode),
		speaker: tts.NewPiperSpeaker(d.PiperBin, d.PiperVoice, d.PiperPlayer),
		stateCh: make(chan State, 8),
		recDone: make(chan string, 1),
		vadOut:  make(chan string, 4),
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

	// VAD runs only while voice mode is active: it holds the ALSA capture
	// subdevice paired with the mic source, which would starve dictation.

	for {
		select {
		case <-ctx.Done():
			o.shutdownVoice(ctx)
			return nil
		case ev := <-events:
			o.handleKey(ev)
		case wav := <-o.vadOut:
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
		// async: EnterVoice/ExitVoice can block for seconds (TUI exit,
		// lock waits, ACP handshake) and must not stall the key loop
		if o.voiceActive {
			go o.exitVoice()
		} else {
			go o.enterVoice()
		}
	case keys.EvTyping:
		if o.voiceActive {
			go o.exitVoice()
		}
	}
}

func (o *Orchestrator) startRecording() {
	ctx, cancel := context.WithCancel(context.Background())
	o.recCancel = cancel
	o.setState(StateDictating)
	log.Printf("recording start (source=%q)", o.d.AudioSource)
	go func() {
		path, err := audio.RecordWav(ctx, o.d.AudioSource, 0)
		log.Printf("recording end path=%q err=%v", path, err)
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
		path, err := audio.RecordWav(ctx, o.d.AudioSource, 10*time.Minute)
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
	log.Printf("dictation commit")
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
	// drain the pending path so a discarded take can't be consumed by the
	// next commit (recDone is buffered and would otherwise go stale)
	select {
	case <-o.recDone:
	case <-time.After(500 * time.Millisecond):
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
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
	vadCtx, vadCancel := context.WithCancel(context.Background())
	o.vadCancel = vadCancel
	go func() {
		v := audio.DefaultVad()
		if err := v.Listen(vadCtx, o.d.AudioSource, o.vadOut); err != nil && vadCtx.Err() == nil {
			log.Printf("vad: %v", err)
		}
	}()
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
	if o.vadCancel != nil {
		o.vadCancel()
		o.vadCancel = nil
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

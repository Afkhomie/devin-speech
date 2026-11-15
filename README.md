# devin-speech

Local, zero-cost voice interface for [Devin CLI](https://devin.ai). Two modes, one daemon:

1. **Dictation** - hold or double-tap a configurable key (default: CapsLock), speak, and the transcription is typed into whatever window is focused and submitted. Works in the Devin TUI, browsers, anywhere.
2. **Voice mode** - tap the key once for a hands-free conversation with your current Devin session. Speech is transcribed, sent to the session over ACP, and Devin's text replies are spoken back through TTS.

Everything runs locally. No paid APIs, no cloud STT/TTS.

## status

Early development. Design approved, spikes in progress.

## planned architecture

| piece | choice |
|---|---|
| daemon | single Go binary |
| key capture | evdev (hold / tap / double-tap state machine, typing auto-exits voice mode) |
| STT | whisper.cpp `whisper-server`, CPU-only |
| TTS | piper -> default audio sink |
| Devin bridge | `devin acp` over stdio JSON-RPC (Agent Client Protocol), `session/load` to attach to an existing session |
| text injection | xdotool (X11) |
| indicator | system tray icon with listening / thinking / speaking states |
| audio input | any standard audio source (e.g. phone-as-mic tools) via the default PulseAudio/PipeWire source |
| lifecycle | systemd user service, lazy-started heavy components |

## session model

Voice mode attaches to the most recent Devin session in the working directory (same semantics as `devin -c`), so voice continues the same conversation with full memory. Cross-project requests work through normal filesystem access.

## requirements (planned)

- Linux, X11 (Wayland not targeted yet)
- Devin CLI installed and authenticated
- whisper.cpp, piper binaries
- user in `input` group for evdev key capture

## license

TBD

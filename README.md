# devin-speech

Local, zero-cost voice interface for [Devin CLI](https://devin.ai). Two modes, one daemon, plus automated switching between the terminal UI and voice.

1. **Dictation** - hold CapsLock to dictate (release to send), or double-tap CapsLock for hands-free locked dictation (tap once to send). Text is typed into whatever window is focused.
2. **Voice mode** - double-tap Spacebar toggles a hands-free conversation with your Devin session. If a Devin TUI is open, it is exited automatically and relaunched on voice exit, so voice continues the exact same session with full memory. Typing any key also exits voice mode.

Everything runs locally: whisper.cpp for STT, piper for TTS, `devin acp` (Agent Client Protocol) as the agent bridge. No paid APIs, no cloud.

## status

v1 implemented: key state machine (evdev), VAD endpointing, whisper STT, piper TTS, ACP client with permission auto-approve, session discovery + TUI/voice switcher, system tray indicator, systemd user service. X11 only.

## requirements

- Linux X11, Devin CLI authenticated, user in `input` group
- whisper.cpp `whisper-server` binary + ggml model
- piper binary + voice model
- xdotool, pw-record/parec, paplay/pw-play
- Go 1.24+

See `docs/install.md` for setup and `docs/implementation-plan.md` for the design plan.

## license

TBD

# install

## requirements

- Linux X11 (Wayland not supported yet)
- Devin CLI installed + `devin auth login`
- user in `input` group: `sudo usermod -aG input $USER` (re-login)
- tools: `whisper.cpp` built from source (`whisper-server` binary),
  `piper` binary + a voice `.onnx`, `xdotool`, `pw-record`/`parec`,
  `paplay`/`pw-play`, `ffmpeg` optional
- Go 1.24+ to build

## build + install

```sh
go build -o devin-speech ./cmd/devin-speech
install -Dm755 devin-speech ~/.local/bin/devin-speech
mkdir -p ~/.config/devin-speech
cp config.example.toml ~/.config/devin-speech/config.toml
# edit paths in config.toml to match your tools
```

## service

```sh
mkdir -p ~/.config/systemd/user
cp devin-speech.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now devin-speech
# optional: start at boot without login
loginctl enable-linger $USER
```

## usage

- **hold CapsLock**: dictate, release to send (types + Enter into focused window)
- **double-tap CapsLock**: lock dictation hands-free; tap once to send
- **double-tap Spacebar**: toggle voice mode. Voice attaches to your current
  devin session: if a TUI is open it exits it first, and on voice exit it
  relaunches `devin -c`. typing any key also exits voice mode.
- tray icon shows state: idle / dictating / listening / thinking / speaking

## notes

- voice session runs with bypass permissions (auto-approves tool calls)
- a devin session can only be open in ONE place: TUI or voice, never both
- STT model default is whisper tiny.en (fast on CPU); accuracy on heavy
  accents may need a bigger model (see docs/benchmark.md)

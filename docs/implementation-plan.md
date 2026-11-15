# devin-speech Implementation Plan

> **For agentic workers:** implement task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A Go daemon that gives Devin CLI local push-to-talk dictation and hands-free voice conversation with automatic TUI<->voice switching.

**Architecture:** Single Go binary `devin-speech` orchestrating external binaries: whisper.cpp `whisper-server` (STT), piper (TTS), `devin acp` (agent bridge via ACP JSON-RPC), xdotool (X11 text injection + TUI automation), evdev (key capture). systemd user service keeps it alive; heavy components lazy-start.

**Tech Stack:** Go 1.24+, grafov/evdev, coder/acp-go-sdk (or hand-rolled JSON-RPC if SDK lacks session/load), fyne-io/systray or remko/go-sni, os/exec for external tools, BurntSushi/toml for config.

**Spec:** `DESIGN.local.md` (private, contains user-specific details) - public summary in `README.md` and `docs/architecture.md`.

## Global Constraints

- No Python anywhere in the pipeline.
- CPU-only inference; smallest viable whisper model that passes the accent benchmark.
- All audio via default PulseAudio/PipeWire source and sink; no custom routing.
- X11 only. Keys: CapsLock = dictation (hold/double-tap-lock/single-tap-stop), Spacebar double-tap = voice mode toggle.
- Voice speaks only `agent_message_chunk` text; never `agent_thought_chunk` or tool calls.
- Session locks are PID-based and exclusive: a session open in a live TUI cannot be ACP-attached (`session_locked`, retryable). Auto-switch handshake handles this.
- Bypass permissions on the voice session (user decision).
- Repo is PUBLIC: no personal info, real names, machine names, or local paths in committed files.

## Review Focus

Inputs/conditions the spec implies that need explicit tests:

1. CapsLock held through autorepeat - evdev sends value=2 repeats; state machine must ignore them or dictation never ends. Test: synthetic repeat events.
2. Double-tap spacebar types two spaces before detection - erasure via Backspace x2 must fire on BOTH start and stop, and not fire on single taps. Test: event sequence -> exactly two backspaces injected, only on double-tap.
3. `session/load` returns `session_locked` (-32015) when TUI holds session - switcher must poll/retry, not fail. Test: mock error then success.
4. sessions.db WAL `DatabaseBusy` - transient; session discovery/load must retry with backoff. Test: inject busy error.
5. Whisper returns empty/garbage on silence or noise - daemon must not send empty prompts or inject blank text. Test: empty transcription -> no-op with indicator reset.
6. ACP `agent_message_chunk` may arrive as many small chunks - TTS must sentence-buffer, not speak per-chunk. Test: chunk splitter on punctuation.
7. Voice mode ON while TUI is mid-turn (agent working) - `/exit` behavior; acceptable = exits anyway, note in docs.
8. Daemon restart while voice mode was attached - on boot, no stale lock held by dead PID should block `devin -c` relaunch logic; verify stale-lock handling.

---

### Task 1: Repo scaffold + config

**Files:**
- Create: `go.mod`, `cmd/devin-speech/main.go`, `internal/config/config.go`, `config.example.toml`, `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Load(path string) (*Config, error)`; `Config` struct with fields: `Keys.CapsLock`, `Keys.Spacebar` (evdev key names or codes), `WhisperURL`, `WhisperModelPath`, `PiperPath`, `PiperVoice`, `ProjectDir`, `PermissionMode`, `TrayEnabled`.

- [ ] **Step 1:** `go mod init github.com/Afkhomie/devin-speech` (public repo path; no personal info).
- [ ] **Step 2:** Write failing test: `config.Load` on missing file returns defaults + nil error (create `~/.config/devin-speech/` on demand); on malformed TOML returns error.
- [ ] **Step 3:** Implement `internal/config` with BurntSushi/toml; defaults: whisper `http://127.0.0.1:8080`, piper `~/tools/piper/piper` -> make configurable `~/.local/share/devin-speech/piper/piper`, voice path configurable, project dir = cwd.
- [ ] **Step 4:** `config.example.toml` committed with all fields commented.
- [ ] **Step 5:** `main.go` skeleton: load config, log "devin-speech started", idle loop. `go build ./...` passes. Commit.

### Task 2: Key state machine (`internal/keys`)

**Files:**
- Create: `internal/keys/machine.go`, `internal/keys/machine_test.go`, `internal/keys/evdev.go`

**Interfaces:**
- Produces: `type Event int` (`EvDictationStart`, `EvDictationStop`, `EvDictationLockToggle`, `EvVoiceToggle`, `EvTyping`); `func NewMachine(cfg MachineConfig) *Machine`; `func (m *Machine) Feed(evdevKey int, value int, ts time.Duration) []Event`; `func (m *Machine) CapsActive() bool`. `evdev.go`: `func Listen(ctx context.Context, m *Machine) error` - opens all `/dev/input/event*`, filters keyboards via `HasKey(KEY_CAPSLOCK)`, feeds events, ignores autorepeat (value==2).

- [ ] **Step 1:** Failing tests for the pure state machine (no evdev needed):
  - capslock down 300ms, up -> `[EvDictationStart]` then on... actually hold semantics: down->(held) emits `EvDictationStart` after hold threshold OR on release if shorter? DECISION: hold = press-and-release emits start+stop pair only if held > threshold; tap = ignored (reserved); double-tap within 300ms = `EvDictationLockToggle`; while locked, single tap = `EvDictationStop`+inject. Spacebar: two presses within 300ms = `EvVoiceToggle`; single presses produce nothing; any non-modifier key value==1 while voiceActive = `EvTyping` (caller clears voiceActive via returned event handling - machine tracks `voiceActive` internally, set by `SetVoiceActive(bool)`).
- [ ] **Step 2:** Implement `machine.go` pure logic with injected timestamps (testable, no real time).
- [ ] **Step 3:** Implement `evdev.go` using `github.com/grafov/evdev`; iterate devices, `HasKey(evdev.KEY_CAPSLOCK)`, read `InputEvent`, call `m.Feed`. Ignore `value==2`.
- [ ] **Step 4:** Manual verify: `go run ./cmd/devin-speech` with a debug log mode (`--debug-keys` flag prints events) - press capslock hold/tap/double, spacebar double; confirm events. Requires `input` group membership - document `sudo usermod -aG input $USER` in README.
- [ ] **Step 5:** Commit.

### Task 3: Audio record (`internal/audio`)

**Files:**
- Create: `internal/audio/record.go`, `internal/audio/record_test.go`

**Interfaces:**
- Produces: `func RecordWav(ctx context.Context, source string, maxDur time.Duration) (path string, err error)` - runs `pw-record --rate 16000 --channels 1 <tmp>.wav` (fallback `parec`), kills on ctx cancel, returns wav path. `func EnsureSource(ctx) error` - verifies a default source exists via `pactl info`.

- [ ] **Step 1:** Failing test: `RecordWav` with 500ms ctx produces a .wav file > 1KB with RIFF header (skip if no audio source - `t.Skip`).
- [ ] **Step 2:** Implement via os/exec; tmpdir under `os.TempDir()/devin-speech/`; cleanup old files >1h.
- [ ] **Step 3:** Verify manually with `wpctl status` / `pactl list sources short` - record 3s, play with ffplay, confirm audible.
- [ ] **Step 4:** Commit.

### Task 4: STT client (`internal/stt`)

**Files:**
- Create: `internal/stt/whisper.go`, `internal/stt/whisper_test.go`, `internal/stt/server.go`

**Interfaces:**
- Produces: `func EnsureServer(ctx context.Context, bin, modelPath string) error` - starts `whisper-server -m <model> --host 127.0.0.1 --port 8080` if `GET /` doesn't respond; waits for readiness. `func Transcribe(ctx context.Context, wavPath string, prompt string) (string, error)` - POST `/inference` multipart `file` + `response_format=json` + `prompt` (accent hint) + `language=en`; parses `{"text": ...}`.

- [ ] **Step 1:** Failing test with `httptest.Server` mocking `/inference` -> returns canned JSON; assert parsed text.
- [ ] **Step 2:** Implement client + server lifecycle (lazy start on first Transcribe, keep alive, idle timeout optional v2).
- [ ] **Step 3:** REAL verify: transcribe a recorded wav through the actual whisper-server; benchmark base.en-q5_1 vs tiny.en-q8_0 on a Master_Afk voice sample for latency+accuracy; pick model, set config default. Record result in docs/benchmark.md.
- [ ] **Step 4:** Commit.

### Task 5: TTS (`internal/tts`)

**Files:**
- Create: `internal/tts/piper.go`, `internal/tts/piper_test.go`, `internal/tts/splitter.go`, `internal/tts/splitter_test.go`

**Interfaces:**
- Produces: `func SplitSentences(text string) []string` - splits on `. ! ? \n` keeping punctuation, merges tiny fragments, caps ~300 chars. `type Speaker interface { Speak(ctx, text string) error }`; `func NewPiperSpeaker(bin, voice, player string) Speaker` - per sentence: `piper --model voice --output-file tmp.wav` (stdin text) then `paplay tmp.wav` (or `pw-play`); player configurable.
- v2 option noted: keep piper `--server`/`--json-input` process hot if flag exists in binary (check `piper --help`).

- [ ] **Step 1:** Failing tests for `SplitSentences`: "Hi. How are you? Fine!" -> 3 sentences; markdown/code fences stripped or truncated politely; empty -> [].
- [ ] **Step 2:** Implement splitter (strip markdown: code blocks, links->[text], headers->text, bullet markers; skip if result empty).
- [ ] **Step 3:** Implement speaker; verify: `echo test | piper` -> wav -> `paplay` audible.
- [ ] **Step 4:** Commit.

### Task 6: Text injection (`internal/inject`)

**Files:**
- Create: `internal/inject/inject.go`, `internal/inject/inject_test.go`

**Interfaces:**
- Produces: `func TypeText(ctx, text string) error` - `xdotool type --delay 12 -- <text>`; `func Key(ctx, keys ...string) error` - `xdotool key <keys>`; `func EraseSpaces(ctx, n int) error` - n x BackSpace; `func NormalizeCaps(ctx, capsOn bool) error` - if capsOn, `xdotool key Caps_Lock`.
- Unicode fallback noted: if xdotool produces wrong chars for non-ASCII, v2 = clipboard+paste. Dictation text mostly ASCII.

- [ ] **Step 1:** Failing test: mock exec (PATH shim dir with fake `xdotool` script logging args) - assert correct argv for TypeText/Key/EraseSpaces/NormalizeCaps.
- [ ] **Step 2:** Implement wrappers.
- [ ] **Step 3:** Manual verify: focus a text field, run TypeText("hello world"), confirm typed; test in devin TUI prompt.
- [ ] **Step 4:** Commit.

### Task 7: ACP client (`internal/acp`)

**Files:**
- Create: `internal/acp/client.go`, `internal/acp/client_test.go`

**Interfaces:**
- Produces: `type Client struct`; `func New(ctx, projectDir, permissionMode string) (*Client, error)` - spawns `devin acp` with `DEVIN_PERMISSION_MODE=bypass` env (verify vs session/set_mode in step 4); `func (c *Client) Initialize(ctx) error`; `func (c *Client) LoadSession(ctx, sessionID, cwd string) error`; `func (c *Client) NewSession(ctx) (string, error)`; `func (c *Client) Prompt(ctx, sessionID, text string, onChunk func(string)) (stopReason string, err error)` - streams `agent_message_chunk` only; `func (c *Client) Close() error`.
- Decision point: try `github.com/coder/acp-go-sdk` first; if it lacks session/load or set_mode, hand-roll minimal JSON-RPC (spike code in `spikes/acp/main.go` is the reference - already proven working).

- [ ] **Step 1:** Failing test: fake `devin` shell script on PATH emitting canned ndjson (initialize result, session/new result, prompt chunks) - assert client parses sessionId, chunks, stopReason; ignore agent_thought_chunk/tool_call.
- [ ] **Step 2:** Implement client (SDK or hand-rolled per decision).
- [ ] **Step 3:** REAL verify against `devin acp`: NewSession + Prompt("reply with one word: ok") -> chunk "ok" received; LoadSession on a closed session -> history replay filtered to nothing spoken (chunks only flow for NEW prompt); verify bypass actually auto-approves (prompt that triggers a tool call, e.g. "run echo hi", confirm no permission request blocks - or switch to session/set_mode bypass if env fails).
- [ ] **Step 4:** Commit.

### Task 8: Session discovery + switcher (`internal/devin` + `internal/switcher`)

**Files:**
- Create: `internal/devin/sessions.go`, `internal/devin/sessions_test.go`, `internal/switcher/switcher.go`, `internal/switcher/switcher_test.go`

**Interfaces:**
- Produces: `func MostRecentSession(ctx, dir string) (id, cwd string, err error)` - runs `devin list --format json`, filters `working_directory == dir`, sorts by `last_activity_at` desc, skips ids whose lock file has a LIVE pid (check `/proc/<pid>`); `func LockedSessions() map[string]LockedInfo` - scans `~/.local/share/devin/cli/session_locks/*.lock`, returns id->pid for live pids. `type Switcher`; `func (s *Switcher) EnterVoice(ctx, inject, acpFactory) (sessionID string, err error)` - if focused-window-is-TUI (heuristic: exactly one live-locked devin session OR active window class match - simplest v1: if any locked session exists, treat as TUI-open): record id+cwd, `inject.TypeText("/exit")`+`Key("Return")`, poll lock-free up to 10s, then `acp.LoadSession(id, cwd)`; `func (s *Switcher) ExitVoice(ctx) error` - close acp child, `inject.TypeText("devin -c")`+`Key("Return")` in same terminal.
- Note: focused-window detection v1 = "a live-locked devin session exists". Refinement (active window check via xdotool getactivewindow getwindowclassname) is a config option later.

- [ ] **Step 1:** Failing tests: `devin list` JSON fixture -> correct most-recent pick; lock-file pid alive/dead detection (mock /proc via injectable stat func); switcher with fake inject+acp -> correct call sequence on enter/exit.
- [ ] **Step 2:** Implement sessions.go + switcher.go.
- [ ] **Step 3:** REAL verify: open `devin` TUI in a scratch dir on a test session, run switcher.EnterVoice -> TUI exits, session loads in acp; ExitVoice -> `devin -c` reopens with history. THIS IS THE CRITICAL PATH - verify manually before proceeding.
- [ ] **Step 4:** Commit.

### Task 9: Voice loop orchestrator (`internal/daemon` or main wiring)

**Files:**
- Create: `internal/orchestrator/orchestrator.go`, `internal/orchestrator/orchestrator_test.go`; modify `cmd/devin-speech/main.go`

**Interfaces:**
- Produces: `type Orchestrator`; states `idle | dictating-hold | dictating-locked | voice | voice-thinking | voice-speaking`; `func (o *Orchestrator) Run(ctx) error` - wires keys.Machine events -> handlers: `EvDictationStart/Stop` -> audio.RecordWav -> stt.Transcribe -> inject (with caps normalize + trailing Return); `EvDictationLockToggle` -> toggle dictation-locked (record on stop); `EvVoiceToggle` -> switcher.EnterVoice/ExitVoice + voice loop; `EvTyping` while voice -> ExitVoice. Voice loop: record fixed window or VAD -> transcribe -> acp.Prompt(onChunk -> tts sentence queue) -> repeat until EvVoiceToggle/EvTyping.
- Endpointing decision (from spike): simplest v1 = record while voice mode active with silence detection via... v1 pragmatic: record N seconds after last speech? DECISION for build: use fixed max 15s window + simple energy-based endpointing in Go on the wav? Simplest robust v1: **tap spacebar once to start/stop each utterance** is NOT what user wants (hands-free). Alternative: record continuously in voice mode, run whisper on trailing window every M seconds? Heavy. PRAGMATIC V1: silence detection on PCM via `parec` raw stream + RMS threshold in Go - implement `internal/audio/vad.go` simple energy VAD (frame RMS > threshold for >300ms = speech; 800ms silence = endpoint). Keep whisper out of the loop.

- [ ] **Step 1:** Failing tests: orchestrator state transitions with fake keys/audio/stt/acp/tts (all interfaces mocked) - dictation hold produces inject call; voice toggle enters+exits; typing exits voice.
- [ ] **Step 2:** Implement VAD (`internal/audio/vad.go`): `parec --format=s16le --rate=16000 --channels=1` raw stream -> RMS per 20ms frame -> speech/silence FSM -> emits utterance wav on endpoint. Unit test with synthetic PCM (silence/square wave).
- [ ] **Step 3:** Wire orchestrator; manual e2e: dictation into a text field; voice mode conversation with a real devin session (short prompts only - "what is 2+2" style) including TUI auto-switch.
- [ ] **Step 4:** Commit.

### Task 10: Tray indicator (`internal/tray`)

**Files:**
- Create: `internal/tray/tray.go`; modify orchestrator to report state.

**Interfaces:**
- Produces: `func Run(ctx, stateCh <-chan State) error` - systray icon + tooltip per state (idle/listening/thinking/speaking/error). Library: try `github.com/remko/go-sni` (pure Go) first; fallback `fyne-io/systray` (cgo, needs gtk3-devel - check `dnf list gtk3-devel`; avoid if it needs sudo install - user may not want system packages; go-sni preferred).

- [ ] **Step 1:** Spike: minimal go-sni tray icon on Plasma X11 - appears in tray, tooltip changes. If go-sni too painful (>1hr), fallback: `yad --notification` or skip tray v1 with OSD via `notify-send` on state change (less pretty, zero deps).
- [ ] **Step 2:** Wire states; verify all 4 states visually.
- [ ] **Step 3:** Commit.

### Task 11: Service + polish + docs

**Files:**
- Create: `devin-speech.service` (systemd user unit), `docs/install.md`, `docs/usage.md`; update `README.md`.

- [ ] **Step 1:** systemd unit: `graphical-session.target`, `Restart=on-failure`, env `DISPLAY`, `DBUS_SESSION_BUS_ADDRESS` (usually inherited). Install to `~/.config/systemd/user/`; `systemctl --user enable --now`; `loginctl enable-linger` note in docs.
- [ ] **Step 2:** Full e2e checklist run: dictation hold/double-tap/stop; voice on/off with TUI switch; indicator states; daemon restart recovery.
- [ ] **Step 3:** README: features, requirements (input group, tools: whisper-server, piper, xdotool, pw-record, paplay, devin CLI), install, usage, key bindings, known limits (X11 only, session exclusivity, bypass risk note).
- [ ] **Step 4:** Push to public repo. NO personal info in any committed file.
- [ ] **Step 5:** Final commit.

## Execution order notes

- Tasks 2-6 are independent leaf packages - parallelizable by subagents once Task 1 interfaces exist.
- Task 7 (ACP) is the critical-path risk - proven by spike already.
- Task 8 switcher is THE critical path for the user's core requirement - do it early, verify manually before orchestrator.
- Task 9 orchestrator depends on all of 2-8.
- Task 10 tray is cosmetic - last before polish.

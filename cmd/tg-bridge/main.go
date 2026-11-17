// tg-bridge: Telegram <-> Devin CLI bridge.
//
// Long-polls the Telegram Bot API and forwards messages to a dedicated devin
// session; Devin's reply text is sent back to the same chat.
//
// Session model: the bot owns its OWN sessions, separate from TUI sessions.
// Each message spawns a short-lived `devin acp`, loads the bot session,
// prompts, then exits — the lock is free between messages.
//
//   - default: one persistent bot session in ~/tg-devin
//   - /project <dir>: switch to (or create) the bot's session for that dir —
//     one bot session per project, remembered across restarts
//   - /new or /reset: fresh bot session in the current project dir
//   - /sessions + /use <n|id>: escape hatch to talk on ANY session (e.g. a
//     TUI one) — resets on next /project or /new
//
// Note: `devin -c` in a project dir resumes the most recent session there,
// which may be the bot's. Use `devin -r` (picker) or `devin -r <id>` for the
// TUI session.
//
// Setup: message @BotFather -> /newbot -> copy the token, then run:
//   tg-bridge -token <token>
// First /start locks in your chat id (saved next to the state file).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Afkhomie/devin-speech/internal/acp"
	"github.com/Afkhomie/devin-speech/internal/devin"
)

var (
	token   = flag.String("token", os.Getenv("TELEGRAM_BOT_TOKEN"), "bot token from @BotFather (or TELEGRAM_BOT_TOKEN env)")
	workDir = flag.String("dir", "", "working dir for the default bot session (default ~/tg-devin)")
	state   = flag.String("state", "", "state file path (default ~/.local/share/tg-bridge/state.json)")
)

type tgMessage struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		From      *struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
		Date int64  `json:"date"`
	} `json:"message"`
}

type tgResponse struct {
	OK     bool        `json:"ok"`
	Result []tgMessage `json:"result"`
	Error  string      `json:"description"`
}

func main() {
	flag.Parse()
	home, _ := os.UserHomeDir()
	if *workDir == "" {
		*workDir = filepath.Join(home, "tg-devin")
	}
	if *state == "" {
		*state = filepath.Join(home, ".local", "share", "tg-bridge", "state.json")
	}
	if *token == "" {
		log.Fatal("no bot token: pass -token or set TELEGRAM_BOT_TOKEN")
	}
	for _, d := range []string{*workDir, filepath.Dir(*state)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			log.Fatalf("mkdir %s: %v", d, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot := &bot{
		token:  *token,
		state:  loadState(*state),
		devin:  &devinSession{},
		dir:    *workDir,
		client: &http.Client{Timeout: 40 * time.Second},
	}
	log.Printf("tg-bridge ready: dir=%s session=%s owner=%d", *workDir, bot.state.SessionID, bot.state.Owner)
	bot.poll(ctx)
	log.Println("shutting down")
}

// ---- devin side ----

// devinSession serializes turns. Each turn spawns a short-lived `devin acp`
// and kills it afterwards, so the session lock is free between messages.
type devinSession struct {
	mu sync.Mutex
}

var errSessionBusy = errors.New("devin is still working on your previous message")

// create makes a fresh session in dir and returns its id.
func (d *devinSession) create(ctx context.Context, dir string) (string, error) {
	c, err := acp.New(ctx, dir, "bypass")
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := c.Initialize(ctx); err != nil {
		return "", err
	}
	return c.NewSession(ctx, dir)
}

// run executes one prompt against sid ("" = new session in cwd) and returns
// the reply text plus the session id that was used.
func (d *devinSession) run(ctx context.Context, sid, cwd, text string) (string, string, error) {
	if !d.mu.TryLock() {
		return "", sid, errSessionBusy
	}
	defer d.mu.Unlock()

	c, err := acp.New(ctx, cwd, "bypass")
	if err != nil {
		return "", sid, err
	}
	defer c.Close()
	if err := c.Initialize(ctx); err != nil {
		return "", sid, err
	}
	if sid == "" {
		if sid, err = c.NewSession(ctx, cwd); err != nil {
			return "", sid, err
		}
	} else if err := c.LoadSession(ctx, sid, cwd); err != nil {
		return "", sid, err
	}

	// session/load replays history as updates; drop whatever is buffered,
	// then only collect chunks that arrive once the prompt starts.
	for {
		select {
		case _, ok := <-c.Updates():
			if !ok {
				return "", sid, fmt.Errorf("acp exited during load")
			}
		default:
			goto drained
		}
	}
drained:

	var collecting atomic.Bool
	var sb strings.Builder
	var sbMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for u := range c.Updates() {
			if !collecting.Load() || u.SessionID != sid || u.Kind != "agent_message_chunk" {
				continue
			}
			sbMu.Lock()
			sb.WriteString(u.Text)
			sbMu.Unlock()
		}
	}()

	collecting.Store(true)
	if _, err := c.Prompt(ctx, sid, text); err != nil {
		c.Close()
		wg.Wait()
		return "", sid, fmt.Errorf("devin prompt: %w", err)
	}
	c.Close()
	wg.Wait()

	sbMu.Lock()
	out := strings.TrimSpace(sb.String())
	sbMu.Unlock()
	return out, sid, nil
}

// ---- telegram side ----

type bridgeState struct {
	Owner      int64             `json:"owner"`       // telegram user id allowed to talk
	Offset     int               `json:"offset"`
	SessionID  string            `json:"session_id"`  // active bot session
	SessionDir string            `json:"session_dir"` // its working directory
	Projects   map[string]string `json:"projects"`    // dir -> bot session id, one per project
}

func loadState(path string) *bridgeState {
	b, err := os.ReadFile(path)
	if err != nil {
		return &bridgeState{}
	}
	var s bridgeState
	if json.Unmarshal(b, &s) != nil {
		return &bridgeState{}
	}
	if s.Projects == nil {
		s.Projects = map[string]string{}
	}
	return &s
}

func (s *bridgeState) save(path string) {
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o600)
}

// setSession points the bot at a session, remembering dir->sid for /project.
func (s *bridgeState) setSession(sid, dir string) {
	s.SessionID = sid
	s.SessionDir = dir
	if dir != "" {
		s.Projects[dir] = sid
	}
}

type bot struct {
	token  string
	state  *bridgeState
	devin  *devinSession
	dir    string
	client *http.Client
}

func (b *bot) api(ctx context.Context, method string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		buf, _ := json.Marshal(payload)
		body = bytes.NewReader(buf)
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.token, method)
	req, err := http.NewRequestWithContext(ctx, "POST", u, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (b *bot) poll(ctx context.Context) {
	statePath := *state
	for {
		var res tgResponse
		// long-poll via getUpdates with timeout=25 (client timeout is 40s)
		u := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=25",
			b.token, b.state.Offset)
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			log.Printf("tg poll: %v", err)
			return
		}
		resp, err := b.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("tg poll: %v (retrying)", err)
			time.Sleep(3 * time.Second)
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			log.Printf("tg decode: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		if !res.OK {
			log.Printf("tg api: %s", res.Error)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, upd := range res.Result {
			b.state.Offset = upd.UpdateID + 1
			go b.handle(ctx, upd)
		}
		b.state.save(statePath)
	}
}

// sessionDir resolves the working dir for the active session id, used to
// repair state entries that predate SessionDir.
func (b *bot) sessionDir(ctx context.Context, sid string) string {
	if b.state.SessionDir != "" {
		return b.state.SessionDir
	}
	sessions, err := devin.RecentSessions(ctx)
	if err != nil {
		return b.dir
	}
	for _, s := range sessions {
		if s.ID == sid {
			return s.WorkingDir
		}
	}
	return b.dir
}

func (b *bot) handle(ctx context.Context, upd tgMessage) {
	m := upd.Message
	if m == nil || m.From == nil {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	uid := m.From.ID
	chatID := m.Chat.ID

	if text == "/start" || text == "/start@"+b.username(ctx) {
		if b.state.Owner == 0 {
			b.state.Owner = uid
			b.state.save(*state)
			b.send(ctx, chatID, "locked in. i'm your telegram devin — i keep my own sessions, separate from the TUI. /project <dir> picks a project, /new resets, /sessions lists everything.")
			log.Printf("tg: owner registered uid=%d", uid)
		} else if uid != b.state.Owner {
			b.send(ctx, chatID, "this bot is private.")
		} else {
			b.send(ctx, chatID, fmt.Sprintf("still here. project: %s", b.state.SessionDir))
		}
		return
	}
	if b.state.Owner == 0 || uid != b.state.Owner {
		log.Printf("tg: ignoring uid=%d (owner=%d)", uid, b.state.Owner)
		return
	}

	switch {
	case strings.HasPrefix(text, "/project"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/project"))
		if arg == "" {
			b.send(ctx, chatID, "usage: /project <dir> — e.g. /project ~/Projects/devin-speech")
			return
		}
		if strings.HasPrefix(arg, "~") {
			arg = filepath.Join(os.Getenv("HOME"), arg[1:])
		}
		arg = filepath.Clean(arg)
		if st, err := os.Stat(arg); err != nil || !st.IsDir() {
			b.send(ctx, chatID, "not a directory: "+arg)
			return
		}
		sid := b.state.Projects[arg]
		if sid == "" {
			b.send(ctx, chatID, "creating bot session in "+arg+"…")
			var err error
			sid, err = b.devin.create(ctx, arg)
			if err != nil {
				b.send(ctx, chatID, "create failed: "+err.Error())
				return
			}
		}
		b.state.setSession(sid, arg)
		b.state.save(*state)
		b.send(ctx, chatID, "bot session ready in "+arg+" — your TUI sessions there are untouched.")
		return

	case text == "/new" || text == "/reset":
		dir := b.state.SessionDir
		if dir == "" {
			dir = b.dir
		}
		b.send(ctx, chatID, "creating fresh session in "+dir+"…")
		sid, err := b.devin.create(ctx, dir)
		if err != nil {
			b.send(ctx, chatID, "create failed: "+err.Error())
			return
		}
		b.state.setSession(sid, dir)
		b.state.save(*state)
		b.send(ctx, chatID, "fresh session ready.")
		return

	case strings.HasPrefix(text, "/sessions"):
		sessions, err := devin.RecentSessions(ctx)
		if err != nil {
			b.send(ctx, chatID, "list failed: "+err.Error())
			return
		}
		locked, _ := devin.LockedSessions()
		var sb strings.Builder
		sb.WriteString("recent sessions — /use <n> or /use <id> to talk on one directly:\n")
		for i, s := range sessions {
			if i >= 8 {
				break
			}
			mark := ""
			if s.ID == b.state.SessionID {
				mark = " 🤖"
			}
			if _, busy := locked[s.ID]; busy {
				mark += " (open in TUI)"
			}
			fmt.Fprintf(&sb, "%d. %s — %s%s\n", i+1, s.ID, s.WorkingDir, mark)
		}
		b.send(ctx, chatID, strings.TrimSpace(sb.String()))
		return

	case strings.HasPrefix(text, "/use"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/use"))
		if arg == "" {
			b.send(ctx, chatID, "usage: /use <n> or /use <session-id>")
			return
		}
		sessions, err := devin.RecentSessions(ctx)
		if err != nil {
			b.send(ctx, chatID, "list failed: "+err.Error())
			return
		}
		var target devin.SessionInfo
		if n, err := strconv.Atoi(arg); err == nil {
			if n < 1 || n > len(sessions) {
				b.send(ctx, chatID, "no session #"+arg)
				return
			}
			target = sessions[n-1]
		} else {
			for _, s := range sessions {
				if strings.HasPrefix(s.ID, arg) {
					target = s
					break
				}
			}
			if target.ID == "" {
				b.send(ctx, chatID, "no session matching "+arg)
				return
			}
		}
		if locked, _ := devin.LockedSessions(); locked != nil {
			if _, busy := locked[target.ID]; busy {
				b.send(ctx, chatID, target.ID+" is open in the TUI right now — close it there first.")
				return
			}
		}
		b.state.setSession(target.ID, target.WorkingDir)
		b.state.save(*state)
		b.send(ctx, chatID, "talking on "+target.ID+" — /project or /new to go back to a bot session.")
		return
	}

	log.Printf("tg: msg from %d: %s", uid, truncate(text, 60))
	b.sendChatAction(ctx, chatID, "typing")
	start := time.Now()

	sid := b.state.SessionID
	cwd := b.sessionDir(ctx, sid)
	if sid == "" {
		cwd = b.dir
	}
	if sid != "" {
		if locked, _ := devin.LockedSessions(); locked != nil {
			if _, busy := locked[sid]; busy {
				b.send(ctx, chatID, "that session is open in the devin TUI — close it there or pick another with /sessions.")
				return
			}
		}
	}

	tctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	reply, usedSID, err := b.devin.run(tctx, sid, cwd, text)
	cancel()
	if err != nil && sid != "" && strings.Contains(err.Error(), "session/load") {
		// stored session is gone — clear it and retry once as a fresh session
		log.Printf("tg: stale session %s, recreating", sid)
		b.state.SessionID = ""
		b.state.SessionDir = ""
		tctx2, cancel2 := context.WithTimeout(ctx, 10*time.Minute)
		reply, usedSID, err = b.devin.run(tctx2, "", cwd, text)
		cancel2()
	}
	if err != nil {
		reply = "devin error: " + err.Error()
	} else if reply == "" {
		reply = "(devin returned empty response)"
	}
	if usedSID != "" && usedSID != sid {
		b.state.setSession(usedSID, cwd)
		b.state.save(*state)
	}
	log.Printf("tg %d -> devin %s (%s): %s", uid, usedSID, time.Since(start).Round(time.Millisecond), truncate(reply, 80))
	for _, chunk := range splitMsg(reply, 4000) {
		b.send(ctx, chatID, chunk)
	}
}

func (b *bot) username(ctx context.Context) string {
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := b.api(ctx, "getMe", nil, &res); err == nil {
		return res.Result.Username
	}
	return "bot"
}

func (b *bot) send(ctx context.Context, chatID int64, text string) {
	var res tgResponse
	err := b.api(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}, &res)
	if err != nil || !res.OK {
		log.Printf("tg send: %v %s", err, res.Error)
	}
}

func (b *bot) sendChatAction(ctx context.Context, chatID int64, action string) {
	b.api(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID, "action": action,
	}, nil)
}

func splitMsg(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		cut := strings.LastIndex(s[:n], "\n")
		if cut < n/2 {
			cut = n
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], "\n")
	}
	return append(out, s)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

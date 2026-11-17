// tg-bridge: Telegram <-> Devin CLI bridge.
//
// Long-polls the Telegram Bot API and forwards messages to a persistent
// `devin acp` session; Devin's reply text is sent back to the same chat.
//
// Setup: message @BotFather -> /newbot -> copy the token, then run:
//   tg-bridge -token <token>
// First /start locks in your chat id (saved next to the state file).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Afkhomie/devin-speech/internal/acp"
)

var (
	token   = flag.String("token", os.Getenv("TELEGRAM_BOT_TOKEN"), "bot token from @BotFather (or TELEGRAM_BOT_TOKEN env)")
	workDir = flag.String("dir", "", "working dir for the Devin session (default ~/tg-devin)")
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
	OK     bool            `json:"ok"`
	Result []tgMessage     `json:"result"`
	Error  string          `json:"description"`
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

	devin, err := startDevin(ctx, *workDir)
	if err != nil {
		log.Fatalf("devin acp: %v", err)
	}
	defer devin.client.Close()

	bot := &bot{
		token:  *token,
		state:  loadState(*state),
		devin:  devin,
		dir:    *workDir,
		client: &http.Client{Timeout: 40 * time.Second},
	}
	log.Printf("tg-bridge ready: dir=%s session=%s owner=%d", *workDir, devin.sid, bot.state.Owner)
	bot.poll(ctx)
	log.Println("shutting down")
}

// ---- devin side (same pattern as voice orchestrator) ----

type devinSession struct {
	client *acp.Client
	sid    string

	mu    sync.Mutex // serializes prompts
	curMu sync.Mutex
	cur   *strings.Builder
}

func startDevin(ctx context.Context, dir string) (*devinSession, error) {
	c, err := acp.New(ctx, dir, "bypass")
	if err != nil {
		return nil, err
	}
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	sid, err := c.NewSession(ctx, dir)
	if err != nil {
		return nil, err
	}
	d := &devinSession{client: c, sid: sid}
	go d.collect(c)
	return d, nil
}

func (d *devinSession) collect(c *acp.Client) {
	for u := range c.Updates() {
		if u.SessionID != d.sid || u.Kind != "agent_message_chunk" {
			continue
		}
		d.curMu.Lock()
		if d.cur != nil {
			d.cur.WriteString(u.Text)
		}
		d.curMu.Unlock()
	}
}

func (d *devinSession) ask(ctx context.Context, text string) (string, error) {
	if !d.mu.TryLock() {
		return "still working on your previous message — one sec", nil
	}
	defer d.mu.Unlock()

	var sb strings.Builder
	d.curMu.Lock()
	d.cur = &sb
	d.curMu.Unlock()
	defer func() {
		d.curMu.Lock()
		d.cur = nil
		d.curMu.Unlock()
	}()

	if _, err := d.client.Prompt(ctx, d.sid, text); err != nil {
		return "", fmt.Errorf("devin prompt: %w", err)
	}
	return strings.TrimSpace(sb.String()), nil
}

// ---- telegram side ----

type bridgeState struct {
	Owner      int64  `json:"owner"`       // telegram user id allowed to talk
	Offset     int    `json:"offset"`
	SessionID  string `json:"session_id"`  // unused v1, reserved for session/load
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
	return &s
}

func (s *bridgeState) save(path string) {
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o600)
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
			b.handle(ctx, upd)
		}
		b.state.save(statePath)
	}
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
			b.send(ctx, chatID, "locked in. i'm devin — talk to me like you talk to the cli. /reset starts a fresh session.")
			log.Printf("tg: owner registered uid=%d", uid)
		} else if uid != b.state.Owner {
			b.send(ctx, chatID, "this bot is private.")
		} else {
			b.send(ctx, chatID, "still here. /reset for a fresh devin session.")
		}
		return
	}
	if strings.HasPrefix(text, "/reset") {
		if uid != b.state.Owner {
			return
		}
		b.send(ctx, chatID, "resetting devin session…")
		if err := b.devin.reset(ctx, b.dir); err != nil {
			b.send(ctx, chatID, "reset failed: "+err.Error())
			return
		}
		b.send(ctx, chatID, "fresh session ready.")
		return
	}

	if b.state.Owner == 0 || uid != b.state.Owner {
		log.Printf("tg: ignoring uid=%d (owner=%d)", uid, b.state.Owner)
		return
	}

	b.sendChatAction(ctx, chatID, "typing")
	start := time.Now()
	reply, err := b.devin.ask(ctx, text)
	if err != nil {
		reply = "devin error: " + err.Error()
	} else if reply == "" {
		reply = "(devin returned empty response)"
	}
	log.Printf("tg %d -> devin (%s): %s", uid, time.Since(start).Round(time.Millisecond), truncate(reply, 80))
	for _, chunk := range splitMsg(reply, 4000) {
		b.send(ctx, chatID, chunk)
	}
}

func (d *devinSession) reset(ctx context.Context, dir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.client.Close()
	c, err := acp.New(ctx, dir, "bypass")
	if err != nil {
		return err
	}
	if err := c.Initialize(ctx); err != nil {
		return err
	}
	sid, err := c.NewSession(ctx, dir)
	if err != nil {
		return err
	}
	d.client = c
	d.sid = sid
	go d.collect(c)
	return nil
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


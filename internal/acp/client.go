package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	nextID atomic.Int64
	// pending maps request id -> response channel
	pending map[int64]chan json.RawMessage
	updates chan Update
	done    chan struct{}
}

type Update struct {
	SessionID string
	Kind      string // agent_message_chunk | agent_thought_chunk | tool_call | ...
	Text      string // only for *_chunk kinds
}

func New(ctx context.Context, projectDir, permissionMode string) (*Client, error) {
	cmd := exec.CommandContext(ctx, "devin", "acp")
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	if permissionMode != "" {
		cmd.Env = append(cmd.Environ(), "DEVIN_PERMISSION_MODE="+permissionMode)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start devin acp: %w", err)
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		pending: map[int64]chan json.RawMessage{},
		updates: make(chan Update, 256),
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c, nil
}

func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	defer close(c.updates)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var msg struct {
			ID     *int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg.ID != nil {
			// response to our request
			c.mu.Lock()
			ch, ok := c.pending[*msg.ID]
			delete(c.pending, *msg.ID)
			c.mu.Unlock()
			if ok {
				ch <- line
				close(ch)
			}
			continue
		}
		// notification or agent->client request; we only surface session/update
		if msg.Method == "session/update" {
			var p struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					SessionUpdate string `json:"sessionUpdate"`
					Content       struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if json.Unmarshal(msg.Params, &p) == nil && strings.HasSuffix(p.Update.SessionUpdate, "_chunk") {
				sel := Update{SessionID: p.SessionID, Kind: p.Update.SessionUpdate, Text: p.Update.Content.Text}
				select {
				case c.updates <- sel:
				case <-c.done:
					return
				}
			}
			continue
		}
		// agent->client requests (permission etc.) - auto-approve allow_*
		if msg.Method == "session/request_permission" && msg.ID != nil {
			go c.autoApprove(*msg.ID, msg.Params)
		}
	}
}

func (c *Client) autoApprove(id int64, params json.RawMessage) {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Options) == 0 {
		return
	}
	for _, o := range p.Options {
		if strings.HasPrefix(o.Kind, "allow") {
			c.respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"outcome":{"outcome":"selected","optionId":%q}}}`, id, o.OptionID))
			return
		}
	}
	c.respond(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"outcome":{"outcome":"cancelled"}}}`, id))
}

func (c *Client) respond(raw string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	io.WriteString(c.stdin, raw+"\n")
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	c.mu.Lock()
	_, err := c.stdin.Write(append(body, '\n'))
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(resp, &env) != nil {
			return nil, fmt.Errorf("bad response: %s", resp)
		}
		if env.Error != nil {
			return nil, fmt.Errorf("acp %s: %d %s", method, env.Error.Code, env.Error.Message)
		}
		return env.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("acp process exited")
	}
}

func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
	})
	return err
}

func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	res, err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.SessionID, nil
}

func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string) error {
	// session/load streams history as notifications then responds; we drain
	// updates in background so they don't block.
	_, err := c.call(ctx, "session/load", map[string]any{
		"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{},
	})
	return err
}

// Prompt sends a user message and blocks until the turn completes,
// returning stopReason. Streamed text arrives on Updates() - the caller
// owns that channel and must drain it concurrently.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	res, err := c.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.StopReason, nil
}

// Updates returns the channel of session/update notifications.
func (c *Client) Updates() <-chan Update { return c.updates }

func (c *Client) Close() error {
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

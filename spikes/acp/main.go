package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

var nextID atomic.Int64

func send(w *bufio.Writer, msg map[string]any) {
	b, _ := json.Marshal(msg)
	fmt.Printf("-> %s\n", b)
	w.Write(b)
	w.WriteByte('\n')
	w.Flush()
}

func req(method string, params any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      nextID.Add(1),
		"method":  method,
		"params":  params,
	}
}

func notify(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func main() {
	mode := "new"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	cwd, _ := os.Getwd()
	cmd := exec.Command("devin", "acp")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	defer cmd.Process.Kill()

	w := bufio.NewWriter(stdin)
	send(w, req("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
	}))

	var sessionID string
	switch mode {
	case "load":
		id := os.Args[2]
		send(w, req("session/load", map[string]any{
			"sessionId":   id,
			"cwd":         cwd,
			"mcpServers":  []any{},
		}))
		sessionID = id
	case "new":
		send(w, req("session/new", map[string]any{
			"cwd":        cwd,
			"mcpServers": []any{},
		}))
	}

	// read loop: print everything; if we see session/new result, grab sessionId and prompt
	prompted := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	deadline := time.After(600 * time.Second)
	for scanner.Scan() {
		line := scanner.Bytes()
		fmt.Printf("<- %s\n", line)
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if mode == "new" && !prompted {
			if res, ok := msg["result"].(map[string]any); ok {
				if sid, ok := res["sessionId"].(string); ok && sid != "" {
					sessionID = sid
					send(w, req("session/prompt", map[string]any{
						"sessionId": sessionID,
						"prompt":    []any{map[string]any{"type": "text", "text": "Reply with exactly one word: ok"}},
					}))
					prompted = true
				}
			}
			if err, ok := msg["error"]; ok {
				fmt.Printf("ERROR: %v\n", err)
			}
		}
		select {
		case <-deadline:
			return
		default:
		}
	}
	_ = filepath.Separator
}

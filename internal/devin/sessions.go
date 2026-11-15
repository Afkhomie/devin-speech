package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type SessionInfo struct {
	ID             string `json:"id"`
	WorkingDir     string `json:"working_directory"`
	LastActivityAt int64  `json:"last_activity_at"`
}

// ListSessions returns sessions for dir via `devin list --format json`.
func ListSessions(ctx context.Context, dir string) ([]SessionInfo, error) {
	out, err := exec.CommandContext(ctx, "devin", "list", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("devin list: %w", err)
	}
	var all []SessionInfo
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("parse devin list: %w", err)
	}
	var res []SessionInfo
	for _, s := range all {
		if s.WorkingDir == dir {
			res = append(res, s)
		}
	}
	return res, nil
}

// PidAlive reports whether pid refers to a running process.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return fileExists(fmt.Sprintf("/proc/%d", pid))
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LockedSessions scans session_locks and returns id->pid for LIVE holders.
func LockedSessions() (map[string]int, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local/share/devin/cli/session_locks")
	matches, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || !PidAlive(pid) {
			continue // stale lock
		}
		id := strings.TrimSuffix(filepath.Base(m), ".lock")
		out[id] = pid
	}
	return out, nil
}

// MostRecentUnlocked returns the most recently active session in dir
// whose lock is free. Returns "" if none.
func MostRecentUnlocked(ctx context.Context, dir string) (string, error) {
	locked, err := LockedSessions()
	if err != nil {
		return "", err
	}
	sessions, err := ListSessions(ctx, dir)
	if err != nil {
		return "", err
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActivityAt > sessions[j].LastActivityAt
	})
	for _, s := range sessions {
		if _, isLocked := locked[s.ID]; !isLocked {
			return s.ID, nil
		}
	}
	return "", nil
}

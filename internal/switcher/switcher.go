package switcher

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Afkhomie/devin-speech/internal/acp"
	"github.com/Afkhomie/devin-speech/internal/devin"
	"github.com/Afkhomie/devin-speech/internal/inject"
)

type Switcher struct {
	projectDir      string
	permissionMode  string
	pollInterval    time.Duration
	lockTimeout     time.Duration
	exitedTUI       bool   // we auto-exited a TUI to get here
	tuiSessionID    string // session the TUI held when we exited it
	tuiSessionCwd   string
	client          *acp.Client
	sessionID       string
}

func New(projectDir, permissionMode string) *Switcher {
	return &Switcher{
		projectDir:     projectDir,
		permissionMode: permissionMode,
		pollInterval:   200 * time.Millisecond,
		lockTimeout:    15 * time.Second,
	}
}

// EnterVoice performs the TUI->voice handshake and returns the attached
// session id. If a devin TUI session is locked (open in a terminal), it
// types /exit into the focused window, waits for the lock to release,
// then loads that exact session over ACP.
func (s *Switcher) EnterVoice(ctx context.Context) (string, error) {
	locked, err := devin.LockedSessions()
	if err != nil {
		return "", err
	}

	var targetID, targetCwd string
	if len(locked) > 0 {
		// exactly one locked session = the focused TUI's (v1 heuristic)
		for id := range locked {
			targetID = id
		}
		targetCwd = s.projectDir
		log.Printf("switcher: exiting TUI session %s", targetID)
		if err := inject.TypeText(ctx, "/exit"); err != nil {
			return "", fmt.Errorf("type /exit: %w", err)
		}
		if err := inject.Key(ctx, "Return"); err != nil {
			return "", fmt.Errorf("send Return: %w", err)
		}
		s.exitedTUI = true
		s.tuiSessionID = targetID
		s.tuiSessionCwd = targetCwd

		// wait for lock release
		deadline := time.Now().Add(s.lockTimeout)
		for time.Now().Before(deadline) {
			l, _ := devin.LockedSessions()
			if _, still := l[targetID]; !still {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(s.pollInterval):
			}
		}
	} else {
		// no TUI: most recent unlocked session in project dir
		targetID, err = devin.MostRecentUnlocked(ctx, s.projectDir)
		if err != nil {
			return "", err
		}
		if targetID == "" {
			// no session yet: caller should NewSession instead
			return "", nil
		}
		targetCwd = s.projectDir
	}

	// spawn acp client and attach
	c, err := acp.New(ctx, targetCwd, s.permissionMode)
	if err != nil {
		return "", err
	}
	if err := c.Initialize(ctx); err != nil {
		c.Close()
		return "", fmt.Errorf("acp initialize: %w", err)
	}
	if err := c.LoadSession(ctx, targetID, targetCwd); err != nil {
		c.Close()
		return "", fmt.Errorf("acp load %s: %w", targetID, err)
	}
	s.client = c
	s.sessionID = targetID
	return targetID, nil
}

// NewVoiceSession creates a fresh ACP session (no prior session exists).
func (s *Switcher) NewVoiceSession(ctx context.Context) (string, error) {
	c, err := acp.New(ctx, s.projectDir, s.permissionMode)
	if err != nil {
		return "", err
	}
	if err := c.Initialize(ctx); err != nil {
		c.Close()
		return "", err
	}
	id, err := c.NewSession(ctx, s.projectDir)
	if err != nil {
		c.Close()
		return "", err
	}
	s.client = c
	s.sessionID = id
	return id, nil
}

// Client exposes the attached ACP client for prompting.
func (s *Switcher) Client() *acp.Client { return s.client }
func (s *Switcher) SessionID() string    { return s.sessionID }
func (s *Switcher) ExitedTUI() bool      { return s.exitedTUI }

// ExitVoice closes the ACP session and, if we auto-exited a TUI,
// relaunches `devin -c` in the same terminal via keystroke injection.
func (s *Switcher) ExitVoice(ctx context.Context) error {
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.exitedTUI {
		// give the lock a moment to release before devin -c picks the session
		time.Sleep(500 * time.Millisecond)
		if err := inject.TypeText(ctx, "devin -c"); err != nil {
			return fmt.Errorf("type devin -c: %w", err)
		}
		if err := inject.Key(ctx, "Return"); err != nil {
			return fmt.Errorf("send Return: %w", err)
		}
		s.exitedTUI = false
	}
	return nil
}

// TermCwd returns the working directory a `devin -c` relaunch should use.
func (s *Switcher) TermCwd() string {
	if s.tuiSessionCwd != "" {
		return s.tuiSessionCwd
	}
	return s.projectDir
}

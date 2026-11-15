package keys

import (
	"testing"
	"time"
)

const (
	caps = 58 // KEY_CAPSLOCK
	space = 57 // KEY_SPACE
	aKey  = 30 // KEY_A
	ctrl  = 29 // KEY_LEFTCTRL
)

func TestCapsHoldCommits(t *testing.T) {
	m := NewMachine(MachineConfig{})
	var evs []Event
	evs = append(evs, m.Feed(caps, 1, 0)...)          // press
	evs = append(evs, m.Feed(caps, 0, 400*time.Millisecond)...) // release after 400ms
	if len(evs) != 2 || evs[0] != EvDictationBegin || evs[1] != EvDictationCommit {
		t.Fatalf("events = %v", evs)
	}
}

func TestCapsQuickTapIsCandidateNotCommit(t *testing.T) {
	m := NewMachine(MachineConfig{})
	var evs []Event
	evs = append(evs, m.Feed(caps, 1, 0)...)
	evs = append(evs, m.Feed(caps, 0, 80*time.Millisecond)...)
	if len(evs) != 2 || evs[0] != EvDictationBegin || evs[1] != EvDictationTap {
		t.Fatalf("events = %v", evs)
	}
}

func TestCapsDoubleTapLocksDictation(t *testing.T) {
	m := NewMachine(MachineConfig{})
	var evs []Event
	// tap 1
	evs = append(evs, m.Feed(caps, 1, 0)...)
	evs = append(evs, m.Feed(caps, 0, 60*time.Millisecond)...)
	// tap 2 within window
	evs = append(evs, m.Feed(caps, 1, 200*time.Millisecond)...)
	evs = append(evs, m.Feed(caps, 0, 260*time.Millisecond)...)
	found := false
	for _, e := range evs {
		if e == EvDictationLockOn {
			found = true
		}
	}
	if !found {
		t.Fatalf("no EvDictationLockOn in %v", evs)
	}
	if m.DictationLocked() != true {
		t.Fatal("DictationLocked should be true")
	}
}

func TestCapsTapWhileLockedStops(t *testing.T) {
	m := NewMachine(MachineConfig{})
	m.Feed(caps, 1, 0)
	m.Feed(caps, 0, 60*time.Millisecond)
	m.Feed(caps, 1, 200*time.Millisecond)
	m.Feed(caps, 0, 260*time.Millisecond) // lock on

	var evs []Event
	evs = append(evs, m.Feed(caps, 1, 2000*time.Millisecond)...)
	evs = append(evs, m.Feed(caps, 0, 2060*time.Millisecond)...)
	found := false
	for _, e := range evs {
		if e == EvDictationLockOff {
			found = true
		}
	}
	if !found {
		t.Fatalf("no EvDictationLockOff in %v", evs)
	}
	if m.DictationLocked() {
		t.Fatal("DictationLocked should be false after stop tap")
	}
}

func TestCapsAutorepeatIgnored(t *testing.T) {
	m := NewMachine(MachineConfig{})
	m.Feed(caps, 1, 0)
	var evs []Event
	for i := 1; i <= 5; i++ {
		evs = append(evs, m.Feed(caps, 2, time.Duration(i)*50*time.Millisecond)...)
	}
	if len(evs) != 0 {
		t.Fatalf("autorepeat produced events: %v", evs)
	}
	evs = append(evs, m.Feed(caps, 0, 500*time.Millisecond)...)
	if evs[len(evs)-1] != EvDictationCommit {
		t.Fatalf("release after repeats: %v", evs)
	}
}

func TestSpaceDoubleTapTogglesVoice(t *testing.T) {
	m := NewMachine(MachineConfig{})
	var evs []Event
	m.Feed(space, 1, 0)
	m.Feed(space, 0, 50*time.Millisecond)
	evs = append(evs, m.Feed(space, 1, 200*time.Millisecond)...)
	evs = append(evs, m.Feed(space, 0, 250*time.Millisecond)...)
	found := false
	for _, e := range evs {
		if e == EvVoiceToggle {
			found = true
		}
	}
	if !found {
		t.Fatalf("no EvVoiceToggle in %v", evs)
	}
}

func TestSpaceSlowTapsDoNothing(t *testing.T) {
	m := NewMachine(MachineConfig{})
	var evs []Event
	m.Feed(space, 1, 0)
	m.Feed(space, 0, 50*time.Millisecond)
	m.Feed(space, 1, 2000*time.Millisecond) // way outside window
	m.Feed(space, 0, 2050*time.Millisecond)
	for _, e := range evs {
		if e == EvVoiceToggle {
			t.Fatalf("slow taps triggered voice toggle: %v", evs)
		}
	}
}

func TestTypingWhileVoiceActive(t *testing.T) {
	m := NewMachine(MachineConfig{})
	m.SetVoiceActive(true)
	evs := m.Feed(aKey, 1, 0)
	if len(evs) != 1 || evs[0] != EvTyping {
		t.Fatalf("events = %v", evs)
	}
	// autorepeat must not retrigger
	if evs := m.Feed(aKey, 2, 30*time.Millisecond); len(evs) != 0 {
		t.Fatalf("repeat produced %v", evs)
	}
	// modifiers don't count as typing
	if evs := m.Feed(ctrl, 1, 40*time.Millisecond); len(evs) != 0 {
		t.Fatalf("modifier produced %v", evs)
	}
}

func TestNoTypingWhenVoiceInactive(t *testing.T) {
	m := NewMachine(MachineConfig{})
	if evs := m.Feed(aKey, 1, 0); len(evs) != 0 {
		t.Fatalf("typing event while voice inactive: %v", evs)
	}
}

func TestCapsStateTracking(t *testing.T) {
	m := NewMachine(MachineConfig{})
	m.Feed(caps, 1, 0)
	m.Feed(caps, 0, 60*time.Millisecond)
	m.Feed(caps, 1, 200*time.Millisecond)
	m.Feed(caps, 0, 260*time.Millisecond)
	// two taps = two toggles = caps back to off
	if m.CapsActive() {
		t.Fatal("CapsActive should be false after even taps")
	}
	m.Feed(caps, 1, 2000*time.Millisecond)
	m.Feed(caps, 0, 2060*time.Millisecond) // third tap while... locked? it stops lock; caps toggles once more -> on? 
	// after lock stop, this tap is a single tap candidate; caps state flips once -> ON
	if !m.CapsActive() {
		t.Fatal("CapsActive should be true after odd tap")
	}
}

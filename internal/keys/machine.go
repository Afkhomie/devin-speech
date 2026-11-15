package keys

import "time"

type Event int

const (
	EvNone Event = iota
	EvDictationBegin  // capslock pressed (recording should start)
	EvDictationCommit // capslock released after hold threshold -> transcribe+inject
	EvDictationTap    // capslock released quickly, not part of double-tap yet
	EvDictationLockOn // double-tap capslock -> lock dictation hands-free
	EvDictationLockOff // capslock tapped while locked -> stop+send
	EvVoiceToggle     // spacebar double-tap -> toggle voice mode
	EvTyping          // non-modifier key pressed while voice mode active
)

type MachineConfig struct {
	HoldThreshold   time.Duration // capslock held this long = dictation commit
	TapMax          time.Duration // press shorter than this = tap candidate
	DoubleTapWindow time.Duration // max gap between two taps to count as double
}

func (c MachineConfig) withDefaults() MachineConfig {
	if c.HoldThreshold == 0 {
		c.HoldThreshold = 300 * time.Millisecond
	}
	if c.TapMax == 0 {
		c.TapMax = 250 * time.Millisecond
	}
	if c.DoubleTapWindow == 0 {
		c.DoubleTapWindow = 400 * time.Millisecond
	}
	return c
}

const (
	keyCapsLock = 58
	keySpace    = 57
)

var modifierKeys = map[int]bool{
	29: true, 42: true, 54: true, 56: true, // ctrl shift right-shift alt
	97: true, 100: true, 125: true, 126: true, // rctrl ralt lmeta rmeta
	69: true, 70: true, // numlock scrolllock
}

// Machine is a pure state machine fed evdev key events.
// value: 0=release, 1=press, 2=autorepeat (ignored for state, except typing suppression).
type Machine struct {
	cfg MachineConfig

	capsOn        bool
	capsDown      bool
	capsDownTs    time.Duration
	capsLastTapTs time.Duration // release time of previous tap
	capsHasPending bool
	dictLocked    bool

	spaceDown    bool
	spaceDownTs  time.Duration
	spaceLastTap time.Duration
	spacePending bool

	voiceActive bool
}

func NewMachine(cfg MachineConfig) *Machine {
	return &Machine{cfg: cfg.withDefaults()}
}

func (m *Machine) SetVoiceActive(v bool) { m.voiceActive = v }
func (m *Machine) CapsActive() bool      { return m.capsOn }
func (m *Machine) DictationLocked() bool { return m.dictLocked }

func isModifier(code int) bool { return modifierKeys[code] }

func (m *Machine) Feed(code int, value int, ts time.Duration) []Event {
	if value == 2 {
		return nil // autorepeat: never a state transition
	}

	switch code {
	case keyCapsLock:
		return m.feedCaps(value, ts)
	case keySpace:
		return m.feedSpace(value, ts)
	default:
		if value == 1 && m.voiceActive && !isModifier(code) {
			return []Event{EvTyping}
		}
		return nil
	}
}

func (m *Machine) feedCaps(value int, ts time.Duration) []Event {
	var evs []Event
	if value == 1 {
		// press: caps state toggles physically
		m.capsOn = !m.capsOn
		m.capsDown = true
		m.capsDownTs = ts
		if m.dictLocked {
			// presses while locked are part of the stop gesture; no Begin
			return nil
		}
		return []Event{EvDictationBegin}
	}
	// release
	if !m.capsDown {
		return nil
	}
	m.capsDown = false
	held := ts - m.capsDownTs

	if m.dictLocked {
		m.dictLocked = false
		return []Event{EvDictationLockOff}
	}

	if held >= m.cfg.HoldThreshold {
		m.capsHasPending = false
		return []Event{EvDictationCommit}
	}

	// quick tap
	evs = append(evs, EvDictationTap)
	if m.capsHasPending && ts-m.capsLastTapTs <= m.cfg.DoubleTapWindow {
		m.capsHasPending = false
		m.dictLocked = true
		evs = append(evs, EvDictationLockOn)
		return evs
	}
	m.capsHasPending = true
	m.capsLastTapTs = ts
	return evs
}

func (m *Machine) feedSpace(value int, ts time.Duration) []Event {
	if value == 1 {
		m.spaceDown = true
		m.spaceDownTs = ts
		return nil
	}
	if !m.spaceDown {
		return nil
	}
	m.spaceDown = false
	held := ts - m.spaceDownTs
	if held >= m.cfg.TapMax {
		return nil // held space = normal typing repeat, not a tap
	}

	var evs []Event
	if m.voiceActive {
		evs = append(evs, EvTyping) // a space while voice-active is typing
	}
	if m.spacePending && ts-m.spaceLastTap <= m.cfg.DoubleTapWindow {
		m.spacePending = false
		return append(evs, EvVoiceToggle)
	}
	m.spacePending = true
	m.spaceLastTap = ts
	return evs
}

// Package outage implements the per-target UP/DOWN state machine over the
// ping stream. The core is pure (no I/O): Feed returns a transition or nil.
package outage

import "pingway.net/pingway/internal/store"

type State int

const (
	StateUnknown State = iota // before enough samples to decide
	StateUp
	StateDown
)

func (s State) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	default:
		return "unknown"
	}
}

type TransitionKind int

const (
	TransitionDown TransitionKind = iota // target went DOWN; open an outage event
	TransitionUp                         // target recovered; close the outage event
)

// Transition is emitted when a target crosses a threshold. At is the
// timestamp of the first failure (DOWN) or first success (UP) in the run
// that caused the transition, per spec.
type Transition struct {
	TargetID int64
	Kind     TransitionKind
	At       int64
}

// Machine is the state machine for a single target. Not safe for
// concurrent use; the Detector serializes access.
type Machine struct {
	failN int // consecutive failures to go DOWN
	okN   int // consecutive successes to go UP

	state        State
	failCount    int
	okCount      int
	firstFailTS  int64
	firstOkTS    int64
}

// NewMachine creates a machine in the given initial state. Use StateUnknown
// for fresh targets and StateDown to resume an open outage after restart.
func NewMachine(failThreshold, okThreshold int, initial State) *Machine {
	return &Machine{failN: failThreshold, okN: okThreshold, state: initial}
}

func (m *Machine) State() State { return m.state }

// Feed processes one sample and returns a transition if one occurred.
func (m *Machine) Feed(s store.Sample) *Transition {
	if s.Success {
		m.failCount = 0
		m.firstFailTS = 0
		if m.okCount == 0 {
			m.firstOkTS = s.TS
		}
		m.okCount++
		switch m.state {
		case StateDown:
			if m.okCount >= m.okN {
				m.state = StateUp
				return &Transition{TargetID: s.TargetID, Kind: TransitionUp, At: m.firstOkTS}
			}
		case StateUnknown:
			// first success settles an unknown target as UP, no event
			m.state = StateUp
		}
		return nil
	}

	m.okCount = 0
	m.firstOkTS = 0
	if m.failCount == 0 {
		m.firstFailTS = s.TS
	}
	m.failCount++
	switch m.state {
	case StateUp, StateUnknown:
		if m.failCount >= m.failN {
			prev := m.state
			m.state = StateDown
			if prev == StateUp {
				return &Transition{TargetID: s.TargetID, Kind: TransitionDown, At: m.firstFailTS}
			}
			// Unknown -> Down: target was never up (e.g. bad host); mark
			// DOWN and open an event so the UI shows it honestly.
			return &Transition{TargetID: s.TargetID, Kind: TransitionDown, At: m.firstFailTS}
		}
	}
	return nil
}

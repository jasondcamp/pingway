package outage

import (
	"testing"

	"pingway.net/pingway/internal/store"
)

func sample(ts int64, ok bool) store.Sample {
	return store.Sample{TargetID: 1, TS: ts, Success: ok, RTTMicros: 1000}
}

func TestMachineUpToDown(t *testing.T) {
	m := NewMachine(5, 3, StateUp)
	var tr *Transition
	for i := int64(0); i < 5; i++ {
		if tr != nil {
			t.Fatalf("transition before threshold")
		}
		tr = m.Feed(sample(1000+i, false))
	}
	if tr == nil || tr.Kind != TransitionDown {
		t.Fatalf("expected DOWN transition, got %+v", tr)
	}
	if tr.At != 1000 {
		t.Fatalf("started_at should be first failure ts, got %d", tr.At)
	}
	if m.State() != StateDown {
		t.Fatalf("state = %v", m.State())
	}
}

func TestMachineDownToUp(t *testing.T) {
	m := NewMachine(5, 3, StateDown)
	if tr := m.Feed(sample(1, true)); tr != nil {
		t.Fatal("early transition")
	}
	if tr := m.Feed(sample(2, true)); tr != nil {
		t.Fatal("early transition")
	}
	tr := m.Feed(sample(3, true))
	if tr == nil || tr.Kind != TransitionUp {
		t.Fatalf("expected UP transition, got %+v", tr)
	}
	if tr.At != 1 {
		t.Fatalf("ended_at should be first success ts, got %d", tr.At)
	}
}

func TestMachineFlappingDoesNotTransition(t *testing.T) {
	m := NewMachine(5, 3, StateUp)
	// alternate 4 failures / 1 success repeatedly: never 5 consecutive
	ts := int64(0)
	for cycle := 0; cycle < 20; cycle++ {
		for i := 0; i < 4; i++ {
			ts++
			if tr := m.Feed(sample(ts, false)); tr != nil {
				t.Fatalf("unexpected transition at cycle %d: %+v", cycle, tr)
			}
		}
		ts++
		if tr := m.Feed(sample(ts, true)); tr != nil {
			t.Fatalf("unexpected transition on success at cycle %d", cycle)
		}
	}
	if m.State() != StateUp {
		t.Fatalf("state = %v, want up", m.State())
	}
}

func TestMachineFlappingWhileDown(t *testing.T) {
	m := NewMachine(5, 3, StateDown)
	ts := int64(0)
	// 2 successes then a failure: stays down
	for cycle := 0; cycle < 10; cycle++ {
		ts++
		m.Feed(sample(ts, true))
		ts++
		m.Feed(sample(ts, true))
		ts++
		if tr := m.Feed(sample(ts, false)); tr != nil {
			t.Fatalf("unexpected transition: %+v", tr)
		}
	}
	if m.State() != StateDown {
		t.Fatalf("state = %v, want down", m.State())
	}
}

func TestMachineUnknownSettlesUpQuietly(t *testing.T) {
	m := NewMachine(5, 3, StateUnknown)
	if tr := m.Feed(sample(1, true)); tr != nil {
		t.Fatalf("unknown->up should not emit an event, got %+v", tr)
	}
	if m.State() != StateUp {
		t.Fatalf("state = %v", m.State())
	}
}

func TestMachineUnknownToDownEmitsEvent(t *testing.T) {
	m := NewMachine(3, 3, StateUnknown)
	m.Feed(sample(1, false))
	m.Feed(sample(2, false))
	tr := m.Feed(sample(3, false))
	if tr == nil || tr.Kind != TransitionDown || tr.At != 1 {
		t.Fatalf("expected DOWN@1 for never-up target, got %+v", tr)
	}
}

func TestMachineFullCycle(t *testing.T) {
	m := NewMachine(2, 2, StateUnknown)
	m.Feed(sample(1, true)) // settles up
	m.Feed(sample(2, false))
	tr := m.Feed(sample(3, false))
	if tr == nil || tr.Kind != TransitionDown || tr.At != 2 {
		t.Fatalf("down: %+v", tr)
	}
	m.Feed(sample(4, true))
	tr = m.Feed(sample(5, true))
	if tr == nil || tr.Kind != TransitionUp || tr.At != 4 {
		t.Fatalf("up: %+v", tr)
	}
	// interrupted recovery resets the success counter
	m.Feed(sample(6, false))
	m.Feed(sample(7, false)) // down again (threshold 2)
	m.Feed(sample(8, true))
	m.Feed(sample(9, false)) // reset
	if tr := m.Feed(sample(10, true)); tr != nil {
		t.Fatalf("recovery counter should have reset: %+v", tr)
	}
	tr = m.Feed(sample(11, true))
	if tr == nil || tr.Kind != TransitionUp || tr.At != 10 {
		t.Fatalf("expected UP@10, got %+v", tr)
	}
}

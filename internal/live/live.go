// Package live keeps in-memory rolling windows of recent samples per
// target, backing /api/status (live RTT, rolling 5-min loss) and the 1s SSE
// ping batches.
package live

import (
	"sync"

	"pingway.net/pingway/internal/store"
)

const windowMs = 300_000

type targetWindow struct {
	samples []store.Sample // ordered by ts, pruned to windowMs
}

type Tracker struct {
	mu      sync.Mutex
	windows map[int64]*targetWindow
	pending []store.Sample // accumulated since last Drain (for SSE batches)
}

func NewTracker() *Tracker {
	return &Tracker{windows: make(map[int64]*targetWindow)}
}

// Add records a sample in the target's rolling window and the pending SSE
// batch.
func (t *Tracker) Add(s store.Sample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.windows[s.TargetID]
	if !ok {
		w = &targetWindow{}
		t.windows[s.TargetID] = w
	}
	w.samples = append(w.samples, s)
	cutoff := s.TS - windowMs
	i := 0
	for i < len(w.samples) && w.samples[i].TS < cutoff {
		i++
	}
	if i > 0 {
		w.samples = append(w.samples[:0], w.samples[i:]...)
	}
	t.pending = append(t.pending, s)
	if len(t.pending) > 4096 {
		t.pending = t.pending[len(t.pending)-4096:]
	}
}

// Drain returns and clears the samples accumulated since the last call.
// The SSE emitter calls this every second.
func (t *Tracker) Drain() []store.Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.pending
	t.pending = nil
	return out
}

// Stats summarizes a target's rolling 5-minute window.
type Stats struct {
	LastRTTMicros int64   `json:"last_rtt_us"`
	LossPct       float64 `json:"loss_pct"`
	Sent          int     `json:"sent"`
}

// Stats returns the rolling-window summary for a target.
func (t *Tracker) Stats(targetID int64) Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.windows[targetID]
	if !ok || len(w.samples) == 0 {
		return Stats{LastRTTMicros: -1}
	}
	// loss excludes during-speedtest samples: drops under a line we are
	// deliberately saturating are self-inflicted, not path loss
	var sent, lost int
	lastRTT := int64(-1)
	for _, s := range w.samples {
		if s.DuringSpeedtest {
			continue
		}
		sent++
		if !s.Success {
			lost++
		}
	}
	for i := len(w.samples) - 1; i >= 0; i-- {
		if w.samples[i].Success {
			lastRTT = w.samples[i].RTTMicros
			break
		}
	}
	st := Stats{LastRTTMicros: lastRTT, Sent: sent}
	if sent > 0 {
		st.LossPct = 100 * float64(lost) / float64(sent)
	}
	return st
}

// Forget drops the window for a deleted target.
func (t *Tracker) Forget(targetID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.windows, targetID)
}

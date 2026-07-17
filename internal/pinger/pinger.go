// Package pinger runs one drift-free ping loop per enabled target and fans
// samples out through an injected callback.
package pinger

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"pingway.net/pingway/internal/store"
	"pingway.net/pingway/internal/supervise"
)

// PingFunc performs one echo and returns the RTT. The production
// implementation wraps pro-bing; tests inject fakes.
type PingFunc func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error)

// Manager owns the per-target ping goroutines and supports reconciling the
// running set against target CRUD changes without restart.
type Manager struct {
	ping     PingFunc
	interval time.Duration
	timeout  time.Duration
	onSample func(store.Sample)
	sup      *supervise.Supervisor
	log      *slog.Logger

	// duringSpeedtest is flipped by the speed tester so concurrent samples
	// are flagged.
	duringSpeedtest *atomic.Bool

	mu      sync.Mutex
	running map[int64]context.CancelFunc // target id -> stop
	specs   map[int64]string             // target id -> host+interval (to detect edits)
}

func NewManager(ping PingFunc, interval, timeout time.Duration, onSample func(store.Sample),
	duringSpeedtest *atomic.Bool, sup *supervise.Supervisor, log *slog.Logger) *Manager {
	return &Manager{
		ping:            ping,
		interval:        interval,
		timeout:         timeout,
		onSample:        onSample,
		duringSpeedtest: duringSpeedtest,
		sup:             sup,
		log:             log.With("component", "pinger"),
		running:         make(map[int64]context.CancelFunc),
		specs:           make(map[int64]string),
	}
}

// specKey identifies the loop-relevant fields of a target; a change in
// either restarts its loop.
func specKey(t store.Target) string {
	return fmt.Sprintf("%s|%d", t.Host, t.IntervalMs)
}

// Reconcile starts loops for enabled targets not yet running, restarts
// loops whose host or interval changed, and stops loops for
// removed/disabled targets.
func (m *Manager) Reconcile(ctx context.Context, targets []store.Target) {
	m.mu.Lock()
	defer m.mu.Unlock()

	want := make(map[int64]store.Target)
	for _, t := range targets {
		if t.Enabled {
			want[t.ID] = t
		}
	}

	for id, cancel := range m.running {
		t, ok := want[id]
		if ok && m.specs[id] == specKey(t) {
			continue
		}
		cancel()
		delete(m.running, id)
		delete(m.specs, id)
		if !ok {
			m.log.Info("stopped ping loop", "target_id", id)
		}
	}

	for id, t := range want {
		if _, ok := m.running[id]; ok {
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		m.running[id] = cancel
		m.specs[id] = specKey(t)
		target := t
		m.sup.Go(tctx, "ping:"+target.Name, func(c context.Context) error {
			m.loop(c, target)
			return nil
		})
		m.log.Info("started ping loop", "target", t.Name, "host", t.Host, "tier", t.Tier)
	}
}

// RunningCount reports active ping loops (for /healthz).
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// loop pings the target on a drift-free schedule: deadlines advance by the
// interval from a fixed origin regardless of how long each ping takes.
// A per-target interval override (e.g. a gentle 10s probe for a
// rate-limited WAN hairpin IP) takes precedence over the global cadence.
func (m *Manager) loop(ctx context.Context, t store.Target) {
	interval := m.interval
	if t.IntervalMs > 0 {
		interval = time.Duration(t.IntervalMs) * time.Millisecond
	}
	next := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		sampleTS := time.Now().UnixMilli()
		pctx, cancel := context.WithTimeout(ctx, m.timeout)
		rtt, err := m.ping(pctx, t.Host, m.timeout)
		cancel()

		if ctx.Err() != nil {
			return
		}
		s := store.Sample{
			TargetID:        t.ID,
			TS:              sampleTS,
			Success:         err == nil,
			DuringSpeedtest: m.duringSpeedtest != nil && m.duringSpeedtest.Load(),
		}
		if err == nil {
			s.RTTMicros = rtt.Microseconds()
		}
		m.onSample(s)

		// advance the deadline drift-free; if we fell behind (e.g. long
		// timeout runs), skip missed slots rather than bursting
		next = next.Add(interval)
		if wait := time.Until(next); wait > 0 {
			timer.Reset(wait)
		} else {
			missed := (-wait / interval) + 1
			next = next.Add(missed * interval)
			timer.Reset(time.Until(next))
		}
	}
}

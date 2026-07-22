package callprobe

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"

	"pingway.net/pingway/internal/supervise"
)

// Manager owns one supervised Prober per enabled reflector and
// reconciles the running set against the configured list.
type Manager struct {
	pps      int
	onSecond func(SecondBucket)
	onFreeze func(FreezeEvent)
	during   *atomic.Bool
	sup      *supervise.Supervisor
	log      *slog.Logger

	mu      sync.Mutex
	running map[int64]*runningProber
}

type runningProber struct {
	prober *Prober
	cancel context.CancelFunc
	spec   Reflector
}

func NewManager(pps int, onSecond func(SecondBucket), onFreeze func(FreezeEvent), during *atomic.Bool, sup *supervise.Supervisor, log *slog.Logger) *Manager {
	return &Manager{
		pps: pps, onSecond: onSecond, onFreeze: onFreeze, during: during,
		sup: sup, log: log, running: make(map[int64]*runningProber),
	}
}

// Reconcile starts probers for new reflectors, stops removed/disabled
// ones, and restarts changed ones.
func (m *Manager) Reconcile(ctx context.Context, reflectors []Reflector) {
	want := make(map[int64]Reflector, len(reflectors))
	for _, r := range reflectors {
		want[r.ID] = r
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rp := range m.running {
		if spec, ok := want[id]; !ok || spec != rp.spec {
			rp.cancel()
			delete(m.running, id)
		}
	}
	for id, spec := range want {
		if _, ok := m.running[id]; ok {
			continue
		}
		pctx, cancel := context.WithCancel(ctx)
		p := NewProber(spec, m.pps, m.onSecond, m.onFreeze, m.during, m.log)
		m.running[id] = &runningProber{prober: p, cancel: cancel, spec: spec}
		m.sup.Go(pctx, "callprobe-"+spec.Name, p.Run)
		m.log.Info("call probe started", "reflector", spec.Name, "host", spec.Host, "pps", m.pps)
	}
}

// Snapshots returns the live view of every running prober, ordered by
// reflector ID for stable display.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	probers := make([]*runningProber, 0, len(m.running))
	for _, rp := range m.running {
		probers = append(probers, rp)
	}
	m.mu.Unlock()
	out := make([]Snapshot, 0, len(probers))
	for _, rp := range probers {
		out = append(out, rp.prober.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReflectorID < out[j].ReflectorID })
	return out
}

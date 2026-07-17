package outage

import (
	"context"
	"log/slog"
	"sync"

	"pingway.net/pingway/internal/store"
)

// Detector owns one Machine per target, persists transitions as outage
// events, and notifies a callback (used to emit SSE status events).
type Detector struct {
	store  *store.Store
	log    *slog.Logger
	failN  int
	okN    int
	notify func(t Transition, newState State)

	mu       sync.Mutex
	machines map[int64]*Machine
	openIDs  map[int64]int64 // target id -> open outage event id
}

func NewDetector(s *store.Store, failN, okN int, notify func(Transition, State), log *slog.Logger) *Detector {
	return &Detector{
		store:    s,
		log:      log.With("component", "outage"),
		failN:    failN,
		okN:      okN,
		notify:   notify,
		machines: make(map[int64]*Machine),
		openIDs:  make(map[int64]int64),
	}
}

// Resume loads open outage events so targets that were DOWN at shutdown
// resume as DOWN and their events stay open (startup reconciliation pass).
func (d *Detector) Resume(ctx context.Context) error {
	open, err := d.store.OpenOutages(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range open {
		// Keep only the newest open event per target; close older ones at
		// their own start (zero-duration) to repair any inconsistency.
		if existing, ok := d.openIDs[e.TargetID]; ok {
			stale := min(existing, e.ID)
			keep := max(existing, e.ID)
			d.openIDs[e.TargetID] = keep
			if err := d.store.CloseOutage(ctx, stale, e.StartedAt); err != nil {
				d.log.Error("close stale outage", "id", stale, "err", err)
			}
			continue
		}
		d.openIDs[e.TargetID] = e.ID
		d.machines[e.TargetID] = NewMachine(d.failN, d.okN, StateDown)
		d.log.Info("resumed open outage", "target_id", e.TargetID, "event_id", e.ID)
	}
	return nil
}

// Feed runs one sample through its target's machine, persisting any
// transition. Safe for concurrent use.
func (d *Detector) Feed(ctx context.Context, s store.Sample) {
	d.mu.Lock()
	m, ok := d.machines[s.TargetID]
	if !ok {
		m = NewMachine(d.failN, d.okN, StateUnknown)
		d.machines[s.TargetID] = m
	}
	tr := m.Feed(s)
	newState := m.State()
	var notify func()
	if tr != nil {
		switch tr.Kind {
		case TransitionDown:
			id, err := d.store.OpenOutage(ctx, tr.TargetID, tr.At)
			if err != nil {
				d.log.Error("open outage", "target_id", tr.TargetID, "err", err)
			} else {
				d.openIDs[tr.TargetID] = id
				d.log.Warn("target DOWN", "target_id", tr.TargetID, "since", tr.At)
			}
		case TransitionUp:
			if id, ok := d.openIDs[tr.TargetID]; ok {
				if err := d.store.CloseOutage(ctx, id, tr.At); err != nil {
					d.log.Error("close outage", "id", id, "err", err)
				}
				delete(d.openIDs, tr.TargetID)
			}
			d.log.Info("target UP", "target_id", tr.TargetID, "at", tr.At)
		}
		t := *tr
		notify = func() {
			if d.notify != nil {
				d.notify(t, newState)
			}
		}
	}
	d.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// States returns the current state per target id.
func (d *Detector) States() map[int64]State {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[int64]State, len(d.machines))
	for id, m := range d.machines {
		out[id] = m.State()
	}
	return out
}

// Forget drops state for a deleted target (its open event, if any, is
// closed at the given timestamp).
func (d *Detector) Forget(ctx context.Context, targetID, ts int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.openIDs[targetID]; ok {
		if err := d.store.CloseOutage(ctx, id, ts); err != nil {
			d.log.Error("close outage on forget", "id", id, "err", err)
		}
		delete(d.openIDs, targetID)
	}
	delete(d.machines, targetID)
}

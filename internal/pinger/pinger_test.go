package pinger

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pingway.net/pingway/internal/outage"
	"pingway.net/pingway/internal/store"
	"pingway.net/pingway/internal/supervise"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTestDetector(t *testing.T, st *store.Store) *outage.Detector {
	t.Helper()
	return outage.NewDetector(st, 3, 2, nil, testLogger())
}

// TestDriftFreeSchedule pings with an artificially slow ping function and
// asserts sample timestamps stay aligned to the interval grid rather than
// drifting by the work duration each cycle.
func TestDriftFreeSchedule(t *testing.T) {
	interval := 50 * time.Millisecond
	workDur := 20 * time.Millisecond // 40% of the interval; naive sleep-after-work would drift badly

	var mu sync.Mutex
	var stamps []time.Time
	ping := func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		time.Sleep(workDur)
		return time.Millisecond, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := supervise.New(testLogger())
	m := NewManager(ping, interval, time.Second, func(store.Sample) {}, nil, sup, testLogger())
	m.Reconcile(ctx, []store.Target{{ID: 1, Name: "t", Host: "x", Enabled: true}})

	time.Sleep(20*interval + interval/2)
	cancel()
	sup.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 15 {
		t.Fatalf("too few pings: %d", len(stamps))
	}
	// total elapsed over N intervals should be ~N*interval, not N*(interval+work)
	elapsed := stamps[len(stamps)-1].Sub(stamps[0])
	ideal := time.Duration(len(stamps)-1) * interval
	drift := elapsed - ideal
	if drift < 0 {
		drift = -drift
	}
	if drift > interval {
		t.Fatalf("schedule drifted %v over %d pings (ideal %v, actual %v)", drift, len(stamps), ideal, elapsed)
	}
}

// TestEndToEndOutageDetection runs a simulated target that fails on
// schedule through pinger -> writer -> temp SQLite and the outage
// detector, verifying an outage event is opened and closed.
func TestEndToEndOutageDetection(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/e2e.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := st.CreateTarget(ctx, store.Target{Name: "sim", Host: "sim", Tier: 3, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// fail pings 10-19, succeed otherwise
	var count atomic.Int64
	ping := func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		n := count.Add(1)
		if n > 10 && n <= 20 {
			return 0, context.DeadlineExceeded
		}
		return 5 * time.Millisecond, nil
	}

	sup := supervise.New(testLogger())
	writer := store.NewWriter(st, testLogger())

	// small thresholds so the test is fast: 3 fails down, 2 ok up
	detector := newTestDetector(t, st)

	onSample := func(s store.Sample) {
		writer.Submit(s)
		detector.Feed(ctx, s)
	}
	m := NewManager(ping, 5*time.Millisecond, time.Second, onSample, nil, sup, testLogger())

	sup.Go(ctx, "writer", writer.Run)
	m.Reconcile(ctx, []store.Target{{ID: id, Name: "sim", Host: "sim", Enabled: true}})

	deadline := time.After(5 * time.Second)
	for {
		events, err := st.ListOutages(ctx, 0, time.Now().UnixMilli()+1000, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 1 && events[0].EndedAt != nil {
			if *events[0].DurationMs < 0 {
				t.Fatalf("negative duration: %+v", events[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no closed outage event; events = %+v", events)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	sup.Wait()

	// writer flushed samples on shutdown
	samples, err := st.QuerySamples(context.Background(), id, 0, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) < 20 {
		t.Fatalf("expected >=20 samples written, got %d", len(samples))
	}
}

// TestPerTargetInterval verifies an interval override slows one target
// without affecting others, and that changing it restarts the loop.
func TestPerTargetInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := supervise.New(testLogger())

	var fastN, slowN atomic.Int64
	ping := func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		if host == "slow" {
			slowN.Add(1)
		} else {
			fastN.Add(1)
		}
		return time.Millisecond, nil
	}
	m := NewManager(ping, 20*time.Millisecond, time.Second, func(store.Sample) {}, nil, sup, testLogger())
	m.Reconcile(ctx, []store.Target{
		{ID: 1, Name: "fast", Host: "fast", Enabled: true},
		{ID: 2, Name: "slow", Host: "slow", Enabled: true, IntervalMs: 200},
	})
	time.Sleep(500 * time.Millisecond)
	f, sl := fastN.Load(), slowN.Load()
	if f < 15 {
		t.Fatalf("fast target pinged %d times, want >=15", f)
	}
	if sl > 5 {
		t.Fatalf("slow target pinged %d times, want <=5 (200ms interval)", sl)
	}

	// interval change must restart the loop (spec key change)
	m.Reconcile(ctx, []store.Target{
		{ID: 1, Name: "fast", Host: "fast", Enabled: true},
		{ID: 2, Name: "slow", Host: "slow", Enabled: true, IntervalMs: 20},
	})
	slowN.Store(0)
	time.Sleep(300 * time.Millisecond)
	if slowN.Load() < 8 {
		t.Fatalf("slow target should speed up after interval change, got %d", slowN.Load())
	}
	cancel()
	sup.Wait()
}

func TestReconcileStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := supervise.New(testLogger())
	ping := func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		return time.Millisecond, nil
	}
	m := NewManager(ping, 10*time.Millisecond, time.Second, func(store.Sample) {}, nil, sup, testLogger())

	targets := []store.Target{
		{ID: 1, Name: "a", Host: "a", Enabled: true},
		{ID: 2, Name: "b", Host: "b", Enabled: true},
		{ID: 3, Name: "c", Host: "c", Enabled: false},
	}
	m.Reconcile(ctx, targets)
	if m.RunningCount() != 2 {
		t.Fatalf("running = %d, want 2", m.RunningCount())
	}
	// disable one, edit another's host
	targets[0].Enabled = false
	targets[1].Host = "b2"
	m.Reconcile(ctx, targets)
	if m.RunningCount() != 1 {
		t.Fatalf("running = %d, want 1", m.RunningCount())
	}
	m.Reconcile(ctx, nil)
	if m.RunningCount() != 0 {
		t.Fatalf("running = %d, want 0", m.RunningCount())
	}
}

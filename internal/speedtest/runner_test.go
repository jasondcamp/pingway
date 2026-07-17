package speedtest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"pingway.net/pingway/internal/api"
	"pingway.net/pingway/internal/config"
	"pingway.net/pingway/internal/settings"
	"pingway.net/pingway/internal/sse"
	"pingway.net/pingway/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func testRunner(t *testing.T, tier3Down bool) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/st.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, _ := config.Load(t.TempDir() + "/none.yaml")
	sm, err := settings.NewManager(context.Background(), st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var during atomic.Bool
	r := NewRunner(st, sm, sse.NewHub(testLogger()), &during,
		func() bool { return tier3Down }, t.TempDir(), testLogger())
	return r, st
}

// mockEngine lets tests control run outcome and duration.
type mockEngine struct {
	name  string
	delay time.Duration
	fail  bool
	runs  atomic.Int64
}

func (m *mockEngine) Name() string { return m.name }
func (m *mockEngine) Run(ctx context.Context) (*Result, error) {
	m.runs.Add(1)
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if m.fail {
		return nil, context.DeadlineExceeded
	}
	return &Result{Engine: m.name, DownloadBps: 1e8, UploadBps: 1e7, LatencyMs: 5, RanAt: time.Now()}, nil
}

func TestRunOnceStoresResult(t *testing.T) {
	r, st := testRunner(t, false)
	mock := &mockEngine{name: "mock"}
	r.engineOverride = mock

	r.runOnce(context.Background(), "mock")

	rows, err := st.ListSpeedTests(context.Background(), 0, time.Now().UnixMilli()+1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Engine != "mock" || rows[0].DownloadBps != 1e8 || rows[0].Error != "" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunOnceStoresFailure(t *testing.T) {
	r, st := testRunner(t, false)
	r.engineOverride = &mockEngine{name: "mock", fail: true}

	r.runOnce(context.Background(), "mock")

	rows, _ := st.ListSpeedTests(context.Background(), 0, time.Now().UnixMilli()+1000)
	if len(rows) != 1 || rows[0].Error == "" {
		t.Fatalf("failed run should be stored with error, rows = %+v", rows)
	}
}

func TestTriggerNowConflictsWhileRunning(t *testing.T) {
	r, _ := testRunner(t, false)
	mock := &mockEngine{name: "mock", delay: 300 * time.Millisecond}
	r.engineOverride = mock

	done := make(chan struct{})
	go func() {
		r.runOnce(context.Background(), "mock")
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !r.Running() {
		if time.Now().After(deadline) {
			t.Fatal("runner never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := r.TriggerNow(); err != api.ErrSpeedtestRunning {
		t.Fatalf("TriggerNow while running = %v, want ErrSpeedtestRunning", err)
	}
	<-done
	if r.Running() {
		t.Fatal("still marked running after finish")
	}
}

func TestUnknownEngineStoredAsFailure(t *testing.T) {
	r, st := testRunner(t, false)
	r.runOnce(context.Background(), "bogus")
	rows, _ := st.ListSpeedTests(context.Background(), 0, time.Now().UnixMilli()+1000)
	if len(rows) != 1 || rows[0].Error == "" {
		t.Fatalf("unknown engine should store a failed result, rows = %+v", rows)
	}
}

func TestDuringSpeedtestFlagToggles(t *testing.T) {
	r, _ := testRunner(t, false)
	var sawTrue atomic.Bool
	mock := &mockEngine{name: "mock", delay: 50 * time.Millisecond}
	r.engineOverride = mock
	go func() {
		for i := 0; i < 100; i++ {
			if r.duringSpeedtest.Load() {
				sawTrue.Store(true)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	r.runOnce(context.Background(), "mock")
	if !sawTrue.Load() {
		t.Fatal("during_speedtest flag never set during run")
	}
	if r.duringSpeedtest.Load() {
		t.Fatal("during_speedtest flag not cleared after run")
	}
}

// TestMeasureThroughputSteadyState verifies ramp-up bytes are discarded.
func TestMeasureThroughputSteadyState(t *testing.T) {
	// server that returns fixed-size chunks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write(make([]byte, 256*1024))
	}))
	defer srv.Close()

	client := srv.Client()
	bps, err := measureThroughput(context.Background(), 2, 600*time.Millisecond, 100*time.Millisecond,
		func(ctx context.Context, counted *atomic.Int64) {
			drainDownload(ctx, client, srv.URL, counted)
		})
	if err != nil {
		t.Fatal(err)
	}
	if bps <= 0 {
		t.Fatalf("bps = %v", bps)
	}
}

func TestMeasureHTTPLatency(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(0))
	}))
	defer srv.Close()
	lat, err := measureHTTPLatency(context.Background(), srv.Client(), srv.URL, 5)
	if err != nil {
		t.Fatal(err)
	}
	if lat <= 0 || hits.Load() != 5 {
		t.Fatalf("lat = %v, hits = %d", lat, hits.Load())
	}
}

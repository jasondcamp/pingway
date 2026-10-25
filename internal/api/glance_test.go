package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"pingway.net/pingway/internal/store"
)

// sample feeds the tracker the way the pinger does.
func sample(t *testing.T, srv *Server, id int64, ts int64, rttMs float64, ok bool) {
	t.Helper()
	srv.tracker.Add(store.Sample{
		TargetID:  id,
		TS:        ts,
		RTTMicros: int64(rttMs * 1000),
		Success:   ok,
	})
}

func getGlance(t *testing.T, srv *Server) (glanceResponse, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/glance", nil))
	var g glanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return g, rec
}

// A healthy LAN and a healthy internet, with the numbers the spec's example
// shows, must come back ok with nothing to blame.
func TestGlanceHealthy(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN Gateway", Host: "192.168.1.1", Tier: 1, Enabled: true})
	cf, _ := st.CreateTarget(ctx, store.Target{Name: "Cloudflare", Host: "1.1.1.1", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	for i := range 20 {
		ts := now - int64(19-i)*1000 // oldest first, as the pinger appends
		sample(t, srv, lan, ts, 1.3, true)
		sample(t, srv, cf, ts, 25.4, true)
	}

	g, rec := getGlance(t, srv)
	if g.Status != "ok" {
		t.Errorf("status = %q, want ok", g.Status)
	}
	if g.WorstHop != nil {
		t.Errorf("worst_hop = %q, want null", *g.WorstHop)
	}
	if g.LANMs == nil || *g.LANMs != 1.3 {
		t.Errorf("lan_ms = %v, want 1.3", g.LANMs)
	}
	if g.InternetMs == nil || *g.InternetMs != 25.4 {
		t.Errorf("internet_ms = %v, want 25.4", g.InternetMs)
	}
	if g.WindowS != 60 {
		t.Errorf("window_s = %d, want 60", g.WindowS)
	}
	if g.Stale {
		t.Error("stale = true with samples one second old")
	}
	// A glance one poll out of date is worse than none.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// Tuesday, end to end: a LAN at 95ms and 39% loss while the internet is
// fine. The old dashboard logic called this healthy.
func TestGlanceBadLANHealthyInternet(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN Gateway", Host: "192.168.1.1", Tier: 1, Enabled: true})
	cf, _ := st.CreateTarget(ctx, store.Target{Name: "Cloudflare", Host: "1.1.1.1", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	for i := range 100 {
		ts := now - int64(99-i)*500
		sample(t, srv, lan, ts, 95, i%100 >= 39) // 39% loss
		sample(t, srv, cf, ts, 25, true)
	}

	g, _ := getGlance(t, srv)
	if g.Status != "down" {
		t.Errorf("status = %q, want down", g.Status)
	}
	if g.WorstHop == nil || *g.WorstHop != "lan" {
		t.Errorf("worst_hop = %v, want lan", g.WorstHop)
	}
	if g.LANLossPct == nil || *g.LANLossPct < 30 {
		t.Errorf("lan_loss_pct = %v, want roughly 39", g.LANLossPct)
	}
}

// One anchor rate-limiting our pings must not paint the internet red while
// another anchor is answering perfectly.
func TestGlanceOneBadAnchorDoesNotCount(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN", Host: "192.168.1.1", Tier: 1, Enabled: true})
	cf, _ := st.CreateTarget(ctx, store.Target{Name: "Cloudflare", Host: "1.1.1.1", Tier: 3, Enabled: true})
	goog, _ := st.CreateTarget(ctx, store.Target{Name: "Google", Host: "8.8.8.8", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	for i := range 40 {
		ts := now - int64(39-i)*1000
		sample(t, srv, lan, ts, 1, true)
		sample(t, srv, cf, ts, 300, i%2 == 0) // 50% loss, awful latency
		sample(t, srv, goog, ts, 22, true)    // perfectly fine
	}

	g, _ := getGlance(t, srv)
	if g.Status != "ok" {
		t.Errorf("status = %q, want ok (Google is healthy)", g.Status)
	}
	if g.InternetMs == nil || *g.InternetMs != 22 {
		t.Errorf("internet_ms = %v, want 22 (the better anchor)", g.InternetMs)
	}
	if g.LossPct == nil || *g.LossPct != 0 {
		t.Errorf("loss_pct = %v, want 0 (loss counts only if every path loses)", g.LossPct)
	}
}

// Nothing arriving is "unknown", never "ok". Silence must not read as health.
func TestGlanceStale(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN", Host: "192.168.1.1", Tier: 1, Enabled: true})

	old := time.Now().UnixMilli() - 30_000
	sample(t, srv, lan, old, 1, true)

	g, _ := getGlance(t, srv)
	if !g.Stale {
		t.Error("stale = false after 30s without a sample")
	}
	if g.Status != "unknown" {
		t.Errorf("status = %q, want unknown", g.Status)
	}
	if g.WorstHop != nil {
		t.Errorf("worst_hop = %q, want null when unknown", *g.WorstHop)
	}
}

// With no samples at all, the endpoint still answers rather than erroring,
// and says it knows nothing.
func TestGlanceNoData(t *testing.T) {
	srv, _ := testServer(t)
	g, rec := getGlance(t, srv)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if g.Status != "unknown" || !g.Stale {
		t.Errorf("got %q stale=%v, want unknown/true", g.Status, g.Stale)
	}
	if g.LANMs != nil || g.InternetMs != nil {
		t.Error("latencies should be null, not zero, with no samples")
	}
	if g.UpdatedAt != "" {
		t.Errorf("updated_at = %q, want empty with no samples", g.UpdatedAt)
	}
}

// Samples taken while a speed test saturates the line are self-inflicted and
// must not count as loss, matching live.Stats.
func TestGlanceIgnoresSpeedtestSamples(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN", Host: "192.168.1.1", Tier: 1, Enabled: true})
	cf, _ := st.CreateTarget(ctx, store.Target{Name: "Cloudflare", Host: "1.1.1.1", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	for i := range 30 {
		ts := now - int64(29-i)*1000
		sample(t, srv, lan, ts, 1, true)
		srv.tracker.Add(store.Sample{TargetID: cf, TS: ts, Success: false, DuringSpeedtest: true})
		sample(t, srv, cf, ts, 25, true)
	}

	g, _ := getGlance(t, srv)
	if g.LossPct == nil || *g.LossPct != 0 {
		t.Errorf("loss_pct = %v, want 0; speedtest drops are self-inflicted", g.LossPct)
	}
	if g.Status != "ok" {
		t.Errorf("status = %q, want ok", g.Status)
	}
}

// The 5-minute tracker window must not leak into a 60-second answer.
func TestGlanceWindowIsSixtySeconds(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	lan, _ := st.CreateTarget(ctx, store.Target{Name: "LAN", Host: "192.168.1.1", Tier: 1, Enabled: true})

	now := time.Now().UnixMilli()
	// Four minutes ago: inside the tracker's window, outside the glance's.
	for i := range 50 {
		srv.tracker.Add(store.Sample{TargetID: lan, TS: now - 240_000 - int64(i)*100, Success: false})
	}
	for i := range 30 {
		sample(t, srv, lan, now-int64(29-i)*1000, 1.0, true)
	}

	g, _ := getGlance(t, srv)
	if g.LANLossPct == nil || *g.LANLossPct != 0 {
		t.Errorf("lan_loss_pct = %v, want 0; old loss is outside the 60s window", g.LANLossPct)
	}
}

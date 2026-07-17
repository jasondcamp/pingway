package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pingway.net/pingway/internal/live"
	"pingway.net/pingway/internal/outage"
	"pingway.net/pingway/internal/sse"
	"pingway.net/pingway/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/api.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := NewServer(Options{
		Store:    st,
		Tracker:  live.NewTracker(),
		Detector: outage.NewDetector(st, 5, 3, nil, testLogger()),
		Hub:      sse.NewHub(testLogger()),
		PingMode: "udp",
		Version:  "test",
		Log:      testLogger(),
	})
	return srv, st
}

func TestPickResolution(t *testing.T) {
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	cases := []struct {
		span time.Duration
		want string
	}{
		{30 * time.Minute, "raw"},
		{2 * time.Hour, "raw"},
		{2*time.Hour + time.Minute, "1m"},
		{6 * time.Hour, "1m"},
		{24 * time.Hour, "1m"},
		{48 * time.Hour, "1m"},
		{49 * time.Hour, "1h"},
		{7 * 24 * time.Hour, "1h"},
		{30 * 24 * time.Hour, "1h"},
	}
	for _, c := range cases {
		if got := PickResolution(0, ms(c.span)); got != c.want {
			t.Errorf("span %v: got %s, want %s", c.span, got, c.want)
		}
	}
}

func TestPingResolutionAutoSelect(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	id, _ := st.CreateTarget(ctx, store.Target{Name: "t", Host: "1.2.3.4", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	for _, span := range []struct {
		fromAgo time.Duration
		wantRes string
	}{
		{time.Hour, "raw"},
		{6 * time.Hour, "1m"},
		{7 * 24 * time.Hour, "1h"},
	} {
		url := fmt.Sprintf("/api/ping?target=%d&from=%d&to=%d", id, now-span.fromAgo.Milliseconds(), now)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", url, rec.Code, rec.Body)
		}
		var resp pingResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Resolution != span.wantRes {
			t.Errorf("span %v: resolution %s, want %s", span.fromAgo, resp.Resolution, span.wantRes)
		}
	}

	// explicit resolution wins over auto-select
	url := fmt.Sprintf("/api/ping?target=%d&from=%d&to=%d&resolution=1h", id, now-60_000, now)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	var resp pingResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Resolution != "1h" {
		t.Errorf("explicit resolution ignored: %s", resp.Resolution)
	}
}

// TestSummaryGolden seeds a fixed dataset and asserts the exact summary
// numbers.
func TestSummaryGolden(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	id, _ := st.CreateTarget(ctx, store.Target{Name: "anchor", Host: "9.9.9.9", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	// 100 raw samples within the last hour: 90 ok (rtt 1000..90000 µs), 10 lost
	w := store.NewWriter(st, testLogger())
	var samples []store.Sample
	base := now - 30*60_000
	for i := 0; i < 90; i++ {
		samples = append(samples, store.Sample{TargetID: id, TS: base + int64(i)*1000, RTTMicros: int64(i+1) * 1000, Success: true})
	}
	for i := 90; i < 100; i++ {
		samples = append(samples, store.Sample{TargetID: id, TS: base + int64(i)*1000, Success: false})
	}
	if err := w.Flush(samples); err != nil {
		t.Fatal(err)
	}

	// one closed outage of 90s inside the window
	oid, _ := st.OpenOutage(ctx, id, now-10*60_000)
	st.CloseOutage(ctx, oid, now-10*60_000+90_000)

	// two speedtests
	st.InsertSpeedTest(ctx, store.SpeedTestRow{Engine: "mock", DownloadBps: 100e6, UploadBps: 10e6, LatencyMs: 10, RanAt: now - 20*60_000})
	st.InsertSpeedTest(ctx, store.SpeedTestRow{Engine: "mock", DownloadBps: 200e6, UploadBps: 30e6, LatencyMs: 20, RanAt: now - 10*60_000})
	// failed speedtest must be excluded from stats
	st.InsertSpeedTest(ctx, store.SpeedTestRow{Engine: "mock", Error: "boom", RanAt: now - 5*60_000})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/summary?range=1h", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Targets) != 1 {
		t.Fatalf("targets = %d", len(resp.Targets))
	}
	ts := resp.Targets[0]
	if ts.Sent != 100 || ts.Lost != 10 {
		t.Errorf("sent/lost = %d/%d, want 100/10", ts.Sent, ts.Lost)
	}
	if ts.UptimePct != 90 {
		t.Errorf("uptime = %v, want 90", ts.UptimePct)
	}
	// avg of 1000..90000 = 45500
	if ts.RTTAvgUs != 45500 {
		t.Errorf("avg = %d, want 45500", ts.RTTAvgUs)
	}
	// p95 via OFFSET floor(90*0.95)=85 (0-indexed 86th value) = 86000
	if ts.RTTP95Us != 86000 {
		t.Errorf("p95 = %d, want 86000", ts.RTTP95Us)
	}
	if ts.OutageCount != 1 || ts.OutageMs != 90_000 {
		t.Errorf("outages = %d/%dms, want 1/90000ms", ts.OutageCount, ts.OutageMs)
	}
	if resp.Speedtest == nil {
		t.Fatal("speedtest summary missing")
	}
	sp := resp.Speedtest
	if sp.Count != 2 || sp.DownMinBps != 100e6 || sp.DownAvgBps != 150e6 || sp.DownMaxBps != 200e6 {
		t.Errorf("down stats: %+v", sp)
	}
	if sp.UpAvgBps != 20e6 || sp.LatencyAvgMs != 15 {
		t.Errorf("up/latency stats: %+v", sp)
	}
	// single tier-3 target down 90s of 3600s => internet outage too
	if resp.InternetOutages != 1 || resp.InternetOutageMs != 90_000 {
		t.Errorf("internet outages = %d/%dms", resp.InternetOutages, resp.InternetOutageMs)
	}
	wantUp := 100 * (1 - 90_000.0/3_600_000.0)
	if diff := resp.InternetUptimePct - wantUp; diff > 0.01 || diff < -0.01 {
		t.Errorf("internet uptime = %v, want %v", resp.InternetUptimePct, wantUp)
	}
}

func TestTargetCRUD(t *testing.T) {
	srv, _ := testServer(t)
	do := func(method, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var req = httptest.NewRequest(method, url, nil)
		if body != "" {
			req = httptest.NewRequest(method, url, jsonBody(body))
		}
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := do("POST", "/api/targets", `{"name":"R","host":"10.0.0.1","tier":1}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created store.Target
	json.Unmarshal(rec.Body.Bytes(), &created)

	if rec = do("POST", "/api/targets", `{"name":"R2","host":"10.0.0.1","tier":1}`); rec.Code != 409 {
		t.Fatalf("dup host: %d", rec.Code)
	}
	if rec = do("POST", "/api/targets", `{"name":"","host":"10.0.0.2","tier":1}`); rec.Code != 400 {
		t.Fatalf("empty name: %d", rec.Code)
	}
	if rec = do("POST", "/api/targets", `{"name":"X","host":"10.0.0.3","tier":7}`); rec.Code != 400 {
		t.Fatalf("bad tier: %d", rec.Code)
	}

	url := fmt.Sprintf("/api/targets/%d", created.ID)
	if rec = do("PUT", url, `{"name":"R!","host":"10.0.0.9","tier":2,"enabled":false}`); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec = do("PUT", "/api/targets/9999", `{"name":"x","host":"h","tier":1}`); rec.Code != 404 {
		t.Fatalf("update missing: %d", rec.Code)
	}
	if rec = do("DELETE", url, ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec = do("GET", "/api/targets", ""); rec.Code != 200 || rec.Body.String() == "" {
		t.Fatalf("list: %d", rec.Code)
	}
}

func jsonBody(s string) *strings.Reader { return strings.NewReader(s) }

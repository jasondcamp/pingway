package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"pingway.net/pingway/internal/store"
)

func TestLossBursts(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()
	id, _ := st.CreateTarget(ctx, store.Target{Name: "t", Host: "5.5.5.5", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	base := now - 30*60_000
	w := store.NewWriter(st, testLogger())
	var samples []store.Sample
	ts := base
	add := func(ok bool, flagged bool) {
		samples = append(samples, store.Sample{
			TargetID: id, TS: ts, RTTMicros: 1000, Success: ok, DuringSpeedtest: flagged,
		})
		ts += 1000
	}
	// 60 ok, burst of 3, 300 ok (~5 min), burst of 4, 60 ok,
	// then a flagged (speedtest) failure that must NOT appear
	for i := 0; i < 60; i++ {
		add(true, false)
	}
	burst1Start := ts
	for i := 0; i < 3; i++ {
		add(false, false)
	}
	burst1End := ts - 1000
	for i := 0; i < 300; i++ {
		add(true, false)
	}
	burst2Start := ts
	for i := 0; i < 4; i++ {
		add(false, false)
	}
	for i := 0; i < 60; i++ {
		add(true, false)
	}
	add(false, true) // during speedtest: excluded
	add(true, false)
	if err := w.Flush(samples); err != nil {
		t.Fatal(err)
	}

	url := fmt.Sprintf("/api/lossbursts?target=%d&from=%d&to=%d", id, base-1000, ts+1000)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp lossBurstResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Bursts) != 2 {
		t.Fatalf("bursts = %d, want 2 (flagged failure excluded): %+v", len(resp.Bursts), resp.Bursts)
	}
	b1, b2 := resp.Bursts[0], resp.Bursts[1]
	if b1.StartedAt != burst1Start || b1.EndedAt != burst1End || b1.Lost != 3 {
		t.Fatalf("burst1 = %+v", b1)
	}
	if b2.Lost != 4 || b2.GapMs == nil {
		t.Fatalf("burst2 = %+v", b2)
	}
	// gap = burst2 start - burst1 end = 301s
	wantGap := burst2Start - burst1End
	if *b2.GapMs != wantGap {
		t.Fatalf("gap = %d, want %d", *b2.GapMs, wantGap)
	}
	if resp.MedianGapMs != wantGap {
		t.Fatalf("median gap = %d, want %d", resp.MedianGapMs, wantGap)
	}
	if resp.Lost != 7 {
		t.Fatalf("lost = %d, want 7 (flagged failure excluded)", resp.Lost)
	}
}

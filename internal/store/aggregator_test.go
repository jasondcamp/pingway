package store

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func insertSamples(t *testing.T, st *Store, samples []Sample) {
	t.Helper()
	w := NewWriter(st, testLogger())
	if err := w.Flush(samples); err != nil {
		t.Fatal(err)
	}
}

func TestAggregatorRollupAndRetention(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	id, err := st.CreateTarget(ctx, Target{Name: "t", Host: "10.0.0.1", Tier: 3, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	old := now - 100*3_600_000 // 100h ago: past 48h retention
	var samples []Sample
	// one old minute-bucket of 60 samples, 10 lost, plus during-speedtest
	// failures that must NOT be rolled up (self-inflicted loss)
	base := old - old%60_000
	for i := int64(0); i < 60; i++ {
		samples = append(samples, Sample{TargetID: id, TS: base + i*1000, RTTMicros: 1000 + i, Success: i >= 10})
	}
	for i := int64(0); i < 5; i++ {
		samples = append(samples, Sample{TargetID: id, TS: base + i*1000 + 500, Success: false, DuringSpeedtest: true})
	}
	// recent samples in the current (incomplete) minute must NOT roll up
	nowBase := now - now%60_000
	samples = append(samples, Sample{TargetID: id, TS: nowBase + 1, RTTMicros: 500, Success: true})
	insertSamples(t, st, samples)

	agg := NewAggregator(st, func() (int, int) { return 48, 30 }, testLogger())
	if err := agg.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// 1m rollup exists for the old bucket
	var sent, lost int64
	err = st.db.QueryRow(`SELECT sent, lost FROM ping_rollup_1m WHERE target_id = ? AND ts_bucket = ?`, id, base).Scan(&sent, &lost)
	if err != nil {
		t.Fatalf("rollup row missing: %v", err)
	}
	if sent != 60 || lost != 10 {
		t.Fatalf("sent/lost = %d/%d", sent, lost)
	}

	// 1h rollup exists
	hourBase := old - old%3_600_000
	var hsent int64
	if err := st.db.QueryRow(`SELECT sent FROM ping_rollup_1h WHERE target_id = ? AND ts_bucket = ?`, id, hourBase).Scan(&hsent); err != nil {
		t.Fatalf("1h rollup missing: %v", err)
	}

	// raw retention pruned the old samples (100h > 48h) but kept recent
	var rawOld, rawRecent int64
	st.db.QueryRow(`SELECT COUNT(*) FROM ping_samples WHERE ts < ?`, now-48*3_600_000).Scan(&rawOld)
	st.db.QueryRow(`SELECT COUNT(*) FROM ping_samples WHERE ts >= ?`, nowBase).Scan(&rawRecent)
	if rawOld != 0 {
		t.Fatalf("expected old raw samples pruned, %d remain", rawOld)
	}
	if rawRecent != 1 {
		t.Fatalf("recent sample should remain, got %d", rawRecent)
	}

	// current incomplete minute not rolled up
	var cur int64
	st.db.QueryRow(`SELECT COUNT(*) FROM ping_rollup_1m WHERE ts_bucket = ?`, nowBase).Scan(&cur)
	if cur != 0 {
		t.Fatal("incomplete minute must not be rolled up")
	}
}

func TestAggregatorIncrementalCursor(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	id, _ := st.CreateTarget(ctx, Target{Name: "t", Host: "10.0.0.2", Tier: 3, Enabled: true})

	now := time.Now().UnixMilli()
	m1 := now - 10*60_000
	m1 -= m1 % 60_000
	insertSamples(t, st, []Sample{{TargetID: id, TS: m1, RTTMicros: 100, Success: true}})

	agg := NewAggregator(st, func() (int, int) { return 48, 30 }, testLogger())
	if err := agg.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// insert a sample into an already-rolled window bucket + a newer one;
	// second pass must only process past the cursor (the old one is missed
	// by design — incremental, no rescans)
	m2 := now - 5*60_000
	m2 -= m2 % 60_000
	insertSamples(t, st, []Sample{{TargetID: id, TS: m2, RTTMicros: 200, Success: true}})
	if err := agg.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var n int64
	st.db.QueryRow(`SELECT COUNT(*) FROM ping_rollup_1m WHERE target_id = ?`, id).Scan(&n)
	if n != 1 {
		// m2 is after the first pass's cursor (cursor = current minute at
		// pass 1), so it is NOT rolled up. Only m1 exists.
		t.Fatalf("rollup rows = %d, want 1 (incremental cursor)", n)
	}

	// retention of 1m rollups
	veryOld := now - 40*86_400_000
	veryOld -= veryOld % 60_000
	if _, err := st.db.Exec(`INSERT INTO ping_rollup_1m (target_id, ts_bucket, sent, lost) VALUES (?, ?, 60, 0)`, id, veryOld); err != nil {
		t.Fatal(err)
	}
	if err := agg.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var oldCount int64
	st.db.QueryRow(`SELECT COUNT(*) FROM ping_rollup_1m WHERE ts_bucket = ?`, veryOld).Scan(&oldCount)
	if oldCount != 0 {
		t.Fatal("40-day-old 1m rollup should be pruned at 30d retention")
	}
}

package store

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

const (
	bucket1m = int64(60_000)
	bucket1h = int64(3_600_000)
)

// RetentionProvider returns the current retention knobs (UI-editable).
type RetentionProvider func() (rawHours, rollup1mDays int)

// Aggregator computes incremental 1m/1h rollups from raw samples and prunes
// old data. It runs every 5 minutes under the supervisor.
type Aggregator struct {
	store     *Store
	retention RetentionProvider
	interval  time.Duration
	log       *slog.Logger
}

func NewAggregator(s *Store, retention RetentionProvider, log *slog.Logger) *Aggregator {
	return &Aggregator{
		store:     s,
		retention: retention,
		interval:  5 * time.Minute,
		log:       log.With("component", "aggregator"),
	}
}

func (a *Aggregator) Run(ctx context.Context) error {
	// run once shortly after boot so restarts don't delay rollups
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := a.RunOnce(ctx); err != nil {
			a.log.Error("aggregation pass failed", "err", err)
		}
		timer.Reset(a.interval)
	}
}

// RunOnce performs one full rollup + retention pass.
func (a *Aggregator) RunOnce(ctx context.Context) error {
	targets, err := a.store.ListTargets(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, t := range targets {
		if err := a.rollupTarget(ctx, t.ID, "1m", bucket1m, now); err != nil {
			return fmt.Errorf("rollup 1m target %d: %w", t.ID, err)
		}
		if err := a.rollupTarget(ctx, t.ID, "1h", bucket1h, now); err != nil {
			return fmt.Errorf("rollup 1h target %d: %w", t.ID, err)
		}
	}
	return a.prune(ctx, now)
}

// rollupTarget rolls raw samples into complete buckets past the cursor.
// The cursor stores the first not-yet-rolled bucket start.
func (a *Aggregator) rollupTarget(ctx context.Context, targetID int64, res string, bucketMs, now int64) error {
	cursorKey := fmt.Sprintf("rollup_%s_cursor.%d", res, targetID)
	cursor := int64(0)
	if v, ok, err := a.store.GetSetting(ctx, cursorKey); err != nil {
		return err
	} else if ok {
		cursor, _ = strconv.ParseInt(v, 10, 64)
	}
	if cursor == 0 {
		// first run: start at the oldest sample's bucket
		var minTS *int64
		if err := a.store.db.QueryRowContext(ctx,
			`SELECT MIN(ts) FROM ping_samples WHERE target_id = ?`, targetID).Scan(&minTS); err != nil {
			return err
		}
		if minTS == nil {
			return nil // no data yet
		}
		cursor = *minTS - *minTS%bucketMs
	}

	// only roll fully complete buckets
	currentBucket := now - now%bucketMs
	if cursor >= currentBucket {
		return nil
	}

	samples, err := a.store.QuerySamples(ctx, targetID, cursor, currentBucket-1)
	if err != nil {
		return err
	}
	// exclude during-speedtest samples: their loss (and inflated RTT) is
	// self-inflicted line saturation, not path health. Loaded-latency data
	// lives in speedtest_results; the raw flag survives in ping_samples
	// for the retention window.
	filtered := samples[:0]
	for _, s := range samples {
		if !s.DuringSpeedtest {
			filtered = append(filtered, s)
		}
	}
	samples = filtered
	if len(samples) == 0 {
		// nothing in the window; advance cursor so we don't rescan forever
		return a.store.SetSetting(ctx, cursorKey, strconv.FormatInt(currentBucket, 10))
	}

	keys, buckets := BucketSamples(samples, bucketMs)
	table := "ping_rollup_" + res
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (target_id, ts_bucket, sent, lost, rtt_avg_us, rtt_min_us, rtt_max_us, rtt_p95_us, jitter_us)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(target_id, ts_bucket) DO UPDATE SET
		   sent = excluded.sent, lost = excluded.lost, rtt_avg_us = excluded.rtt_avg_us,
		   rtt_min_us = excluded.rtt_min_us, rtt_max_us = excluded.rtt_max_us,
		   rtt_p95_us = excluded.rtt_p95_us, jitter_us = excluded.jitter_us`, table))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		st := ComputeBucket(buckets[k])
		var avg, mn, mx, p95, jit any
		if st.Sent > st.Lost {
			avg, mn, mx, p95, jit = st.RTTAvgUs, st.RTTMinUs, st.RTTMaxUs, st.RTTP95Us, st.JitterUs
		}
		if _, err := stmt.ExecContext(ctx, targetID, k, st.Sent, st.Lost, avg, mn, mx, p95, jit); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		cursorKey, strconv.FormatInt(currentBucket, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// prune enforces retention: raw samples past rawHours (only up to what has
// been rolled into 1h buckets), 1m rollups past rollup1mDays.
func (a *Aggregator) prune(ctx context.Context, now int64) error {
	rawHours, rollup1mDays := a.retention()
	rawCutoff := now - int64(rawHours)*3_600_000
	res, err := a.store.db.ExecContext(ctx, `DELETE FROM ping_samples WHERE ts < ?`, rawCutoff)
	if err != nil {
		return fmt.Errorf("prune raw: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		a.log.Debug("pruned raw samples", "rows", n)
	}
	cutoff1m := now - int64(rollup1mDays)*86_400_000
	if _, err := a.store.db.ExecContext(ctx, `DELETE FROM ping_rollup_1m WHERE ts_bucket < ?`, cutoff1m); err != nil {
		return fmt.Errorf("prune 1m rollups: %w", err)
	}
	// call-probe seconds follow raw retention; freeze events (the
	// evidence) follow the long rollup retention
	return a.store.PruneCallprobe(ctx, rawCutoff, cutoff1m)
}

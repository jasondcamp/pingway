package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Reflector struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

type CallprobeBucket struct {
	ReflectorID     int64 `json:"reflector_id"`
	TS              int64 `json:"ts"`
	Sent            int64 `json:"sent"`
	Lost            int64 `json:"lost"`
	RTTAvgUs        int64 `json:"rtt_avg_us"`
	JitterUs        int64 `json:"jitter_us"`
	MOSx100         int64 `json:"mos_x100"`
	DuringSpeedtest bool  `json:"during_speedtest,omitempty"`
}

type FreezeEventRow struct {
	ID              int64 `json:"id"`
	ReflectorID     int64 `json:"reflector_id"`
	StartedAt       int64 `json:"started_at"`
	DurationMs      int64 `json:"duration_ms"`
	PacketsLost     int64 `json:"packets_lost"`
	DuringSpeedtest bool  `json:"during_speedtest,omitempty"`
}

// --- reflectors ---

func (s *Store) ListReflectors(ctx context.Context) ([]Reflector, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, host, sort_order, enabled, created_at FROM reflectors ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list reflectors: %w", err)
	}
	defer rows.Close()
	var out []Reflector
	for rows.Next() {
		var r Reflector
		if err := rows.Scan(&r.ID, &r.Name, &r.Host, &r.SortOrder, &r.Enabled, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertReflectorByHost applies YAML/env reflector config at boot.
func (s *Store) UpsertReflectorByHost(ctx context.Context, r Reflector) error {
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reflectors (name, host, sort_order, enabled, created_at)
		 VALUES (?, ?, ?, 1, ?)
		 ON CONFLICT(host) DO UPDATE SET name = excluded.name, sort_order = excluded.sort_order`,
		r.Name, r.Host, r.SortOrder, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert reflector %s: %w", r.Host, err)
	}
	return nil
}

// PruneReflectorsNotIn disables reflectors no longer present in config so
// removed droplets stop being probed (history is kept).
func (s *Store) PruneReflectorsNotIn(ctx context.Context, hosts []string) error {
	if len(hosts) == 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE reflectors SET enabled = 0`)
		return err
	}
	q := `UPDATE reflectors SET enabled = 0 WHERE host NOT IN (?` + repeat(",?", len(hosts)-1) + `)`
	args := make([]any, len(hosts))
	for i, h := range hosts {
		args[i] = h
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// --- probe buckets + freezes ---

func (s *Store) InsertCallprobeBucket(ctx context.Context, b CallprobeBucket) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO callprobe_1s
		 (reflector_id, ts, sent, lost, rtt_avg_us, jitter_us, mos_x100, during_speedtest)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ReflectorID, b.TS, b.Sent, b.Lost, b.RTTAvgUs, b.JitterUs, b.MOSx100, b.DuringSpeedtest)
	if err != nil {
		return fmt.Errorf("insert callprobe bucket: %w", err)
	}
	return nil
}

func (s *Store) InsertFreezeEvent(ctx context.Context, f FreezeEventRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO freeze_events (reflector_id, started_at, duration_ms, packets_lost, during_speedtest)
		 VALUES (?, ?, ?, ?, ?)`,
		f.ReflectorID, f.StartedAt, f.DurationMs, f.PacketsLost, f.DuringSpeedtest)
	if err != nil {
		return fmt.Errorf("insert freeze event: %w", err)
	}
	return nil
}

// QueryCallprobeHistory returns per-second buckets, decimated to at most
// maxPoints per reflector via time-bucket averaging when the range is
// large.
func (s *Store) QueryCallprobeHistory(ctx context.Context, from, to int64, maxPoints int) ([]CallprobeBucket, error) {
	if maxPoints <= 0 {
		maxPoints = 1800
	}
	bucketMs := (to - from) / int64(maxPoints)
	if bucketMs < 1000 {
		bucketMs = 1000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT reflector_id, (ts / ?) * ? AS tb,
		        SUM(sent), SUM(lost),
		        CAST(AVG(rtt_avg_us) AS INTEGER),
		        CAST(AVG(jitter_us) AS INTEGER),
		        MIN(mos_x100)
		 FROM callprobe_1s
		 WHERE ts >= ? AND ts <= ?
		 GROUP BY reflector_id, tb ORDER BY tb`,
		bucketMs, bucketMs, from, to)
	if err != nil {
		return nil, fmt.Errorf("query callprobe history: %w", err)
	}
	defer rows.Close()
	var out []CallprobeBucket
	for rows.Next() {
		var b CallprobeBucket
		var rtt, jit, mos sql.NullInt64
		if err := rows.Scan(&b.ReflectorID, &b.TS, &b.Sent, &b.Lost, &rtt, &jit, &mos); err != nil {
			return nil, err
		}
		b.RTTAvgUs, b.JitterUs, b.MOSx100 = rtt.Int64, jit.Int64, mos.Int64
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListFreezeEvents returns freezes in range, newest first, excluding
// speedtest-flagged ones (self-inflicted congestion is not evidence).
func (s *Store) ListFreezeEvents(ctx context.Context, from, to int64, minDurationMs int64) ([]FreezeEventRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, reflector_id, started_at, duration_ms, packets_lost, during_speedtest
		 FROM freeze_events
		 WHERE started_at >= ? AND started_at <= ? AND duration_ms >= ? AND during_speedtest = 0
		 ORDER BY started_at DESC LIMIT 500`, from, to, minDurationMs)
	if err != nil {
		return nil, fmt.Errorf("list freeze events: %w", err)
	}
	defer rows.Close()
	var out []FreezeEventRow
	for rows.Next() {
		var f FreezeEventRow
		if err := rows.Scan(&f.ID, &f.ReflectorID, &f.StartedAt, &f.DurationMs, &f.PacketsLost, &f.DuringSpeedtest); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PruneCallprobe deletes probe rows older than the raw retention window
// (freeze events are kept for the rollup retention window — they are the
// evidence).
func (s *Store) PruneCallprobe(ctx context.Context, rawCutoffMs, eventCutoffMs int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM callprobe_1s WHERE ts < ?`, rawCutoffMs); err != nil {
		return fmt.Errorf("prune callprobe_1s: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM freeze_events WHERE started_at < ?`, eventCutoffMs); err != nil {
		return fmt.Errorf("prune freeze_events: %w", err)
	}
	return nil
}

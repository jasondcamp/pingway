// Package store owns all SQLite access. Ping samples are written only by
// the Writer (single-writer pattern); everything else uses the shared pool.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"pingway.net/pingway/migrations"
)

const schemaVersionKey = "schema_version"

type Store struct {
	db *sql.DB
}

type Target struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Tier      int    `json:"tier"`
	SortOrder int    `json:"sort_order"`
	IntervalMs int   `json:"interval_ms"` // 0 = global default
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Sample is one ping result. RTTMicros is meaningful only when Success.
type Sample struct {
	TargetID        int64 `json:"target_id"`
	TS              int64 `json:"ts"`
	RTTMicros       int64 `json:"rtt_us"`
	Success         bool  `json:"success"`
	DuringSpeedtest bool  `json:"during_speedtest,omitempty"`
}

type OutageEvent struct {
	ID         int64  `json:"id"`
	TargetID   int64  `json:"target_id"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    *int64 `json:"ended_at"`
	DurationMs *int64 `json:"duration_ms"`
}

type SpeedTestRow struct {
	ID              int64   `json:"id"`
	Engine          string  `json:"engine"`
	ServerName      string  `json:"server_name"`
	ServerID        string  `json:"server_id"`
	DownloadBps     float64 `json:"download_bps"`
	UploadBps       float64 `json:"upload_bps"`
	LatencyMs       float64 `json:"latency_ms"`
	LoadedLatencyMs float64 `json:"loaded_latency_ms"`
	PacketLoss      float64 `json:"packet_loss"`
	RanAt           int64   `json:"ran_at"`
	DurationMs      int64   `json:"duration_ms"`
	Error           string  `json:"error"`
}

type RollupRow struct {
	TargetID int64 `json:"target_id"`
	TSBucket int64 `json:"ts"`
	Sent     int64 `json:"sent"`
	Lost     int64 `json:"lost"`
	RTTAvgUs int64 `json:"rtt_avg_us"`
	RTTMinUs int64 `json:"rtt_min_us"`
	RTTMaxUs int64 `json:"rtt_max_us"`
	RTTP95Us int64 `json:"rtt_p95_us"`
	JitterUs int64 `json:"jitter_us"`
}

// Open opens (creating if needed) the SQLite database at path and applies
// pending migrations. WAL mode and NORMAL synchronous are set per spec.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// modernc sqlite serializes writes; a small pool covers concurrent readers.
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate applies embedded numbered migrations newer than the recorded
// schema version, in order, each in its own transaction.
func (s *Store) migrate() error {
	// settings must exist before we can read the version; 0001 creates it,
	// so bootstrap it here too (idempotent).
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("bootstrap settings table: %w", err)
	}
	version := 0
	if v, ok, err := s.GetSetting(context.Background(), schemaVersionKey); err != nil {
		return err
	} else if ok {
		version, err = strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("bad schema_version %q: %w", v, err)
		}
	}

	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		numStr, _, ok := strings.Cut(name, "_")
		if !ok {
			return fmt.Errorf("bad migration filename %q", name)
		}
		num, err := strconv.Atoi(numStr)
		if err != nil {
			return fmt.Errorf("bad migration filename %q: %w", name, err)
		}
		if num <= version {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			schemaVersionKey, strconv.Itoa(num)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		version = num
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying pool for read queries by other components.
func (s *Store) DB() *sql.DB { return s.db }

// --- targets ---

func (s *Store) ListTargets(ctx context.Context) ([]Target, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, host, tier, sort_order, enabled, created_at, interval_ms
		 FROM targets ORDER BY tier, sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Name, &t.Host, &t.Tier, &t.SortOrder, &t.Enabled, &t.CreatedAt, &t.IntervalMs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTarget(ctx context.Context, id int64) (*Target, error) {
	var t Target
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, host, tier, sort_order, enabled, created_at, interval_ms FROM targets WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Host, &t.Tier, &t.SortOrder, &t.Enabled, &t.CreatedAt, &t.IntervalMs)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get target %d: %w", id, err)
	}
	return &t, nil
}

func (s *Store) CreateTarget(ctx context.Context, t Target) (int64, error) {
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO targets (name, host, tier, sort_order, enabled, created_at, interval_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Host, t.Tier, t.SortOrder, t.Enabled, t.CreatedAt, t.IntervalMs)
	if err != nil {
		return 0, fmt.Errorf("create target: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateTarget(ctx context.Context, t Target) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE targets SET name = ?, host = ?, tier = ?, sort_order = ?, enabled = ?, interval_ms = ? WHERE id = ?`,
		t.Name, t.Host, t.Tier, t.SortOrder, t.Enabled, t.IntervalMs, t.ID)
	if err != nil {
		return fmt.Errorf("update target %d: %w", t.ID, err)
	}
	return nil
}

// DeleteTarget removes the target row but keeps historical samples,
// rollups, and outage events for it.
func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete target %d: %w", id, err)
	}
	return nil
}

// UpsertTargetByHost inserts the target if its host is new, otherwise
// updates name/tier (used to apply YAML/env config at boot).
func (s *Store) UpsertTargetByHost(ctx context.Context, t Target) error {
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO targets (name, host, tier, sort_order, enabled, created_at)
		 VALUES (?, ?, ?, ?, 1, ?)
		 ON CONFLICT(host) DO UPDATE SET name = excluded.name, tier = excluded.tier, sort_order = excluded.sort_order`,
		t.Name, t.Host, t.Tier, t.SortOrder, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert target %s: %w", t.Host, err)
	}
	return nil
}

func (s *Store) CountTargets(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM targets`).Scan(&n)
	return n, err
}

// --- settings ---

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get setting %s: %w", key, err)
	}
	return v, true, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// --- outages ---

func (s *Store) OpenOutage(ctx context.Context, targetID, startedAt int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO outage_events (target_id, started_at) VALUES (?, ?)`, targetID, startedAt)
	if err != nil {
		return 0, fmt.Errorf("open outage: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) CloseOutage(ctx context.Context, id, endedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE outage_events SET ended_at = ?, duration_ms = ? - started_at WHERE id = ?`,
		endedAt, endedAt, id)
	if err != nil {
		return fmt.Errorf("close outage %d: %w", id, err)
	}
	return nil
}

// OpenOutages returns all outage events that have not ended, keyed by
// target. Used by the startup reconciliation pass and the detector to
// resume open events across restarts.
func (s *Store) OpenOutages(ctx context.Context) ([]OutageEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, target_id, started_at, ended_at, duration_ms FROM outage_events WHERE ended_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("open outages: %w", err)
	}
	defer rows.Close()
	return scanOutages(rows)
}

func (s *Store) ListOutages(ctx context.Context, from, to int64, targetID int64) ([]OutageEvent, error) {
	q := `SELECT id, target_id, started_at, ended_at, duration_ms FROM outage_events
	      WHERE started_at < ? AND (ended_at IS NULL OR ended_at > ?)`
	args := []any{to, from}
	if targetID > 0 {
		q += ` AND target_id = ?`
		args = append(args, targetID)
	}
	q += ` ORDER BY started_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list outages: %w", err)
	}
	defer rows.Close()
	return scanOutages(rows)
}

func scanOutages(rows *sql.Rows) ([]OutageEvent, error) {
	var out []OutageEvent
	for rows.Next() {
		var e OutageEvent
		if err := rows.Scan(&e.ID, &e.TargetID, &e.StartedAt, &e.EndedAt, &e.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- speedtests ---

func (s *Store) InsertSpeedTest(ctx context.Context, r SpeedTestRow) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO speedtest_results
		 (engine, server_name, server_id, download_bps, upload_bps, latency_ms,
		  loaded_latency_ms, packet_loss, ran_at, duration_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Engine, r.ServerName, r.ServerID, r.DownloadBps, r.UploadBps, r.LatencyMs,
		r.LoadedLatencyMs, r.PacketLoss, r.RanAt, r.DurationMs, r.Error)
	if err != nil {
		return 0, fmt.Errorf("insert speedtest: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) ListSpeedTests(ctx context.Context, from, to int64) ([]SpeedTestRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, engine, server_name, server_id, download_bps, upload_bps, latency_ms,
		        loaded_latency_ms, packet_loss, ran_at, duration_ms, error
		 FROM speedtest_results WHERE ran_at >= ? AND ran_at <= ? ORDER BY ran_at`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list speedtests: %w", err)
	}
	defer rows.Close()
	var out []SpeedTestRow
	for rows.Next() {
		var r SpeedTestRow
		if err := rows.Scan(&r.ID, &r.Engine, &r.ServerName, &r.ServerID, &r.DownloadBps, &r.UploadBps,
			&r.LatencyMs, &r.LoadedLatencyMs, &r.PacketLoss, &r.RanAt, &r.DurationMs, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastSpeedTestTime returns the ran_at of the most recent speed test row
// of any kind (success, failure, or skip marker), or 0 if none exist.
// Used by the scheduler to survive restarts without resetting its clock.
func (s *Store) LastSpeedTestTime(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(ran_at) FROM speedtest_results`).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("last speedtest time: %w", err)
	}
	return ts.Int64, nil
}

// LastSpeedTest returns the most recent successful result, falling back to
// the most recent result of any kind. Nil if none exist.
func (s *Store) LastSpeedTest(ctx context.Context) (*SpeedTestRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, engine, server_name, server_id, download_bps, upload_bps, latency_ms,
		        loaded_latency_ms, packet_loss, ran_at, duration_ms, error
		 FROM speedtest_results ORDER BY ran_at DESC LIMIT 50`)
	if err != nil {
		return nil, fmt.Errorf("last speedtest: %w", err)
	}
	defer rows.Close()
	var newest *SpeedTestRow
	for rows.Next() {
		var r SpeedTestRow
		if err := rows.Scan(&r.ID, &r.Engine, &r.ServerName, &r.ServerID, &r.DownloadBps, &r.UploadBps,
			&r.LatencyMs, &r.LoadedLatencyMs, &r.PacketLoss, &r.RanAt, &r.DurationMs, &r.Error); err != nil {
			return nil, err
		}
		if r.Error == "" {
			return &r, nil
		}
		if newest == nil {
			newest = &r
		}
	}
	return newest, rows.Err()
}

// --- samples (reads; writes go through Writer) ---

func (s *Store) QuerySamples(ctx context.Context, targetID, from, to int64) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT target_id, ts, rtt_us, success, during_speedtest FROM ping_samples
		 WHERE target_id = ? AND ts >= ? AND ts <= ? ORDER BY ts`, targetID, from, to)
	if err != nil {
		return nil, fmt.Errorf("query samples: %w", err)
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var sm Sample
		var rtt sql.NullInt64
		if err := rows.Scan(&sm.TargetID, &sm.TS, &rtt, &sm.Success, &sm.DuringSpeedtest); err != nil {
			return nil, err
		}
		if rtt.Valid {
			sm.RTTMicros = rtt.Int64
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

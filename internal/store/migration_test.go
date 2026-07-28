package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestMigration0004PreservesTargets builds a schema-v3 database (host
// UNIQUE inline) and verifies the 0004 table rebuild keeps rows and ids —
// samples/rollups/outages reference target_id — and relaxes uniqueness to
// (host, probe).
func TestMigration0004PreservesTargets(t *testing.T) {
	path := t.TempDir() + "/old.db"
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE targets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			host TEXT NOT NULL UNIQUE,
			tier INTEGER NOT NULL DEFAULT 3,
			sort_order INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			interval_ms INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO targets (id, name, host, tier, sort_order, enabled, created_at, interval_ms)
		 VALUES (5, 'CF', '1.1.1.1', 3, 0, 1, 123, 0), (9, 'GW', '10.0.0.1', 1, 1, 0, 456, 10000)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO settings (key, value) VALUES ('schema_version', '3')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	targets, err := st.ListTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	byID := map[int64]Target{}
	for _, tr := range targets {
		byID[tr.ID] = tr
	}
	cf, gw := byID[5], byID[9]
	if cf.Host != "1.1.1.1" || cf.Probe != ProbeICMP || !cf.Enabled || cf.CreatedAt != 123 {
		t.Fatalf("id 5 mangled: %+v", cf)
	}
	if gw.Host != "10.0.0.1" || gw.Probe != ProbeICMP || gw.Enabled || gw.IntervalMs != 10000 {
		t.Fatalf("id 9 mangled: %+v", gw)
	}

	// same host, different probe is now allowed…
	if _, err := st.CreateTarget(ctx, Target{Name: "CF DNS", Host: "1.1.1.1", Tier: 3, Probe: ProbeDNS, Enabled: true}); err != nil {
		t.Fatalf("same host different probe rejected: %v", err)
	}
	// …but a duplicate (host, probe) pair is not
	if _, err := st.CreateTarget(ctx, Target{Name: "dupe", Host: "1.1.1.1", Tier: 3, Enabled: true}); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate (host, probe) accepted: %v", err)
	}
}

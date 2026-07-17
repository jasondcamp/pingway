CREATE TABLE IF NOT EXISTS targets (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	host       TEXT NOT NULL UNIQUE,
	tier       INTEGER NOT NULL DEFAULT 3,
	sort_order INTEGER NOT NULL DEFAULT 0,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS ping_samples (
	target_id        INTEGER NOT NULL,
	ts               INTEGER NOT NULL,
	rtt_us           INTEGER,
	success          INTEGER NOT NULL,
	during_speedtest INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ping_samples_target_ts ON ping_samples(target_id, ts);

CREATE TABLE IF NOT EXISTS ping_rollup_1m (
	target_id  INTEGER NOT NULL,
	ts_bucket  INTEGER NOT NULL,
	sent       INTEGER NOT NULL,
	lost       INTEGER NOT NULL,
	rtt_avg_us INTEGER,
	rtt_min_us INTEGER,
	rtt_max_us INTEGER,
	rtt_p95_us INTEGER,
	jitter_us  INTEGER,
	PRIMARY KEY (target_id, ts_bucket)
);

CREATE TABLE IF NOT EXISTS ping_rollup_1h (
	target_id  INTEGER NOT NULL,
	ts_bucket  INTEGER NOT NULL,
	sent       INTEGER NOT NULL,
	lost       INTEGER NOT NULL,
	rtt_avg_us INTEGER,
	rtt_min_us INTEGER,
	rtt_max_us INTEGER,
	rtt_p95_us INTEGER,
	jitter_us  INTEGER,
	PRIMARY KEY (target_id, ts_bucket)
);

CREATE TABLE IF NOT EXISTS speedtest_results (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	engine            TEXT NOT NULL,
	server_name       TEXT NOT NULL DEFAULT '',
	server_id         TEXT NOT NULL DEFAULT '',
	download_bps      REAL NOT NULL DEFAULT 0,
	upload_bps        REAL NOT NULL DEFAULT 0,
	latency_ms        REAL NOT NULL DEFAULT 0,
	loaded_latency_ms REAL NOT NULL DEFAULT 0,
	packet_loss       REAL NOT NULL DEFAULT 0,
	ran_at            INTEGER NOT NULL,
	duration_ms       INTEGER NOT NULL DEFAULT 0,
	error             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_speedtest_ran_at ON speedtest_results(ran_at);

CREATE TABLE IF NOT EXISTS outage_events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	target_id   INTEGER NOT NULL,
	started_at  INTEGER NOT NULL,
	ended_at    INTEGER,
	duration_ms INTEGER
);
CREATE INDEX IF NOT EXISTS idx_outage_target_started ON outage_events(target_id, started_at);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

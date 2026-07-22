-- Synthetic call probe: RTP-shaped UDP stream to off-network reflectors.
-- Measures the thing users actually complain about (call freezes) in the
-- ISP's own quality units (MOS).

CREATE TABLE IF NOT EXISTS reflectors (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	host       TEXT NOT NULL UNIQUE, -- "host:port"
	sort_order INTEGER NOT NULL DEFAULT 0,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

-- One row per reflector per second of probe traffic (50 packets each).
-- mos_x100 is the rolling-window Mean Opinion Score * 100.
CREATE TABLE IF NOT EXISTS callprobe_1s (
	reflector_id     INTEGER NOT NULL,
	ts               INTEGER NOT NULL, -- ms, start of second
	sent             INTEGER NOT NULL,
	lost             INTEGER NOT NULL,
	rtt_avg_us       INTEGER,
	jitter_us        INTEGER,
	mos_x100         INTEGER,
	during_speedtest INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (reflector_id, ts)
);
CREATE INDEX IF NOT EXISTS idx_callprobe_1s_ts ON callprobe_1s(ts);

-- A freeze: N consecutive lost probe packets = a visible call stall.
CREATE TABLE IF NOT EXISTS freeze_events (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	reflector_id     INTEGER NOT NULL,
	started_at       INTEGER NOT NULL, -- ms
	duration_ms      INTEGER NOT NULL,
	packets_lost     INTEGER NOT NULL,
	during_speedtest INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_freeze_reflector_started ON freeze_events(reflector_id, started_at);

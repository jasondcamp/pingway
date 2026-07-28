-- Per-target probe kind: 'icmp' (echo RTT, the original behavior) or
-- 'dns' (a real recursive DNS query over UDP :53, timing the answer).
--
-- Uniqueness relaxes from host to (host, probe) so the same server can be
-- monitored both ways (e.g. ping 1.1.1.1 and DNS-query 1.1.1.1). The
-- original UNIQUE was an inline column constraint, so the table is rebuilt;
-- ids are preserved because samples/rollups/outages reference target_id.
CREATE TABLE targets_new (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	host        TEXT NOT NULL,
	tier        INTEGER NOT NULL DEFAULT 3,
	sort_order  INTEGER NOT NULL DEFAULT 0,
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_at  INTEGER NOT NULL,
	interval_ms INTEGER NOT NULL DEFAULT 0,
	probe       TEXT NOT NULL DEFAULT 'icmp'
);
INSERT INTO targets_new (id, name, host, tier, sort_order, enabled, created_at, interval_ms)
	SELECT id, name, host, tier, sort_order, enabled, created_at, interval_ms FROM targets;
DROP TABLE targets;
ALTER TABLE targets_new RENAME TO targets;
CREATE UNIQUE INDEX idx_targets_host_probe ON targets(host, probe);

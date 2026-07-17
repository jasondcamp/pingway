-- Per-target ping interval override (ms). 0 = use the global ping
-- interval. Lets low-rate targets (e.g. a WAN hairpin IP behind an
-- ICMP rate limiter) be probed gently without slowing everything else.
ALTER TABLE targets ADD COLUMN interval_ms INTEGER NOT NULL DEFAULT 0;

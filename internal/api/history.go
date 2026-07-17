package api

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"time"

	"pingway.net/pingway/internal/store"
)

// PickResolution auto-selects a resolution for a time range: raw up to 2h,
// 1m up to 48h, 1h beyond. Exported for the golden tests.
func PickResolution(from, to int64) string {
	span := time.Duration(to-from) * time.Millisecond
	switch {
	case span <= 2*time.Hour:
		return "raw"
	case span <= 48*time.Hour:
		return "1m"
	default:
		return "1h"
	}
}

type pingPoint struct {
	TS       int64    `json:"ts"`
	RTTAvgUs *int64   `json:"rtt_avg_us"`
	RTTMinUs *int64   `json:"rtt_min_us,omitempty"`
	RTTMaxUs *int64   `json:"rtt_max_us,omitempty"`
	RTTP95Us *int64   `json:"rtt_p95_us,omitempty"`
	JitterUs *int64   `json:"jitter_us,omitempty"`
	Sent     int64    `json:"sent"`
	Lost     int64    `json:"lost"`
	LossPct  float64  `json:"loss_pct"`
}

type pingResponse struct {
	TargetID   int64       `json:"target_id"`
	Resolution string      `json:"resolution"`
	From       int64       `json:"from"`
	To         int64       `json:"to"`
	Points     []pingPoint `json:"points"`
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(r.URL.Query().Get("target"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "target query param is required")
		return
	}
	from, to := queryRange(r, time.Hour)
	if to <= from {
		writeErr(w, http.StatusBadRequest, "to must be after from")
		return
	}
	res := r.URL.Query().Get("resolution")
	if res == "" {
		res = PickResolution(from, to)
	}

	var points []pingPoint
	switch res {
	case "raw":
		samples, err := s.store.QuerySamples(r.Context(), targetID, from, to)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		points = make([]pingPoint, 0, len(samples))
		for _, sm := range samples {
			// during-speedtest failures are self-inflicted saturation
			// drops, not path loss; successes keep their (loaded) RTT
			if sm.DuringSpeedtest && !sm.Success {
				continue
			}
			p := pingPoint{TS: sm.TS, Sent: 1}
			if sm.Success {
				rtt := sm.RTTMicros
				p.RTTAvgUs = &rtt
			} else {
				p.Lost = 1
				p.LossPct = 100
			}
			points = append(points, p)
		}
	case "1m", "1h":
		table := "ping_rollup_1m"
		if res == "1h" {
			table = "ping_rollup_1h"
		}
		rows, err := s.store.DB().QueryContext(r.Context(),
			`SELECT ts_bucket, sent, lost, rtt_avg_us, rtt_min_us, rtt_max_us, rtt_p95_us, jitter_us
			 FROM `+table+` WHERE target_id = ? AND ts_bucket >= ? AND ts_bucket <= ? ORDER BY ts_bucket`,
			targetID, from, to)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		points = []pingPoint{}
		for rows.Next() {
			var p pingPoint
			var avg, mn, mx, p95, jit sql.NullInt64
			if err := rows.Scan(&p.TS, &p.Sent, &p.Lost, &avg, &mn, &mx, &p95, &jit); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if avg.Valid {
				p.RTTAvgUs = &avg.Int64
			}
			if mn.Valid {
				p.RTTMinUs = &mn.Int64
			}
			if mx.Valid {
				p.RTTMaxUs = &mx.Int64
			}
			if p95.Valid {
				p.RTTP95Us = &p95.Int64
			}
			if jit.Valid {
				p.JitterUs = &jit.Int64
			}
			if p.Sent > 0 {
				p.LossPct = 100 * float64(p.Lost) / float64(p.Sent)
			}
			points = append(points, p)
		}
		if err := rows.Err(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "resolution must be raw, 1m, or 1h")
		return
	}

	writeJSON(w, http.StatusOK, pingResponse{
		TargetID: targetID, Resolution: res, From: from, To: to, Points: points,
	})
}

// --- summary ---

type targetSummary struct {
	TargetID     int64   `json:"target_id"`
	Name         string  `json:"name"`
	Tier         int     `json:"tier"`
	UptimePct    float64 `json:"uptime_pct"`
	RTTAvgUs     int64   `json:"rtt_avg_us"`
	RTTP95Us     int64   `json:"rtt_p95_us"`
	Sent         int64   `json:"sent"`
	Lost         int64   `json:"lost"`
	OutageCount  int64   `json:"outage_count"`
	OutageMs     int64   `json:"outage_total_ms"`
}

type speedSummary struct {
	Count       int64   `json:"count"`
	DownMinBps  float64 `json:"down_min_bps"`
	DownAvgBps  float64 `json:"down_avg_bps"`
	DownMaxBps  float64 `json:"down_max_bps"`
	UpMinBps    float64 `json:"up_min_bps"`
	UpAvgBps    float64 `json:"up_avg_bps"`
	UpMaxBps    float64 `json:"up_max_bps"`
	LatencyAvgMs float64 `json:"latency_avg_ms"`
}

type summaryResponse struct {
	Range          string          `json:"range"`
	From           int64           `json:"from"`
	To             int64           `json:"to"`
	Targets        []targetSummary `json:"targets"`
	Speedtest      *speedSummary   `json:"speedtest"`
	InternetUptimePct float64      `json:"internet_uptime_pct"`
	InternetOutages   int64        `json:"internet_outage_count"`
	InternetOutageMs  int64        `json:"internet_outage_total_ms"`
}

var summaryRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	// explicit from/to (unix ms) wins; otherwise a named relative range
	var from, to int64
	rng := r.URL.Query().Get("range")
	if r.URL.Query().Get("from") != "" || r.URL.Query().Get("to") != "" {
		from, to = queryRange(r, 24*time.Hour)
		if to <= from {
			writeErr(w, http.StatusBadRequest, "to must be after from")
			return
		}
		rng = "custom"
	} else {
		if rng == "" {
			rng = "24h"
		}
		span, ok := summaryRanges[rng]
		if !ok {
			writeErr(w, http.StatusBadRequest, "range must be one of 1h, 6h, 24h, 7d, 30d (or pass from/to)")
			return
		}
		to = time.Now().UnixMilli()
		from = to - span.Milliseconds()
	}
	ctx := r.Context()

	targets, err := s.store.ListTargets(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := summaryResponse{Range: rng, From: from, To: to, Targets: []targetSummary{}}

	// choose source table by span, same thresholds as PickResolution
	res := PickResolution(from, to)
	for _, t := range targets {
		ts := targetSummary{TargetID: t.ID, Name: t.Name, Tier: t.Tier}
		var sent, lost sql.NullInt64
		var avg, p95 sql.NullFloat64
		switch res {
		case "raw":
			// during_speedtest samples excluded: self-inflicted saturation
			// must not count against uptime or skew latency stats
			err = s.store.DB().QueryRowContext(ctx,
				`SELECT COUNT(*), SUM(success = 0), AVG(CASE WHEN success = 1 THEN rtt_us END)
				 FROM ping_samples WHERE target_id = ? AND ts >= ? AND ts <= ? AND during_speedtest = 0`,
				t.ID, from, to).Scan(&sent, &lost, &avg)
			if err == nil && sent.Int64 > 0 {
				// p95 over raw successes
				var v sql.NullInt64
				offset := int64(float64(sent.Int64-lost.Int64) * 0.95)
				qerr := s.store.DB().QueryRowContext(ctx,
					`SELECT rtt_us FROM ping_samples WHERE target_id = ? AND ts >= ? AND ts <= ?
					   AND success = 1 AND during_speedtest = 0
					 ORDER BY rtt_us LIMIT 1 OFFSET ?`, t.ID, from, to, offset).Scan(&v)
				if qerr == nil && v.Valid {
					p95 = sql.NullFloat64{Float64: float64(v.Int64), Valid: true}
				}
			}
		default:
			table := "ping_rollup_1m"
			if res == "1h" {
				table = "ping_rollup_1h"
			}
			err = s.store.DB().QueryRowContext(ctx,
				`SELECT SUM(sent), SUM(lost), SUM(rtt_avg_us * (sent - lost)) / NULLIF(SUM(sent - lost), 0),
				        MAX(rtt_p95_us)
				 FROM `+table+` WHERE target_id = ? AND ts_bucket >= ? AND ts_bucket <= ?`,
				t.ID, from, to).Scan(&sent, &lost, &avg, &p95)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ts.Sent = sent.Int64
		ts.Lost = lost.Int64
		if ts.Sent > 0 {
			ts.UptimePct = 100 * float64(ts.Sent-ts.Lost) / float64(ts.Sent)
		}
		if avg.Valid {
			ts.RTTAvgUs = int64(avg.Float64)
		}
		if p95.Valid {
			ts.RTTP95Us = int64(p95.Float64)
		}

		// outage count + clamped total duration within the window
		events, err := s.store.ListOutages(ctx, from, to, t.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, e := range events {
			ts.OutageCount++
			ts.OutageMs += clampedOutageMs(e, from, to)
		}
		resp.Targets = append(resp.Targets, ts)
	}

	// internet-level: intervals where ALL tier-3 targets were down
	resp.InternetUptimePct, resp.InternetOutages, resp.InternetOutageMs =
		s.internetOutageStats(ctx, targets, from, to)

	// speed test min/avg/max
	var sc sql.NullInt64
	var dmin, davg, dmax, umin, uavg, umax, lavg sql.NullFloat64
	err = s.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(download_bps), AVG(download_bps), MAX(download_bps),
		        MIN(upload_bps), AVG(upload_bps), MAX(upload_bps), AVG(latency_ms)
		 FROM speedtest_results WHERE ran_at >= ? AND ran_at <= ? AND error = ''`, from, to).
		Scan(&sc, &dmin, &davg, &dmax, &umin, &uavg, &umax, &lavg)
	if err == nil && sc.Int64 > 0 {
		resp.Speedtest = &speedSummary{
			Count: sc.Int64,
			DownMinBps: dmin.Float64, DownAvgBps: davg.Float64, DownMaxBps: dmax.Float64,
			UpMinBps: umin.Float64, UpAvgBps: uavg.Float64, UpMaxBps: umax.Float64,
			LatencyAvgMs: lavg.Float64,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func clampedOutageMs(e store.OutageEvent, from, to int64) int64 {
	start := max(e.StartedAt, from)
	end := to
	if e.EndedAt != nil {
		end = min(*e.EndedAt, to)
	}
	if end < start {
		return 0
	}
	return end - start
}

// internetOutageStats computes uptime over intervals where every tier-3
// target was simultaneously down, using a sweep over outage boundaries.
func (s *Server) internetOutageStats(ctx context.Context, targets []store.Target, from, to int64) (uptimePct float64, count, totalMs int64) {
	var tier3 []int64
	for _, t := range targets {
		if t.Tier == 3 && t.Enabled {
			tier3 = append(tier3, t.ID)
		}
	}
	if len(tier3) == 0 || to <= from {
		return 100, 0, 0
	}

	type boundary struct {
		ts    int64
		delta int
	}
	var bounds []boundary
	for _, id := range tier3 {
		events, err := s.store.ListOutages(ctx, from, to, id)
		if err != nil {
			s.log.Error("internet outage stats", "err", err)
			return 100, 0, 0
		}
		for _, e := range events {
			start := max(e.StartedAt, from)
			end := to
			if e.EndedAt != nil {
				end = min(*e.EndedAt, to)
			}
			if end <= start {
				continue
			}
			bounds = append(bounds, boundary{start, 1}, boundary{end, -1})
		}
	}
	if len(bounds) == 0 {
		return 100, 0, 0
	}
	sort.Slice(bounds, func(i, j int) bool {
		if bounds[i].ts != bounds[j].ts {
			return bounds[i].ts < bounds[j].ts
		}
		// process ends before starts at the same instant
		return bounds[i].delta < bounds[j].delta
	})

	down := 0
	var downSince int64
	allDown := false
	for _, b := range bounds {
		down += b.delta
		if !allDown && down == len(tier3) {
			allDown = true
			downSince = b.ts
		} else if allDown && down < len(tier3) {
			allDown = false
			count++
			totalMs += b.ts - downSince
		}
	}
	if allDown {
		count++
		totalMs += to - downSince
	}
	uptimePct = 100 * (1 - float64(totalMs)/float64(to-from))
	return uptimePct, count, totalMs
}

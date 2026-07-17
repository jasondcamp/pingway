package api

import (
	"net/http"
	"strconv"
	"time"
)

// lossBurst is a run of consecutive failed pings — sub-outage packet loss
// (an outage needs N consecutive failures; a burst can be a single drop).
type lossBurst struct {
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"` // ts of the last failed sample in the run
	Lost      int64  `json:"lost"`
	GapMs     *int64 `json:"gap_ms,omitempty"` // since the end of the previous burst
}

type lossBurstResponse struct {
	TargetID    int64       `json:"target_id"`
	From        int64       `json:"from"`
	To          int64       `json:"to"`
	Sent        int64       `json:"sent"`
	Lost        int64       `json:"lost"`
	Bursts      []lossBurst `json:"bursts"`
	MedianGapMs int64       `json:"median_gap_ms"` // 0 when fewer than 2 bursts
}

// handleLossBursts scans raw samples and groups consecutive failures.
// Raw samples are retained ~48h, so this is a diagnostic view of the
// recent past, not deep history. during_speedtest samples are ignored:
// saturation drops would masquerade as periodic loss.
func (s *Server) handleLossBursts(w http.ResponseWriter, r *http.Request) {
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

	samples, err := s.store.QuerySamples(r.Context(), targetID, from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := lossBurstResponse{TargetID: targetID, From: from, To: to, Bursts: []lossBurst{}}
	var cur *lossBurst
	var prevEnd int64
	for _, sm := range samples {
		if sm.DuringSpeedtest {
			continue
		}
		resp.Sent++
		if !sm.Success {
			resp.Lost++
			if cur == nil {
				cur = &lossBurst{StartedAt: sm.TS, EndedAt: sm.TS, Lost: 1}
				if prevEnd > 0 {
					gap := sm.TS - prevEnd
					cur.GapMs = &gap
				}
			} else {
				cur.EndedAt = sm.TS
				cur.Lost++
			}
			continue
		}
		if cur != nil {
			prevEnd = cur.EndedAt
			resp.Bursts = append(resp.Bursts, *cur)
			cur = nil
		}
	}
	if cur != nil {
		resp.Bursts = append(resp.Bursts, *cur)
	}

	var gaps []int64
	for _, b := range resp.Bursts {
		if b.GapMs != nil {
			gaps = append(gaps, *b.GapMs)
		}
	}
	if len(gaps) > 0 {
		// median of gaps: the "is it periodic?" number
		for i := 1; i < len(gaps); i++ {
			for j := i; j > 0 && gaps[j] < gaps[j-1]; j-- {
				gaps[j], gaps[j-1] = gaps[j-1], gaps[j]
			}
		}
		resp.MedianGapMs = gaps[len(gaps)/2]
	}

	writeJSON(w, http.StatusOK, resp)
}

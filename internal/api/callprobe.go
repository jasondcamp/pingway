package api

import (
	"net/http"
	"strconv"
	"time"

	"pingway.net/pingway/internal/callprobe"
	"pingway.net/pingway/internal/store"
)

// CallprobeInfo exposes live call-probe snapshots for /api/status and SSE.
type CallprobeInfo interface {
	Snapshots() []callprobe.Snapshot
}

// handleCallprobeHistory returns decimated per-second buckets in range.
func (s *Server) handleCallprobeHistory(w http.ResponseWriter, r *http.Request) {
	from, to := queryRange(r, time.Hour)
	buckets, err := s.store.QueryCallprobeHistory(r.Context(), from, to, 1800)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if buckets == nil {
		buckets = []store.CallprobeBucket{}
	}
	writeJSON(w, http.StatusOK, buckets)
}

type freezeResponse struct {
	Events []store.FreezeEventRow `json:"events"`
	// Totals over the full range (unlimited by the row cap)
	Count        int64 `json:"count"`
	CountVisible int64 `json:"count_visible"` // >= 200ms: a human-visible stall
}

// handleFreezes returns freeze events in range plus headline totals.
// min_ms filters the event list (default 0 = all recorded freezes).
func (s *Server) handleFreezes(w http.ResponseWriter, r *http.Request) {
	from, to := queryRange(r, time.Hour)
	var minMs int64
	if v := r.URL.Query().Get("min_ms"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			minMs = n
		}
	}
	events, err := s.store.ListFreezeEvents(r.Context(), from, to, minMs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []store.FreezeEventRow{}
	}
	resp := freezeResponse{Events: events}
	err = s.store.DB().QueryRowContext(r.Context(),
		`SELECT COUNT(*), COALESCE(SUM(duration_ms >= 200), 0) FROM freeze_events
		 WHERE started_at >= ? AND started_at <= ? AND during_speedtest = 0`,
		from, to).Scan(&resp.Count, &resp.CountVisible)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleReflectors lists configured reflectors.
func (s *Server) handleReflectors(w http.ResponseWriter, r *http.Request) {
	refs, err := s.store.ListReflectors(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if refs == nil {
		refs = []store.Reflector{}
	}
	writeJSON(w, http.StatusOK, refs)
}

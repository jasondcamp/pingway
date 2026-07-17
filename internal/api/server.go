// Package api serves the REST API, SSE stream, and embedded frontend.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"pingway.net/pingway/internal/live"
	"pingway.net/pingway/internal/outage"
	"pingway.net/pingway/internal/settings"
	"pingway.net/pingway/internal/sse"
	"pingway.net/pingway/internal/store"
)

// ErrSpeedtestRunning is returned by a SpeedtestTrigger when a run is
// already in progress (mapped to HTTP 409).
var ErrSpeedtestRunning = errors.New("speedtest already running")

// SpeedtestTrigger lets the API start a manual speed test run.
type SpeedtestTrigger interface {
	TriggerNow() error
	Running() bool
}

// PingerInfo exposes pinger health for /healthz and /api/status.
type PingerInfo interface {
	RunningCount() int
}

type Server struct {
	store     *store.Store
	tracker   *live.Tracker
	detector  *outage.Detector
	hub       *sse.Hub
	pinger    PingerInfo
	speedtest SpeedtestTrigger // may be nil (disabled)
	settings  *settings.Manager
	onTargetsChanged func()
	pingMode  string
	version   string
	startedAt int64
	log       *slog.Logger

	baselineMu   sync.Mutex
	baselines    map[int64]int64
	baselineTime time.Time

	mux *http.ServeMux
}

type Options struct {
	Store     *store.Store
	Tracker   *live.Tracker
	Detector  *outage.Detector
	Hub       *sse.Hub
	Pinger    PingerInfo
	Speedtest SpeedtestTrigger
	Settings  *settings.Manager
	// OnTargetsChanged is called after any target CRUD so the pinger
	// manager can reconcile its goroutines. It must not depend on the
	// request context.
	OnTargetsChanged func()
	PingMode         string
	Version          string
	Frontend         fs.FS // embedded SPA; nil = API only
	Log              *slog.Logger
}

func NewServer(o Options) *Server {
	s := &Server{
		store:            o.Store,
		tracker:          o.Tracker,
		detector:         o.Detector,
		hub:              o.Hub,
		pinger:           o.Pinger,
		speedtest:        o.Speedtest,
		settings:         o.Settings,
		onTargetsChanged: o.OnTargetsChanged,
		pingMode:         o.PingMode,
		version:          o.Version,
		startedAt:        time.Now().UnixMilli(),
		log:              o.Log.With("component", "api"),
		baselines:        make(map[int64]int64),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/targets", s.handleListTargets)
	mux.HandleFunc("POST /api/targets", s.handleCreateTarget)
	mux.HandleFunc("PUT /api/targets/{id}", s.handleUpdateTarget)
	mux.HandleFunc("DELETE /api/targets/{id}", s.handleDeleteTarget)
	mux.HandleFunc("GET /api/ping", s.handlePing)
	mux.HandleFunc("GET /api/speedtests", s.handleListSpeedtests)
	mux.HandleFunc("POST /api/speedtest/run", s.handleRunSpeedtest)
	mux.HandleFunc("GET /api/outages", s.handleOutages)
	mux.HandleFunc("GET /api/lossbursts", s.handleLossBursts)
	mux.HandleFunc("GET /api/summary", s.handleSummary)
	mux.HandleFunc("GET /api/export", s.handleExport)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	if o.Hub != nil {
		mux.Handle("GET /api/stream", o.Hub)
	}
	if o.Frontend != nil {
		s.mountFrontend(mux, o.Frontend)
	}
	s.mux = mux
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// queryRange parses from/to unix-ms query params with defaults.
func queryRange(r *http.Request, defSpan time.Duration) (from, to int64) {
	now := time.Now().UnixMilli()
	to = now
	from = now - defSpan.Milliseconds()
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = n
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = n
		}
	}
	return from, to
}

// --- health ---

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// DB writable check
	if err := s.store.SetSetting(r.Context(), "healthcheck", strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Sprintf("db not writable: %v", err))
		return
	}
	// pinger goroutines alive (only meaningful if targets exist)
	n, err := s.store.CountTargets(r.Context())
	if err == nil && n > 0 && s.pinger != nil && s.pinger.RunningCount() == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no ping loops running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- status ---

type targetStatus struct {
	store.Target
	State         string  `json:"state"`
	LastRTTMicros int64   `json:"last_rtt_us"`
	Loss60sPct    float64 `json:"loss_60s_pct"`
	BaselineRTTUs int64   `json:"baseline_rtt_us"`
	OutageSince   *int64  `json:"outage_since,omitempty"`
}

type statusResponse struct {
	Version          string            `json:"version"`
	PingMode         string            `json:"ping_mode"`
	StartedAt        int64             `json:"started_at"`
	Now              int64             `json:"now"`
	Internet         internetStatus    `json:"internet"`
	Targets          []targetStatus    `json:"targets"`
	LastSpeedtest    *store.SpeedTestRow `json:"last_speedtest"`
	SpeedtestRunning bool              `json:"speedtest_running"`
}

type internetStatus struct {
	State       string `json:"state"`
	OutageSince *int64 `json:"outage_since,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	targets, err := s.store.ListTargets(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	states := s.detector.States()
	open, err := s.store.OpenOutages(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	openByTarget := make(map[int64]int64)
	for _, e := range open {
		openByTarget[e.TargetID] = e.StartedAt
	}
	baselines := s.getBaselines(ctx, targets)

	resp := statusResponse{
		Version:   s.version,
		PingMode:  s.pingMode,
		StartedAt: s.startedAt,
		Now:       time.Now().UnixMilli(),
	}

	tier3Total, tier3Down := 0, 0
	var internetSince *int64
	for _, t := range targets {
		st := states[t.ID]
		ls := s.tracker.Stats(t.ID)
		ts := targetStatus{
			Target:        t,
			State:         st.String(),
			LastRTTMicros: ls.LastRTTMicros,
			Loss60sPct:    ls.LossPct,
			BaselineRTTUs: baselines[t.ID],
		}
		if since, ok := openByTarget[t.ID]; ok {
			ts.OutageSince = &since
		}
		if t.Enabled && t.Tier == 3 {
			tier3Total++
			if st == outage.StateDown {
				tier3Down++
				if since, ok := openByTarget[t.ID]; ok && (internetSince == nil || since > *internetSince) {
					v := since
					internetSince = &v
				}
			}
		}
		resp.Targets = append(resp.Targets, ts)
	}

	resp.Internet = internetStatus{State: "up"}
	if tier3Total > 0 && tier3Down == tier3Total {
		// internet outage began when the last tier-3 target went down
		resp.Internet = internetStatus{State: "down", OutageSince: internetSince}
	}

	if last, err := s.store.LastSpeedTest(ctx); err == nil {
		resp.LastSpeedtest = last
	}
	if s.speedtest != nil {
		resp.SpeedtestRunning = s.speedtest.Running()
	}
	writeJSON(w, http.StatusOK, resp)
}

// getBaselines returns each target's 24h average RTT (µs), cached for 5
// minutes. Prefers 1m rollups, falls back to raw samples for young
// deployments.
func (s *Server) getBaselines(ctx context.Context, targets []store.Target) map[int64]int64 {
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	if time.Since(s.baselineTime) < 5*time.Minute && len(s.baselines) > 0 {
		return s.baselines
	}
	out := make(map[int64]int64, len(targets))
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	for _, t := range targets {
		var avg *float64
		err := s.store.DB().QueryRowContext(ctx,
			`SELECT AVG(rtt_avg_us) FROM ping_rollup_1m WHERE target_id = ? AND ts_bucket >= ?`,
			t.ID, since).Scan(&avg)
		if err != nil || avg == nil {
			err = s.store.DB().QueryRowContext(ctx,
				`SELECT AVG(rtt_us) FROM ping_samples WHERE target_id = ? AND ts >= ? AND success = 1`,
				t.ID, since).Scan(&avg)
			if err != nil || avg == nil {
				continue
			}
		}
		out[t.ID] = int64(*avg)
	}
	s.baselines = out
	s.baselineTime = time.Now()
	return out
}

// --- speedtest trigger ---

func (s *Server) handleRunSpeedtest(w http.ResponseWriter, r *http.Request) {
	if s.speedtest == nil {
		writeErr(w, http.StatusServiceUnavailable, "speed tests disabled")
		return
	}
	err := s.speedtest.TriggerNow()
	switch {
	case errors.Is(err, ErrSpeedtestRunning):
		writeErr(w, http.StatusConflict, "a speed test is already running")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
	}
}

func (s *Server) handleListSpeedtests(w http.ResponseWriter, r *http.Request) {
	from, to := queryRange(r, 7*24*time.Hour)
	rows, err := s.store.ListSpeedTests(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []store.SpeedTestRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleOutages(w http.ResponseWriter, r *http.Request) {
	from, to := queryRange(r, 7*24*time.Hour)
	var targetID int64
	if v := r.URL.Query().Get("target"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad target id")
			return
		}
		targetID = n
	}
	events, err := s.store.ListOutages(r.Context(), from, to, targetID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []store.OutageEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

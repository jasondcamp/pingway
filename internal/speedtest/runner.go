package speedtest

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"pingway.net/pingway/internal/api"
	"pingway.net/pingway/internal/settings"
	"pingway.net/pingway/internal/sse"
	"pingway.net/pingway/internal/store"
)

// Runner schedules speed tests at the configured interval (±10% jitter),
// supports manual triggers, skips runs during internet-level outages, and
// persists every outcome — including failures and skip markers.
type Runner struct {
	store    *store.Store
	settings *settings.Manager
	hub      *sse.Hub
	log      *slog.Logger
	dataDir  string

	// duringSpeedtest is shared with the pinger so concurrent samples are
	// flagged; loaded latency is derived from those samples afterwards.
	duringSpeedtest *atomic.Bool
	// allTier3Down reports whether an internet-level outage is active.
	allTier3Down func() bool

	running atomic.Bool
	trigger chan struct{}

	// engineOverride, when set, bypasses engineFor (tests inject mocks).
	engineOverride Engine
}

func NewRunner(st *store.Store, sm *settings.Manager, hub *sse.Hub, duringSpeedtest *atomic.Bool,
	allTier3Down func() bool, dataDir string, log *slog.Logger) *Runner {
	return &Runner{
		store:           st,
		settings:        sm,
		hub:             hub,
		log:             log.With("component", "speedtest"),
		dataDir:         dataDir,
		duringSpeedtest: duringSpeedtest,
		allTier3Down:    allTier3Down,
		trigger:         make(chan struct{}, 1),
	}
}

// Running reports whether a test is currently in progress.
func (r *Runner) Running() bool { return r.running.Load() }

// TriggerNow starts a manual run; api.ErrSpeedtestRunning if one is active.
func (r *Runner) TriggerNow() error {
	if r.running.Load() {
		return api.ErrSpeedtestRunning
	}
	select {
	case r.trigger <- struct{}{}:
		return nil
	default:
		return api.ErrSpeedtestRunning
	}
}

// engineFor builds the engine each run so settings changes apply without
// restart.
func (r *Runner) engineFor(name string) (Engine, error) {
	if r.engineOverride != nil {
		return r.engineOverride, nil
	}
	switch name {
	case "librespeed":
		return NewLibreSpeed(r.log), nil
	case "cloudflare":
		return NewCloudflare(r.log), nil
	case "ookla":
		return NewOokla(r.dataDir, r.settings.Get().OoklaAcceptEULA, r.log), nil
	default:
		return nil, fmt.Errorf("unknown speedtest engine %q", name)
	}
}

// Run is the scheduler loop; run it under the supervisor.
func (r *Runner) Run(ctx context.Context) error {
	timer := time.NewTimer(r.nextDelay())
	defer timer.Stop()
	for {
		scheduled := false
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			scheduled = true
		case <-r.trigger:
		}

		cfg := r.settings.Get()
		switch {
		case scheduled && !cfg.SpeedtestEnabled:
			// disabled: keep ticking so re-enabling takes effect
		case scheduled && r.allTier3Down():
			r.log.Warn("skipping scheduled speed test: internet outage active")
			r.store.InsertSpeedTest(ctx, store.SpeedTestRow{
				Engine: cfg.SpeedtestEngine,
				RanAt:  time.Now().UnixMilli(),
				Error:  "skipped_outage",
			})
		default:
			r.runOnce(ctx, cfg.SpeedtestEngine)
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(r.nextDelay())
	}
}

// nextDelay returns the configured interval with ±10% jitter.
func (r *Runner) nextDelay() time.Duration {
	interval := time.Duration(r.settings.Get().SpeedtestIntervalMinutes) * time.Minute
	jitter := 0.9 + 0.2*rand.Float64()
	return time.Duration(float64(interval) * jitter)
}

func (r *Runner) runOnce(ctx context.Context, engineName string) {
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	defer r.running.Store(false)

	r.log.Info("speed test starting", "engine", engineName)
	r.hub.Broadcast(sse.Event{Name: "status", Data: map[string]any{
		"type": "speedtest_started", "engine": engineName, "at": time.Now().UnixMilli(),
	}})

	startMs := time.Now().UnixMilli()
	if r.duringSpeedtest != nil {
		r.duringSpeedtest.Store(true)
	}

	var result *Result
	engine, err := r.engineFor(engineName)
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		result, err = engine.Run(rctx)
		cancel()
	}

	if r.duringSpeedtest != nil {
		r.duringSpeedtest.Store(false)
	}
	endMs := time.Now().UnixMilli()

	if err != nil {
		result = &Result{
			Engine: engineName,
			RanAt:  time.UnixMilli(startMs),
			Error:  err.Error(),
		}
		result.DurationMs = endMs - startMs
		r.log.Error("speed test failed", "engine", engineName, "err", err)
	} else {
		if result.LoadedLatencyMs == 0 {
			if loaded := r.deriveLoadedLatency(ctx, startMs, endMs); loaded > 0 {
				result.LoadedLatencyMs = loaded
			}
		}
		r.log.Info("speed test finished",
			"engine", result.Engine, "server", result.ServerName,
			"down_mbps", fmt.Sprintf("%.1f", result.DownloadBps/1e6),
			"up_mbps", fmt.Sprintf("%.1f", result.UploadBps/1e6),
			"latency_ms", fmt.Sprintf("%.1f", result.LatencyMs),
			"loaded_latency_ms", fmt.Sprintf("%.1f", result.LoadedLatencyMs))
	}

	row := store.SpeedTestRow{
		Engine:          result.Engine,
		ServerName:      result.ServerName,
		ServerID:        result.ServerID,
		DownloadBps:     result.DownloadBps,
		UploadBps:       result.UploadBps,
		LatencyMs:       result.LatencyMs,
		LoadedLatencyMs: result.LoadedLatencyMs,
		PacketLoss:      result.PacketLoss,
		RanAt:           result.RanAt.UnixMilli(),
		DurationMs:      result.DurationMs,
		Error:           result.Error,
	}
	if id, err := r.store.InsertSpeedTest(ctx, row); err != nil {
		r.log.Error("store speed test", "err", err)
	} else {
		row.ID = id
	}

	r.hub.Broadcast(sse.Event{Name: "speedtest", Data: row})
	r.hub.Broadcast(sse.Event{Name: "status", Data: map[string]any{
		"type": "speedtest_finished", "engine": row.Engine, "at": endMs, "error": row.Error,
	}})
}

// deriveLoadedLatency averages ping RTTs flagged during_speedtest across
// all targets in the run window — latency under load as actually observed
// on the monitored path.
func (r *Runner) deriveLoadedLatency(ctx context.Context, fromMs, toMs int64) float64 {
	var avg *float64
	err := r.store.DB().QueryRowContext(ctx,
		`SELECT AVG(rtt_us) FROM ping_samples WHERE during_speedtest = 1 AND success = 1 AND ts >= ? AND ts <= ?`,
		fromMs, toMs).Scan(&avg)
	if err != nil || avg == nil {
		return 0
	}
	return *avg / 1000
}

// pingway: continuous network path monitor. See SPEC.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"pingway.net/pingway/frontend"
	"pingway.net/pingway/internal/api"
	"pingway.net/pingway/internal/config"
	"pingway.net/pingway/internal/live"
	"pingway.net/pingway/internal/outage"
	"pingway.net/pingway/internal/pinger"
	"pingway.net/pingway/internal/settings"
	"pingway.net/pingway/internal/speedtest"
	"pingway.net/pingway/internal/sse"
	"pingway.net/pingway/internal/store"
	"pingway.net/pingway/internal/supervise"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// -healthcheck: probe the running instance and exit (used as the
	// container HEALTHCHECK, since distroless has no curl).
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = config.DefaultListen
	}
	if listen[0] == ':' {
		listen = "127.0.0.1" + listen
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + listen + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy: status", resp.StatusCode)
		return 1
	}
	fmt.Println("ok")
	return 0
}

func run() error {
	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogFormat)
	log.Info("pingway starting", "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- storage ---
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}
	dbPath := filepath.Join(cfg.DataDir, "pingway.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("database open", "path", dbPath)

	// --- targets: seed from yaml/env, defaults if nothing anywhere ---
	if len(cfg.Targets) > 0 {
		for i, t := range cfg.Targets {
			err := st.UpsertTargetByHost(ctx, store.Target{Name: t.Name, Host: t.Host, Tier: t.Tier, SortOrder: i})
			if err != nil {
				return err
			}
		}
	} else if n, err := st.CountTargets(ctx); err != nil {
		return err
	} else if n == 0 {
		for i, t := range config.DefaultTargets() {
			err := st.UpsertTargetByHost(ctx, store.Target{Name: t.Name, Host: t.Host, Tier: t.Tier, SortOrder: i})
			if err != nil {
				return err
			}
			log.Info("added default target", "name", t.Name, "host", t.Host, "tier", t.Tier)
		}
	}

	appSettings, err := settings.NewManager(ctx, st, cfg)
	if err != nil {
		return err
	}

	// --- components ---
	sup := supervise.New(log)
	hub := sse.NewHub(log)
	tracker := live.NewTracker()
	writer := store.NewWriter(st, log)

	targetNames := func() map[int64]string {
		names := make(map[int64]string)
		if targets, err := st.ListTargets(ctx); err == nil {
			for _, t := range targets {
				names[t.ID] = t.Name
			}
		}
		return names
	}
	detector := outage.NewDetector(st, cfg.Outage.FailureThreshold, cfg.Outage.RecoveryThreshold,
		func(tr outage.Transition, state outage.State) {
			kind := "target_up"
			if tr.Kind == outage.TransitionDown {
				kind = "target_down"
			}
			hub.Broadcast(sse.Event{Name: "status", Data: map[string]any{
				"type": kind, "target_id": tr.TargetID, "at": tr.At, "state": state.String(),
				"name": targetNames()[tr.TargetID],
			}})
		}, log)
	if err := detector.Resume(ctx); err != nil {
		return err
	}

	var duringSpeedtest atomic.Bool
	onSample := func(s store.Sample) {
		writer.Submit(s)
		tracker.Add(s)
		detector.Feed(ctx, s)
	}

	mode := pinger.DetectMode(log)
	pingMgr := pinger.NewManager(
		pinger.NewProbingPingFunc(mode),
		time.Duration(cfg.Ping.IntervalMs)*time.Millisecond,
		time.Duration(cfg.Ping.TimeoutMs)*time.Millisecond,
		onSample, &duringSpeedtest, sup, log)

	reconcile := func() {
		targets, err := st.ListTargets(ctx)
		if err != nil {
			log.Error("reconcile: list targets", "err", err)
			return
		}
		pingMgr.Reconcile(ctx, targets)
	}
	reconcile()

	aggregator := store.NewAggregator(st, func() (int, int) {
		a := appSettings.Get()
		return a.RetentionRawHours, a.RetentionRollup1mDays
	}, log)

	allTier3Down := func() bool {
		targets, err := st.ListTargets(ctx)
		if err != nil {
			return false
		}
		states := detector.States()
		total, down := 0, 0
		for _, t := range targets {
			if t.Enabled && t.Tier == 3 {
				total++
				if states[t.ID] == outage.StateDown {
					down++
				}
			}
		}
		return total > 0 && down == total
	}
	speedRunner := speedtest.NewRunner(st, appSettings, hub, &duringSpeedtest, allTier3Down, cfg.DataDir, log)

	// --- supervised loops ---
	sup.Go(ctx, "writer", writer.Run)
	sup.Go(ctx, "aggregator", aggregator.Run)
	sup.Go(ctx, "speedtest", speedRunner.Run)
	sup.Go(ctx, "sse-ping-emitter", func(c context.Context) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.Done():
				return nil
			case <-ticker.C:
				if batch := tracker.Drain(); len(batch) > 0 && hub.ClientCount() > 0 {
					hub.Broadcast(sse.Event{Name: "ping", Data: batch})
				}
			}
		}
	})

	// --- http ---
	feFS, err := frontend.FS()
	if err != nil {
		return fmt.Errorf("frontend fs: %w", err)
	}
	server := api.NewServer(api.Options{
		Store:            st,
		Tracker:          tracker,
		Detector:         detector,
		Hub:              hub,
		Pinger:           pingMgr,
		Speedtest:        speedRunner,
		Settings:         appSettings,
		OnTargetsChanged: reconcile,
		PingMode:         string(mode),
		Version:          version,
		Frontend:         feFS,
		Log:              log,
	})
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutCtx)
	}()

	log.Info("listening", "addr", cfg.Listen, "ping_mode", mode)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		stop()
		sup.Wait()
		return err
	}

	// graceful shutdown: stop loops, flush writer
	stop()
	sup.Wait()
	log.Info("pingway stopped")
	return nil
}

func newLogger(format string) *slog.Logger {
	var h slog.Handler
	if format == "text" || (format == "" && isTerminal()) {
		h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	return slog.New(h)
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

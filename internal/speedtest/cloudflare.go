package speedtest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Cloudflare engine: measures against speed.cloudflare.com's __down/__up
// endpoints. These are NOT an official API — Cloudflare can change or
// rate-limit them at any time, so this engine degrades gracefully and
// reports failures as stored results rather than crashing the scheduler.
type Cloudflare struct {
	client *http.Client
	log    *slog.Logger
}

const (
	cfBase    = "https://speed.cloudflare.com"
	// 50MB chunks, re-requested until time is up. Cloudflare 403s larger
	// requests (100MB was rejected as of 2026-07), so stay well under.
	cfDownURL = cfBase + "/__down?bytes=52428800"
	cfUpURL   = cfBase + "/__up"
	cfPingURL = cfBase + "/__down?bytes=0"
)

func NewCloudflare(log *slog.Logger) *Cloudflare {
	return &Cloudflare{
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log.With("engine", "cloudflare"),
	}
}

func (c *Cloudflare) Name() string { return "cloudflare" }

func (c *Cloudflare) Run(ctx context.Context) (*Result, error) {
	started := time.Now()
	res := &Result{Engine: c.Name(), ServerName: "speed.cloudflare.com", RanAt: started}

	latency, err := measureHTTPLatency(ctx, c.client, cfPingURL, 5)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: latency: %w", err)
	}
	res.LatencyMs = latency

	down, err := measureThroughput(ctx, parallelStreams, transferDuration, rampUp,
		func(cc context.Context, counted *atomic.Int64) {
			drainDownload(cc, c.client, cfDownURL, counted)
		})
	if err != nil {
		return nil, fmt.Errorf("cloudflare: download: %w", err)
	}
	res.DownloadBps = down

	up, err := measureThroughput(ctx, parallelStreams, transferDuration, rampUp,
		func(cc context.Context, counted *atomic.Int64) {
			pushUpload(cc, c.client, cfUpURL, 20*1024*1024, counted)
		})
	if err != nil {
		return nil, fmt.Errorf("cloudflare: upload: %w", err)
	}
	res.UploadBps = up

	res.DurationMs = time.Since(started).Milliseconds()
	return res, nil
}

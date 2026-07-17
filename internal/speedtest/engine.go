// Package speedtest provides pluggable speed test engines (librespeed,
// cloudflare, ookla) and the scheduler that runs them.
package speedtest

import (
	"context"
	"time"
)

// Result is the outcome of one speed test run. A failed run has Error set
// and is stored anyway — failures are data, not noise.
type Result struct {
	Engine          string
	ServerName      string
	ServerID        string
	DownloadBps     float64
	UploadBps       float64
	LatencyMs       float64 // idle latency
	LoadedLatencyMs float64 // latency under load, derived or engine-provided
	PacketLoss      float64 // if engine provides it
	RanAt           time.Time
	DurationMs      int64
	Error           string
}

// Engine runs one speed test.
type Engine interface {
	Name() string
	Run(ctx context.Context) (*Result, error)
}

// Transfer tuning shared by the HTTP-based engines. Throughput is measured
// over a fixed wall-clock duration, discarding the ramp-up window
// (measurement honesty: steady-state only).
const (
	transferDuration = 10 * time.Second
	rampUp           = 2 * time.Second
	parallelStreams  = 4
)

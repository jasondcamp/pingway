//go:build integration

package speedtest

// Integration test against real public LibreSpeed servers. Run with:
//   go test -tags=integration -run TestLibreSpeedIntegration ./internal/speedtest -v
// Skipped in CI by default (no -tags=integration).

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLibreSpeedIntegration(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	engine := NewLibreSpeed(log)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := engine.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("server=%s down=%.1f Mbps up=%.1f Mbps latency=%.1f ms (%.1fs)",
		res.ServerName, res.DownloadBps/1e6, res.UploadBps/1e6, res.LatencyMs,
		float64(res.DurationMs)/1000)
	if res.DownloadBps <= 0 || res.UploadBps <= 0 || res.LatencyMs <= 0 {
		t.Fatalf("implausible result: %+v", res)
	}
}

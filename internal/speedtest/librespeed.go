package speedtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// LibreSpeed engine: native net/http implementation against public
// LibreSpeed backends (garbage.php download endpoint, empty.php upload/
// latency endpoint). Default engine; fully open, no EULA issues.
type LibreSpeed struct {
	client *http.Client
	log    *slog.Logger
	// serverOverride (LIBRESPEED_SERVER env or config) pins a backend base
	// URL like https://speedtest.example.com/backend; otherwise the public
	// server list is fetched and the lowest-latency server picked.
	serverOverride string
}

const librespeedServerList = "https://librespeed.org/backend-servers/servers.php"

func NewLibreSpeed(log *slog.Logger) *LibreSpeed {
	return &LibreSpeed{
		client:         &http.Client{Timeout: 30 * time.Second},
		log:            log.With("engine", "librespeed"),
		serverOverride: os.Getenv("LIBRESPEED_SERVER"),
	}
}

func (l *LibreSpeed) Name() string { return "librespeed" }

type librespeedServer struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	DlURL  string `json:"dlURL"`
	UlURL  string `json:"ulURL"`
	PingURL string `json:"pingURL"`
}

func (s librespeedServer) base() string {
	b := s.Server
	if strings.HasPrefix(b, "//") {
		b = "https:" + b
	}
	return strings.TrimSuffix(b, "/")
}

func (l *LibreSpeed) Run(ctx context.Context) (*Result, error) {
	started := time.Now()
	res := &Result{Engine: l.Name(), RanAt: started}

	srv, err := l.pickServer(ctx)
	if err != nil {
		return nil, fmt.Errorf("librespeed: pick server: %w", err)
	}
	res.ServerName = srv.Name
	res.ServerID = srv.base()

	pingURL := srv.base() + "/" + strings.TrimPrefix(orDefault(srv.PingURL, "empty.php"), "/")
	dlURL := srv.base() + "/" + strings.TrimPrefix(orDefault(srv.DlURL, "garbage.php"), "/") + "?ckSize=100"
	ulURL := srv.base() + "/" + strings.TrimPrefix(orDefault(srv.UlURL, "empty.php"), "/")

	latency, err := measureHTTPLatency(ctx, l.client, pingURL, 5)
	if err != nil {
		return nil, fmt.Errorf("librespeed: latency: %w", err)
	}
	res.LatencyMs = latency

	down, err := measureThroughput(ctx, parallelStreams, transferDuration, rampUp,
		func(c context.Context, counted *atomic.Int64) {
			drainDownload(c, l.client, dlURL, counted)
		})
	if err != nil {
		return nil, fmt.Errorf("librespeed: download: %w", err)
	}
	res.DownloadBps = down

	up, err := measureUpload(ctx, l.client, ulURL, transferDuration, rampUp, parallelStreams)
	if err != nil {
		return nil, fmt.Errorf("librespeed: upload: %w", err)
	}
	res.UploadBps = up

	res.DurationMs = time.Since(started).Milliseconds()
	return res, nil
}

// pickServer returns the override server, or the public-list server with
// the lowest probe latency (probing at most 8 candidates).
func (l *LibreSpeed) pickServer(ctx context.Context) (*librespeedServer, error) {
	if l.serverOverride != "" {
		return &librespeedServer{Name: "custom", Server: l.serverOverride}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, librespeedServerList, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch server list: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var servers []librespeedServer
	if err := json.Unmarshal(body, &servers); err != nil {
		return nil, fmt.Errorf("parse server list: %w", err)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("empty server list")
	}
	if len(servers) > 8 {
		servers = servers[:8]
	}
	best := -1
	bestLat := 0.0
	for i := range servers {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pingURL := servers[i].base() + "/" + strings.TrimPrefix(orDefault(servers[i].PingURL, "empty.php"), "/")
		lat, err := measureHTTPLatency(pctx, l.client, pingURL, 1)
		cancel()
		if err != nil {
			continue
		}
		if best == -1 || lat < bestLat {
			best, bestLat = i, lat
		}
	}
	if best == -1 {
		return nil, fmt.Errorf("no reachable librespeed server")
	}
	l.log.Debug("picked server", "name", servers[best].Name, "latency_ms", bestLat)
	return &servers[best], nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

package pinger

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// Mode is the ICMP socket mode in use.
type Mode string

const (
	ModeRaw Mode = "raw" // privileged raw socket (CAP_NET_RAW)
	ModeUDP Mode = "udp" // unprivileged UDP/ICMP datagram socket
)

// NewProbingPingFunc returns the production PingFunc using pro-bing in the
// given mode.
func NewProbingPingFunc(mode Mode) PingFunc {
	privileged := mode == ModeRaw
	return func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		p := probing.New(host)
		p.Count = 1
		p.Timeout = timeout
		p.SetPrivileged(privileged)
		if err := p.RunWithContext(ctx); err != nil {
			return 0, fmt.Errorf("ping %s: %w", host, err)
		}
		stats := p.Statistics()
		if stats.PacketsRecv < 1 {
			return 0, fmt.Errorf("ping %s: timeout", host)
		}
		return stats.AvgRtt, nil
	}
}

// DetectMode probes whether privileged raw sockets are available (needs
// CAP_NET_RAW) and falls back to unprivileged UDP ping. The result is
// logged and exposed in /api/status.
func DetectMode(log *slog.Logger) Mode {
	p := probing.New("127.0.0.1")
	p.Count = 1
	p.Timeout = 500 * time.Millisecond
	p.SetPrivileged(true)
	err := p.Run()
	if err == nil {
		log.Info("icmp mode: privileged raw socket")
		return ModeRaw
	}
	log.Info("icmp mode: unprivileged udp fallback (grant CAP_NET_RAW for raw sockets)", "probe_err", err)
	return ModeUDP
}

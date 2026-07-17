//go:build !linux

package config

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// DefaultGateway shells out to `route -n get default` (macOS/BSD). Used for
// local development; production containers are Linux.
func DefaultGateway() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("route -n get default: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "gateway:"); ok {
			gw := strings.TrimSpace(v)
			if net.ParseIP(gw) != nil {
				return gw, nil
			}
		}
	}
	return "", fmt.Errorf("no default gateway in route output")
}

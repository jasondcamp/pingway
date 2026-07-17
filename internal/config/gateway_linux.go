//go:build linux

package config

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// DefaultGateway parses /proc/net/route for the IPv4 default route.
func DefaultGateway() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("open /proc/net/route: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		// Destination 00000000 means default route.
		if fields[1] != "00000000" {
			continue
		}
		gwHex, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		ip := make(net.IP, 4)
		// /proc/net/route stores addresses in little-endian hex.
		binary.LittleEndian.PutUint32(ip, uint32(gwHex))
		if !ip.Equal(net.IPv4zero) {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no default route found")
}

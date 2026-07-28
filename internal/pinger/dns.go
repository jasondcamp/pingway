package pinger

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"time"
)

// dnsQueryName is the name resolved by DNS probes. It is popular enough to
// be answered from cache by any public resolver, so the RTT measures the
// resolver's responsiveness rather than upstream recursion.
const dnsQueryName = "example.com."

// NewDNSPingFunc returns a PingFunc that measures one real DNS lookup: a
// recursive A query for dnsQueryName over UDP to host:53 (a host:port
// target overrides the port), timed until a well-formed response with the
// matching transaction ID arrives. No answer within the timeout fails the
// sample exactly like a lost ping; an rcode other than NOERROR also fails —
// the server is reachable but DNS service is broken.
func NewDNSPingFunc() PingFunc {
	return func(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
		addr := host
		if _, _, err := net.SplitHostPort(host); err != nil {
			addr = net.JoinHostPort(host, "53")
		}
		var d net.Dialer
		conn, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return 0, fmt.Errorf("dns %s: %w", host, err)
		}
		defer conn.Close()
		// unblock the read if the surrounding context is cancelled (shutdown)
		stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(0, 1)) })
		defer stop()

		id := uint16(rand.Uint32())
		query := buildDNSQuery(id, dnsQueryName)

		start := time.Now()
		if err := conn.SetDeadline(start.Add(timeout)); err != nil {
			return 0, fmt.Errorf("dns %s: %w", host, err)
		}
		if _, err := conn.Write(query); err != nil {
			return 0, fmt.Errorf("dns %s: %w", host, err)
		}

		buf := make([]byte, 1500)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return 0, fmt.Errorf("dns %s: %w", host, err)
			}
			// ignore strays: too short, wrong transaction ID, or not a response
			if n < 12 || binary.BigEndian.Uint16(buf[:2]) != id || buf[2]&0x80 == 0 {
				continue
			}
			if rcode := buf[3] & 0x0F; rcode != 0 {
				return 0, fmt.Errorf("dns %s: rcode %d", host, rcode)
			}
			return time.Since(start), nil
		}
	}
}

// buildDNSQuery encodes a single-question recursive A/IN query.
func buildDNSQuery(id uint16, name string) []byte {
	b := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(b[0:2], id)
	binary.BigEndian.PutUint16(b[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(b[4:6], 1)      // QDCOUNT
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)          // root
	b = append(b, 0, 1, 0, 1) // QTYPE A, QCLASS IN
	return b
}

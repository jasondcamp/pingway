package pinger

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// fakeDNSServer answers each query on a loopback UDP socket. rcode sets the
// response code; respond=false swallows queries to force a timeout.
func fakeDNSServer(t *testing.T, rcode byte, respond bool) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if !respond || n < 12 {
				continue
			}
			resp := make([]byte, n)
			copy(resp, buf[:n])
			resp[2] |= 0x80 // QR
			resp[3] = (resp[3] &^ 0x0F) | rcode
			pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSPingSuccess(t *testing.T) {
	addr := fakeDNSServer(t, 0, true)
	ping := NewDNSPingFunc()
	rtt, err := ping(context.Background(), addr, time.Second)
	if err != nil {
		t.Fatalf("dns ping failed: %v", err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Fatalf("implausible rtt %v", rtt)
	}
}

func TestDNSPingServfail(t *testing.T) {
	addr := fakeDNSServer(t, 2, true) // SERVFAIL
	ping := NewDNSPingFunc()
	if _, err := ping(context.Background(), addr, time.Second); err == nil {
		t.Fatal("expected error for SERVFAIL response")
	}
}

func TestDNSPingTimeout(t *testing.T) {
	addr := fakeDNSServer(t, 0, false)
	ping := NewDNSPingFunc()
	start := time.Now()
	_, err := ping(context.Background(), addr, 150*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("timeout not honored: %v", elapsed)
	}
}

func TestBuildDNSQuery(t *testing.T) {
	q := buildDNSQuery(0xBEEF, "example.com.")
	if binary.BigEndian.Uint16(q[:2]) != 0xBEEF {
		t.Fatal("bad id")
	}
	if binary.BigEndian.Uint16(q[4:6]) != 1 {
		t.Fatal("bad qdcount")
	}
	want := append([]byte{7}, "example"...)
	want = append(want, 3)
	want = append(want, "com"...)
	want = append(want, 0, 0, 1, 0, 1)
	if got := q[12:]; string(got) != string(want) {
		t.Fatalf("bad question: %v", got)
	}
}

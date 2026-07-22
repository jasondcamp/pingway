package callprobe

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

func TestPacketRoundTrip(t *testing.T) {
	buf := make([]byte, PacketSize)
	tok := [TokenLen]byte{1, 2, 3, 4, 5, 6, 7, 8}
	MarshalProbe(buf, tok, 42, 123456789)
	seq, send, ok := UnmarshalProbe(buf)
	if !ok || seq != 42 || send != 123456789 {
		t.Fatalf("roundtrip: seq=%d send=%d ok=%v", seq, send, ok)
	}
	if !IsProbe(buf) {
		t.Fatal("IsProbe rejected a valid packet")
	}
	if string(ProbeToken(buf)) != string(tok[:]) {
		t.Fatal("token did not survive roundtrip")
	}
	if IsProbe(buf[:100]) || IsProbe(make([]byte, PacketSize)) {
		t.Fatal("IsProbe accepted junk")
	}

	hello := make([]byte, PacketSize)
	MarshalHello(hello)
	if !IsHello(hello) || IsHello(buf) {
		t.Fatal("hello detection wrong")
	}
	reply := MarshalToken(tok)
	if len(reply) >= PacketSize {
		t.Fatal("token reply must be smaller than hello (no amplification)")
	}
	got, ok := UnmarshalToken(reply)
	if !ok || got != tok {
		t.Fatal("token reply roundtrip failed")
	}
}

func TestMOSBands(t *testing.T) {
	if m := mos(0, 30, 1); m < 4.2 {
		t.Fatalf("clean line should score >4.2, got %.2f", m)
	}
	if m := mos(5, 80, 30); m > 4.0 || m < 3.0 {
		t.Fatalf("lossy line should land in the degraded band, got %.2f", m)
	}
	if m := mos(40, 500, 200); m > 1.6 {
		t.Fatalf("broken line should approach 1, got %.2f", m)
	}
}

// TestFreezeDetection drives the finalizer directly: a run of consecutive
// losses bounded by received packets must produce one freeze event with
// the right duration.
func TestFreezeDetection(t *testing.T) {
	var freezes []FreezeEvent
	p := NewProber(Reflector{ID: 1, Name: "t"}, 50, nil,
		func(f FreezeEvent) { freezes = append(freezes, f) }, nil, slog.New(slog.DiscardHandler))

	base := time.Now().UnixNano()
	interval := int64(20 * time.Millisecond)
	// seqs 1..5 received, 6..15 lost (10 packets = 200ms), 16..20 received
	for i := int64(1); i <= 20; i++ {
		p.mu.Lock()
		p.sent[uint32(i)] = base + i*interval
		if i <= 5 || i >= 16 {
			p.rtts[uint32(i)] = int64(30 * time.Millisecond)
		}
		p.lastSeq = uint32(i) + 1
		p.mu.Unlock()
	}
	// finalize far in the future so lost packets time out
	p.finalize(base + int64(time.Hour))

	if len(freezes) != 1 {
		t.Fatalf("want 1 freeze, got %d: %+v", len(freezes), freezes)
	}
	f := freezes[0]
	if f.PacketsLost != 10 {
		t.Fatalf("want 10 lost, got %d", f.PacketsLost)
	}
	// duration = gap between first lost send (seq 6) and next received send (seq 16)
	if f.DurationMs != 200 {
		t.Fatalf("want 200ms, got %dms", f.DurationMs)
	}
}

// TestEndToEndAgainstLoopReflector runs a real prober against the real
// ReflectorServer (handshake included) and checks buckets flow with
// zero loss.
func TestEndToEndAgainstLoopReflector(t *testing.T) {
	rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewReflectorServer(rc, 0, 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	go srv.Serve(srvCtx)

	var mu sync.Mutex
	var buckets []SecondBucket
	p := NewProber(Reflector{ID: 7, Name: "loop", Host: rc.LocalAddr().String()}, 50,
		func(b SecondBucket) { mu.Lock(); buckets = append(buckets, b); mu.Unlock() },
		nil, nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}

	snap := p.Snapshot()
	if !snap.Alive {
		t.Fatal("prober should be alive against loopback")
	}
	if snap.MOS < 4.0 {
		t.Fatalf("loopback MOS should be excellent, got %.2f", snap.MOS)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(buckets) == 0 {
		t.Fatal("no second buckets emitted")
	}
	for _, b := range buckets {
		if b.Lost != 0 {
			t.Fatalf("loopback lost packets: %+v", b)
		}
		if b.ReflectorID != 7 {
			t.Fatalf("wrong reflector id: %+v", b)
		}
	}
}

// TestSpoofedProbeGetsNoEcho: a probe with a wrong token (what a
// spoofed-source attacker can produce at best) must be dropped.
func TestSpoofedProbeGetsNoEcho(t *testing.T) {
	rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewReflectorServer(rc, 0, 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	conn, err := net.DialUDP("udp", nil, rc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	buf := make([]byte, PacketSize)
	MarshalProbe(buf, [TokenLen]byte{9, 9, 9, 9, 9, 9, 9, 9}, 1, time.Now().UnixNano())
	for i := 0; i < 5; i++ {
		conn.Write(buf)
	}
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reply := make([]byte, 2048)
	if n, err := conn.Read(reply); err == nil {
		t.Fatalf("got %d-byte echo for an invalid token", n)
	}

	// and the legit handshake path still works from this socket
	hello := make([]byte, PacketSize)
	MarshalHello(hello)
	conn.Write(hello)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(reply)
	if err != nil {
		t.Fatal("hello got no token reply:", err)
	}
	tok, ok := UnmarshalToken(reply[:n])
	if !ok {
		t.Fatal("bad token reply")
	}
	MarshalProbe(buf, tok, 2, time.Now().UnixNano())
	conn.Write(buf)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(reply); err != nil {
		t.Fatal("valid token got no echo:", err)
	}
}

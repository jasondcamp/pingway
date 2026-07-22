package callprobe

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// ReflectorServer is the public-fleet-safe echo service:
//
//   - HELLO → TOKEN handshake (HMAC of the observed source address, so
//     the server keeps zero per-client state and spoofed sources never
//     learn a valid token)
//   - echoes only exact-size probe packets carrying a valid token,
//     only back to the observed source, reply size == request size
//   - per-source and global rate limits
type ReflectorServer struct {
	conn      *net.UDPConn
	key       []byte // per-boot HMAC key; restart invalidates all tokens
	perIPRate float64
	perIPCap  float64
	global    tokenBucket
	log       *slog.Logger

	mu      sync.Mutex
	sources map[string]*tokenBucket

	statsMu sync.Mutex
	echoed  uint64
	hellos  uint64
	dropped uint64
}

type tokenBucket struct {
	tokens float64
	cap    float64
	rate   float64
	last   time.Time
}

func (b *tokenBucket) allow(now time.Time) bool {
	b.tokens = min(b.cap, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// NewReflectorServer wraps conn. maxPPS caps total echo+token output;
// perIPPPS caps a single source (burst 2x).
func NewReflectorServer(conn *net.UDPConn, maxPPS, perIPPPS int, log *slog.Logger) (*ReflectorServer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("hmac key: %w", err)
	}
	if maxPPS <= 0 {
		maxPPS = 20000
	}
	if perIPPPS <= 0 {
		perIPPPS = 120
	}
	return &ReflectorServer{
		conn:      conn,
		key:       key,
		perIPRate: float64(perIPPPS),
		perIPCap:  float64(perIPPPS * 2),
		global:    tokenBucket{tokens: float64(maxPPS), cap: float64(maxPPS), rate: float64(maxPPS), last: time.Now()},
		log:       log,
		sources:   make(map[string]*tokenBucket),
	}, nil
}

// tokenFor derives the stateless per-source token.
func (s *ReflectorServer) tokenFor(src *net.UDPAddr) [TokenLen]byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(src.IP)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(src.Port))
	mac.Write(port[:])
	var out [TokenLen]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func (s *ReflectorServer) allowSource(src *net.UDPAddr, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ipKey := src.IP.String()
	b, ok := s.sources[ipKey]
	if !ok {
		b = &tokenBucket{tokens: s.perIPCap, cap: s.perIPCap, rate: s.perIPRate, last: now}
		s.sources[ipKey] = b
	}
	return b.allow(now)
}

// sweep drops idle per-source buckets so spoofed floods cannot grow the
// map unbounded.
func (s *ReflectorServer) sweep(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ip, b := range s.sources {
		if now.Sub(b.last) > time.Minute {
			delete(s.sources, ip)
		}
	}
	return len(s.sources)
}

// Serve reads and echoes until ctx is cancelled or the socket fails.
func (s *ReflectorServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()
	go s.statsLoop(ctx)

	buf := make([]byte, 2048)
	for {
		n, src, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		now := time.Now()
		pkt := buf[:n]

		switch {
		case IsHello(pkt):
			if s.globalAllow(now) && s.allowSource(src, now) {
				tok := s.tokenFor(src)
				s.conn.WriteToUDP(MarshalToken(tok), src)
				s.count(&s.hellos)
			} else {
				s.count(&s.dropped)
			}
		case IsProbe(pkt):
			tok := s.tokenFor(src)
			if subtle.ConstantTimeCompare(ProbeToken(pkt), tok[:]) == 1 &&
				s.globalAllow(now) && s.allowSource(src, now) {
				s.conn.WriteToUDP(pkt, src)
				s.count(&s.echoed)
			} else {
				s.count(&s.dropped)
			}
		default:
			s.count(&s.dropped)
		}
	}
}

func (s *ReflectorServer) globalAllow(now time.Time) bool {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.global.allow(now)
}

func (s *ReflectorServer) count(c *uint64) {
	s.statsMu.Lock()
	*c++
	s.statsMu.Unlock()
}

func (s *ReflectorServer) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := s.sweep(time.Now())
			s.statsMu.Lock()
			e, hl, d := s.echoed, s.hellos, s.dropped
			s.echoed, s.hellos, s.dropped = 0, 0, 0
			s.statsMu.Unlock()
			s.log.Info("stats", "echoed_per_min", e, "hellos_per_min", hl, "dropped_per_min", d, "active_sources", n)
		}
	}
}

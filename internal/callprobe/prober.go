package callprobe

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Reflector is one echo endpoint under test.
type Reflector struct {
	ID   int64
	Name string
	Host string // "host:port"
}

// SecondBucket is one second of finalized probe results for one reflector.
type SecondBucket struct {
	ReflectorID     int64
	TS              int64 // ms, start of second
	Sent            int
	Lost            int
	RTTAvgUs        int64
	JitterUs        int64
	MOSx100         int
	DuringSpeedtest bool
}

// FreezeEvent is a run of consecutive lost packets — what a human
// experiences as "the call froze for N ms".
type FreezeEvent struct {
	ReflectorID     int64
	StartedAt       int64 // ms
	DurationMs      int64
	PacketsLost     int
	DuringSpeedtest bool
}

// Snapshot is the live view of one reflector for the dashboard.
type Snapshot struct {
	ReflectorID int64   `json:"reflector_id"`
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	MOS         float64 `json:"mos"`
	RTTMs       float64 `json:"rtt_ms"`
	JitterMs    float64 `json:"jitter_ms"`
	LossPct     float64 `json:"loss_pct"`
	InFreeze    bool    `json:"in_freeze"`
	Alive       bool    `json:"alive"`
}

const (
	defaultTimeout = 2 * time.Second
	// minFreezeRun: 3 consecutive losses at 50pps = 60ms, below which a
	// jitter buffer absorbs the gap invisibly.
	minFreezeRun = 3
	// statsWindow is the rolling window MOS/loss are computed over.
	statsWindow = 10
)

// mos computes a Mean Opinion Score via the Cole & Rosenbluth
// simplification of the ITU-T G.107 E-model. This is the telecom
// industry's own quality metric: >4 good, 3–4 degraded, <3 unusable
// for calls.
func mos(lossPct, rttMs, jitterMs float64) float64 {
	effLatency := rttMs/2 + 2*jitterMs + 10
	var id float64
	if effLatency < 160 {
		id = effLatency / 40
	} else {
		id = (effLatency - 120) / 10
	}
	ie := 30 * math.Log(1+15*lossPct/100)
	r := 94.2 - id - ie
	if r < 0 {
		r = 0
	}
	if r > 100 {
		r = 100
	}
	m := 1 + 0.035*r + 7e-6*r*(r-60)*(100-r)
	return math.Max(1, math.Min(5, m))
}

// Prober runs the probe stream against one reflector.
type Prober struct {
	r        Reflector
	pps      int
	timeout  time.Duration
	onSecond func(SecondBucket)
	onFreeze func(FreezeEvent)
	during   *atomic.Bool
	log      *slog.Logger

	token   [TokenLen]byte // handshake token, set by Run before the loops

	mu      sync.Mutex
	sent    map[uint32]int64 // seq -> send unixnano, awaiting finalization
	rtts    map[uint32]int64 // seq -> rtt nanos, received but not finalized
	next    uint32           // next seq to finalize (in order)
	lastSeq uint32           // last seq sent (exclusive upper bound)

	// finalization state
	jitterNs   float64 // RFC3550-style EWMA of |ΔRTT|
	prevRTTNs  int64
	runLost    int
	runStartNs int64
	window     [statsWindow]struct{ sent, lost, rttSumUs, rttN int64 }
	curSecond  [statsWindow]int64 // which wall-second each window slot holds
	activeSec  int64              // send-second currently being finalized
	lastReply  atomic.Int64       // unixnano of last received echo
}

func NewProber(r Reflector, pps int, onSecond func(SecondBucket), onFreeze func(FreezeEvent), during *atomic.Bool, log *slog.Logger) *Prober {
	if pps <= 0 {
		pps = 50
	}
	return &Prober{
		r: r, pps: pps, timeout: defaultTimeout,
		onSecond: onSecond, onFreeze: onFreeze, during: during,
		sent: make(map[uint32]int64), rtts: make(map[uint32]int64),
		next: 1, lastSeq: 1,
		log: log.With("component", "callprobe", "reflector", r.Name),
	}
}

// dialRTPStyle binds a local port in the RTP range (10000–20000) so the
// flow looks like a real call to port-classifying middleboxes.
func dialRTPStyle(host string) (*net.UDPConn, error) {
	raddr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	for i := 0; i < 20; i++ {
		laddr := &net.UDPAddr{Port: 10000 + rand.Intn(10000)}
		if conn, err := net.DialUDP("udp", laddr, raddr); err == nil {
			return conn, nil
		}
	}
	// all random ports busy (wildly unlikely): let the kernel pick
	return net.DialUDP("udp", nil, raddr)
}

// Run sends until ctx is cancelled. Run under a supervisor: any socket
// error returns and the supervisor restarts with backoff (which also
// re-resolves DNS).
func (p *Prober) Run(ctx context.Context) error {
	conn, err := dialRTPStyle(p.r.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close() // unblocks reads/writes
	}()

	if err := p.handshake(conn); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	errc := make(chan error, 2)
	go p.sendLoop(ctx, conn, errc)
	go p.recvLoop(ctx, conn, errc)

	start := time.Now()
	sweep := time.NewTicker(100 * time.Millisecond)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case <-sweep.C:
			now := time.Now()
			p.finalize(now.UnixNano())
			// no-echo watchdog: a reflector restart invalidates our
			// token (its HMAC key is per-boot). Flush any in-progress
			// freeze as evidence, then restart to re-handshake.
			lr := p.lastReply.Load()
			silent := lr == 0 || now.Sub(time.Unix(0, lr)) > watchdogSilence
			if now.Sub(start) > watchdogSilence && silent {
				p.flushRun()
				return fmt.Errorf("no echoes for %s; re-handshaking", watchdogSilence)
			}
		}
	}
}

// watchdogSilence is how long the prober tolerates zero echoes before
// assuming its token is stale and re-handshaking.
const watchdogSilence = 15 * time.Second

// handshake sends HELLO and waits for the TOKEN reply.
func (p *Prober) handshake(conn *net.UDPConn) error {
	hello := make([]byte, PacketSize)
	MarshalHello(hello)
	reply := make([]byte, 64)
	lastErr := fmt.Errorf("no token reply")
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := conn.Write(hello); err != nil {
			lastErr = err
			continue
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			conn.SetReadDeadline(deadline)
			n, err := conn.Read(reply)
			if err != nil {
				lastErr = err
				break
			}
			if tok, ok := UnmarshalToken(reply[:n]); ok {
				conn.SetReadDeadline(time.Time{})
				p.token = tok
				return nil
			}
		}
	}
	return fmt.Errorf("handshake with %s failed: %w", p.r.Host, lastErr)
}

// flushRun emits an in-progress loss run as a freeze event before the
// prober restarts, so long dead-air periods still land in the log.
func (p *Prober) flushRun() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runLost >= minFreezeRun && p.onFreeze != nil {
		interval := int64(time.Second) / int64(p.pps)
		p.onFreeze(FreezeEvent{
			ReflectorID:     p.r.ID,
			StartedAt:       p.runStartNs / 1e6,
			DurationMs:      int64(p.runLost) * interval / 1e6,
			PacketsLost:     p.runLost,
			DuringSpeedtest: p.during != nil && p.during.Load(),
		})
	}
	p.runLost = 0
}

func (p *Prober) sendLoop(ctx context.Context, conn *net.UDPConn, errc chan<- error) {
	interval := time.Second / time.Duration(p.pps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	buf := make([]byte, PacketSize)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			p.mu.Lock()
			seq := p.lastSeq
			p.lastSeq++
			p.sent[seq] = now
			p.mu.Unlock()
			MarshalProbe(buf, p.token, seq, now)
			if _, err := conn.Write(buf); err != nil {
				if ctx.Err() == nil {
					errc <- fmt.Errorf("send: %w", err)
				}
				return
			}
		}
	}
}

func (p *Prober) recvLoop(ctx context.Context, conn *net.UDPConn, errc chan<- error) {
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if ctx.Err() == nil {
				errc <- fmt.Errorf("recv: %w", err)
			}
			return
		}
		seq, sendNano, ok := UnmarshalProbe(buf[:n])
		if !ok {
			continue
		}
		now := time.Now().UnixNano()
		p.lastReply.Store(now)
		p.mu.Lock()
		if _, awaiting := p.sent[seq]; awaiting {
			p.rtts[seq] = now - sendNano
		}
		p.mu.Unlock()
	}
}

// finalize advances the in-order finalization pointer: a seq resolves as
// received once its echo arrived, or as lost once it is older than the
// timeout. Processing strictly in order makes gap (freeze) detection
// exact even when echoes return out of order.
func (p *Prober) finalize(nowNano int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.next < p.lastSeq {
		seq := p.next
		sendNano, ok := p.sent[seq]
		if !ok { // should not happen; skip defensively
			p.next++
			continue
		}
		if rtt, received := p.rtts[seq]; received {
			p.closeRun(sendNano)
			p.accountReceived(sendNano, rtt)
		} else if nowNano-sendNano > int64(p.timeout) {
			p.accountLost(sendNano)
		} else {
			break // still in flight; later seqs must wait
		}
		delete(p.sent, seq)
		delete(p.rtts, seq)
		p.next++
	}
}

func (p *Prober) accountReceived(sendNano, rttNs int64) {
	if p.prevRTTNs > 0 {
		d := math.Abs(float64(rttNs - p.prevRTTNs))
		p.jitterNs += (d - p.jitterNs) / 16 // RFC 3550 §6.4.1 estimator
	}
	p.prevRTTNs = rttNs
	w := p.bucketFor(sendNano)
	w.sent++
	w.rttSumUs += rttNs / 1000
	w.rttN++
}

func (p *Prober) accountLost(sendNano int64) {
	if p.runLost == 0 {
		p.runStartNs = sendNano
	}
	p.runLost++
	w := p.bucketFor(sendNano)
	w.sent++
	w.lost++
}

// closeRun ends an active loss run (a received packet arrived after it).
func (p *Prober) closeRun(endSendNano int64) {
	if p.runLost >= minFreezeRun && p.onFreeze != nil {
		p.onFreeze(FreezeEvent{
			ReflectorID:     p.r.ID,
			StartedAt:       p.runStartNs / 1e6,
			DurationMs:      (endSendNano - p.runStartNs) / 1e6,
			PacketsLost:     p.runLost,
			DuringSpeedtest: p.during != nil && p.during.Load(),
		})
	}
	p.runLost = 0
}

// bucketFor returns the rolling-window slot for a send timestamp.
// Finalization is strictly send-ordered, so when the send-second
// advances, the previous second is complete and can be emitted (its
// slot keeps its counts for the rolling window until the ring reuses
// it ten seconds later).
func (p *Prober) bucketFor(sendNano int64) *struct{ sent, lost, rttSumUs, rttN int64 } {
	sec := sendNano / 1e9
	if p.activeSec != 0 && sec != p.activeSec {
		prevIdx := int(p.activeSec % statsWindow)
		p.emitSecond(p.activeSec, &p.window[prevIdx])
	}
	p.activeSec = sec
	idx := int(sec % statsWindow)
	w := &p.window[idx]
	// ring wrapped onto an old second: reset the slot for the new one
	if p.curSecond[idx] != sec {
		*w = struct{ sent, lost, rttSumUs, rttN int64 }{}
		p.curSecond[idx] = sec
	}
	return w
}

func (p *Prober) emitSecond(sec int64, w *struct{ sent, lost, rttSumUs, rttN int64 }) {
	if p.onSecond == nil || w.sent == 0 {
		return
	}
	var rttAvg int64
	if w.rttN > 0 {
		rttAvg = w.rttSumUs / w.rttN
	}
	lossPct, rttMs, jitterMs := p.windowStatsLocked()
	p.onSecond(SecondBucket{
		ReflectorID:     p.r.ID,
		TS:              sec * 1000,
		Sent:            int(w.sent),
		Lost:            int(w.lost),
		RTTAvgUs:        rttAvg,
		JitterUs:        int64(p.jitterNs / 1000),
		MOSx100:         int(mos(lossPct, rttMs, jitterMs) * 100),
		DuringSpeedtest: p.during != nil && p.during.Load(),
	})
}

// windowStatsLocked aggregates the rolling window. Caller holds p.mu.
func (p *Prober) windowStatsLocked() (lossPct, rttMs, jitterMs float64) {
	var sent, lost, rttSum, rttN int64
	for i := range p.window {
		sent += p.window[i].sent
		lost += p.window[i].lost
		rttSum += p.window[i].rttSumUs
		rttN += p.window[i].rttN
	}
	if sent > 0 {
		lossPct = 100 * float64(lost) / float64(sent)
	}
	if rttN > 0 {
		rttMs = float64(rttSum/rttN) / 1000
	}
	return lossPct, rttMs, p.jitterNs / 1e6
}

// Snapshot returns the live view for the dashboard.
func (p *Prober) Snapshot() Snapshot {
	p.mu.Lock()
	lossPct, rttMs, jitterMs := p.windowStatsLocked()
	inFreeze := p.runLost >= minFreezeRun
	p.mu.Unlock()
	alive := time.Since(time.Unix(0, p.lastReply.Load())) < 5*time.Second
	s := Snapshot{
		ReflectorID: p.r.ID, Name: p.r.Name, Host: p.r.Host,
		RTTMs: rttMs, JitterMs: jitterMs, LossPct: lossPct,
		InFreeze: inFreeze, Alive: alive,
	}
	if alive {
		s.MOS = math.Round(mos(lossPct, rttMs, jitterMs)*100) / 100
	} else {
		s.MOS = 1
	}
	return s
}

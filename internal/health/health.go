// Package health turns rolling-window path metrics into a single verdict:
// is the network ok, and if not, whose fault is it.
//
// There is exactly one evaluator and it lives here, because there used to be
// none. The dashboard decided "where is the problem" inside its own renderer
// (localizeFault in pathviz.ts) and only ever spoke when the internet tier
// was unhealthy: a LAN at 95ms and 39% loss scored `warn`, the internet
// anchors were fine, every branch fell through, and the page said nothing.
// Silence reads as health. Treating the LAN as a modifier on internet health
// instead of a condition in its own right is what made that possible.
//
// So the rules are ordered, each tier is a condition on its own terms, and
// the decision is made exactly once. Both the glance endpoint and the
// dashboard read that verdict; each phrases it for its own audience, but
// neither decides for itself whether the network is broken.
package health

// Status is the overall verdict. Unknown means the prober has stopped
// reporting, which is not the same as healthy and must never render as such.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
	StatusUnknown  Status = "unknown"
)

// Hop is which segment to blame. Nil when there is nothing to blame.
type Hop string

const (
	HopLAN      Hop = "lan"
	HopISP      Hop = "isp"
	HopInternet Hop = "internet"
)

// WindowSeconds is the rolling window the glance metrics are computed over.
//
// 60s rather than the dashboard's 5 minutes: this answers "is it bad right
// now" for a panel someone glances at, and a five-minute mean takes minutes
// to admit an outage started and minutes more to admit it ended.
const WindowSeconds = 60

// Thresholds are the numbers that decide a verdict.
//
// Settings-backed rather than constants, so the one place to tune them is
// the one place that already persists tuning. The defaults below are the
// starting point, not the contract.
type Thresholds struct {
	// LAN. Absolute, not a multiple of baseline: a baseline-relative rule
	// cannot say that 95ms on a local gateway is wrong, because it is only
	// wrong compared to what a LAN should be, not to what this LAN was.
	LANDownLossPct     float64 `json:"lan_down_loss_pct"`
	LANDegradedMs      float64 `json:"lan_degraded_ms"`
	LANDegradedLossPct float64 `json:"lan_degraded_loss_pct"`

	// Internet.
	InternetDownLossPct     float64 `json:"internet_down_loss_pct"`
	InternetDownMs          float64 `json:"internet_down_ms"`
	InternetDegradedLossPct float64 `json:"internet_degraded_loss_pct"`
	InternetDegradedMs      float64 `json:"internet_degraded_ms"`
	InternetDegradedJitter  float64 `json:"internet_degraded_jitter_ms"`

	// StaleAfterSeconds is how long without a completed probe cycle before
	// the answer becomes "unknown".
	StaleAfterSeconds int `json:"stale_after_s"`
}

// Defaults are the thresholds from the spec.
func Defaults() Thresholds {
	return Thresholds{
		LANDownLossPct:          20,
		LANDegradedMs:           10,
		LANDegradedLossPct:      2,
		InternetDownLossPct:     5,
		InternetDownMs:          150,
		InternetDegradedLossPct: 1,
		InternetDegradedMs:      60,
		InternetDegradedJitter:  30,
		StaleAfterSeconds:       15,
	}
}

// Metrics is the window summarised. Pointer fields are nil when nothing
// could be measured, which is not zero: no successful probe to the LAN is a
// different fact from a 0ms LAN, and the two must not collapse.
type Metrics struct {
	Stale bool

	LANMs      *float64
	LANLossPct *float64
	// LANUnreachable is the outage detector's verdict, not an inference
	// from loss. A target can be at 100% loss inside the window and not yet
	// be declared down, and vice versa.
	LANUnreachable bool

	// ISPUnreachable is true when an ISP-tier hop is down. On its own it
	// means little: those routers deprioritise or rate-limit ICMP, so a
	// silent one is normal while traffic through it is fine.
	ISPUnreachable bool

	InternetMs      *float64
	InternetLossPct *float64
	JitterMs        *float64
}

// Verdict is the answer. Hop is nil when Status is ok or unknown.
type Verdict struct {
	Status Status
	Hop    *Hop
}

func hop(h Hop) *Hop { return &h }

// internetVerdict grades the internet tier alone.
func internetVerdict(m Metrics, t Thresholds) Status {
	if m.InternetLossPct != nil {
		if *m.InternetLossPct >= t.InternetDownLossPct {
			return StatusDown
		}
	}
	if m.InternetMs != nil && *m.InternetMs >= t.InternetDownMs {
		return StatusDown
	}
	if m.InternetLossPct != nil && *m.InternetLossPct > t.InternetDegradedLossPct {
		return StatusDegraded
	}
	if m.InternetMs != nil && *m.InternetMs > t.InternetDegradedMs {
		return StatusDegraded
	}
	if m.JitterMs != nil && *m.JitterMs > t.InternetDegradedJitter {
		return StatusDegraded
	}
	return StatusOK
}

// Evaluate applies the rules in order; the first match wins.
//
// The order is the point. Nearest-hop-first means a broken LAN is reported
// as a broken LAN even when everything beyond it is fine, which is the case
// the previous dashboard logic could not express.
func Evaluate(m Metrics, t Thresholds) Verdict {
	if m.Stale {
		return Verdict{Status: StatusUnknown}
	}

	// LAN.
	if m.LANUnreachable {
		return Verdict{Status: StatusDown, Hop: hop(HopLAN)}
	}
	if m.LANLossPct != nil && *m.LANLossPct >= t.LANDownLossPct {
		return Verdict{Status: StatusDown, Hop: hop(HopLAN)}
	}
	if m.LANMs != nil && *m.LANMs > t.LANDegradedMs {
		return Verdict{Status: StatusDegraded, Hop: hop(HopLAN)}
	}
	if m.LANLossPct != nil && *m.LANLossPct > t.LANDegradedLossPct {
		return Verdict{Status: StatusDegraded, Hop: hop(HopLAN)}
	}

	net := internetVerdict(m, t)

	// ISP, only corroborated. An unreachable ISP hop while the internet
	// beyond it is healthy is a quiet router, not an outage.
	if m.ISPUnreachable && net != StatusOK {
		return Verdict{Status: StatusDown, Hop: hop(HopISP)}
	}

	if net != StatusOK {
		return Verdict{Status: net, Hop: hop(HopInternet)}
	}
	return Verdict{Status: StatusOK}
}

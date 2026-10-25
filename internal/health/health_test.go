package health

import "testing"

func f(v float64) *float64 { return &v }

func TestEvaluate(t *testing.T) {
	th := Defaults()
	cases := []struct {
		name string
		m    Metrics
		want Status
		hop  *Hop
	}{
		// The bug this package exists for. A LAN at 95ms and 39% loss with
		// healthy internet used to produce no message at all, because the
		// old logic only spoke when the internet tier was unhealthy.
		{
			name: "tuesday: bad LAN, healthy internet",
			m: Metrics{
				LANMs: f(95), LANLossPct: f(39),
				InternetMs: f(25), InternetLossPct: f(0), JitterMs: f(2),
			},
			want: StatusDown, hop: hop(HopLAN),
		},
		{
			name: "all healthy",
			m: Metrics{
				LANMs: f(1.3), LANLossPct: f(0),
				InternetMs: f(25.4), InternetLossPct: f(0), JitterMs: f(2.1),
			},
			want: StatusOK, hop: nil,
		},
		{
			name: "stale beats everything, including a dead LAN",
			m:    Metrics{Stale: true, LANUnreachable: true, LANLossPct: f(100)},
			want: StatusUnknown, hop: nil,
		},
		{
			name: "LAN latency alone degrades",
			m: Metrics{
				LANMs: f(11), LANLossPct: f(0),
				InternetMs: f(25), InternetLossPct: f(0),
			},
			want: StatusDegraded, hop: hop(HopLAN),
		},
		{
			name: "LAN loss at the degraded edge is not down",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(19.9),
				InternetMs: f(25), InternetLossPct: f(0),
			},
			want: StatusDegraded, hop: hop(HopLAN),
		},
		{
			name: "LAN beats internet when both are bad",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(25),
				InternetMs: f(400), InternetLossPct: f(50),
			},
			want: StatusDown, hop: hop(HopLAN),
		},
		{
			name: "quiet ISP hop alone is not an outage",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(0), ISPUnreachable: true,
				InternetMs: f(25), InternetLossPct: f(0), JitterMs: f(2),
			},
			want: StatusOK, hop: nil,
		},
		{
			name: "quiet ISP hop plus bad internet is the ISP",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(0), ISPUnreachable: true,
				InternetMs: f(25), InternetLossPct: f(3),
			},
			want: StatusDown, hop: hop(HopISP),
		},
		{
			name: "internet loss down",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(0),
				InternetMs: f(25), InternetLossPct: f(5),
			},
			want: StatusDown, hop: hop(HopInternet),
		},
		{
			name: "jitter alone degrades",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(0),
				InternetMs: f(25), InternetLossPct: f(0), JitterMs: f(31),
			},
			want: StatusDegraded, hop: hop(HopInternet),
		},
		{
			name: "no LAN target configured is not a LAN fault",
			m: Metrics{
				InternetMs: f(25), InternetLossPct: f(0), JitterMs: f(2),
			},
			want: StatusOK, hop: nil,
		},
		{
			name: "no successful internet probe reads as down, not as zero",
			m: Metrics{
				LANMs: f(1), LANLossPct: f(0),
				InternetMs: nil, InternetLossPct: f(100),
			},
			want: StatusDown, hop: hop(HopInternet),
		},
		{
			name: "detector says LAN down even with little measured loss",
			m: Metrics{
				LANUnreachable: true, LANLossPct: f(0),
				InternetMs: f(25), InternetLossPct: f(0),
			},
			want: StatusDown, hop: hop(HopLAN),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(c.m, th)
			if got.Status != c.want {
				t.Errorf("status = %q, want %q", got.Status, c.want)
			}
			switch {
			case c.hop == nil && got.Hop != nil:
				t.Errorf("hop = %q, want nil", *got.Hop)
			case c.hop != nil && got.Hop == nil:
				t.Errorf("hop = nil, want %q", *c.hop)
			case c.hop != nil && *got.Hop != *c.hop:
				t.Errorf("hop = %q, want %q", *got.Hop, *c.hop)
			}
		})
	}
}

// The thresholds are settings, so a tuned threshold must actually change
// the verdict rather than being read once and baked in.
func TestThresholdsApply(t *testing.T) {
	m := Metrics{LANMs: f(1), LANLossPct: f(0), InternetMs: f(80), InternetLossPct: f(0)}
	if got := Evaluate(m, Defaults()); got.Status != StatusDegraded {
		t.Fatalf("with defaults: got %q, want degraded", got.Status)
	}
	relaxed := Defaults()
	relaxed.InternetDegradedMs = 100
	if got := Evaluate(m, relaxed); got.Status != StatusOK {
		t.Fatalf("with a relaxed threshold: got %q, want ok", got.Status)
	}
}

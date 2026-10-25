package api

import (
	"math"
	"net/http"
	"sort"
	"time"

	"pingway.net/pingway/internal/health"
	"pingway.net/pingway/internal/outage"
	"pingway.net/pingway/internal/store"
)

// glanceResponse is the whole contract for a panel that renders one line.
//
// Field names are deliberately flat and stable: a firmware build that reads
// this is not going to be redeployed because a key moved.
type glanceResponse struct {
	Status   health.Status `json:"status"`
	WorstHop *health.Hop   `json:"worst_hop"`

	LANMs      *float64 `json:"lan_ms"`
	LANLossPct *float64 `json:"lan_loss_pct"`

	InternetMs *float64 `json:"internet_ms"`
	LossPct    *float64 `json:"loss_pct"`
	JitterMs   *float64 `json:"jitter_ms"`

	WindowS   int    `json:"window_s"`
	UpdatedAt string `json:"updated_at"`
	Stale     bool   `json:"stale"`
}

// round1 keeps the payload readable. A panel showing 25.43871ms is showing
// precision the measurement does not have.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

func ptr(v float64) *float64 { return &v }

// windowStats reduces one target's window to a median RTT and a loss
// percentage.
//
// during-speedtest samples are excluded, matching live.Stats: drops under a
// line we are deliberately saturating are self-inflicted, and counting them
// would make every scheduled speed test look like an outage.
//
// Median rather than mean: one 900ms retransmit should not move the number a
// person is using to decide whether the network is fine.
func windowStats(samples []store.Sample) (med *float64, lossPct *float64, rtts []float64) {
	var sent, lost int
	for _, s := range samples {
		if s.DuringSpeedtest {
			continue
		}
		sent++
		if s.Success {
			rtts = append(rtts, float64(s.RTTMicros)/1000)
		} else {
			lost++
		}
	}
	if sent == 0 {
		return nil, nil, nil
	}
	lp := 100 * float64(lost) / float64(sent)
	lossPct = &lp
	if len(rtts) == 0 {
		return nil, lossPct, nil
	}
	sorted := append([]float64(nil), rtts...)
	sort.Float64s(sorted)
	m := sorted[len(sorted)/2]
	return &m, lossPct, rtts
}

// jitter is the mean absolute difference between consecutive RTTs, in
// arrival order. Needs two samples to mean anything.
func jitter(rtts []float64) *float64 {
	if len(rtts) < 2 {
		return nil
	}
	var sum float64
	for i := 1; i < len(rtts); i++ {
		sum += math.Abs(rtts[i] - rtts[i-1])
	}
	j := sum / float64(len(rtts)-1)
	return &j
}

// handleGlance answers "is the network ok right now, and whose fault is it".
//
// Everything comes from the tracker's in-memory window plus the outage
// detector's current states. No probe is run on request and no sample table
// is queried, so a panel polling this every second costs nothing and cannot
// slow the prober down. The one database read is the target list itself, a
// handful of rows, which /api/status already does per request.
func (s *Server) handleGlance(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.ListTargets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The settings manager is optional on this server (see handleGetSettings),
	// so fall back rather than dereference. Defaults are a valid answer; a
	// panic on the endpoint a wall panel polls every second is not.
	th := health.Defaults()
	if s.settings != nil {
		th = s.settings.Get().HealthThresholds
	}
	now := time.Now()
	since := now.UnixMilli() - int64(health.WindowSeconds)*1000
	states := s.detector.States()

	resp := glanceResponse{WindowS: health.WindowSeconds}
	m := health.Metrics{}

	// LAN: the primary tier-1 target, lowest sort order then lowest id.
	//
	// One target, not the whole tier: the spec's numbers describe a gateway,
	// and a flaky second access point would otherwise be reported as the
	// latency of "the LAN". Which target that is stays stable as long as the
	// ordering does, and the dashboard's path view already shows the rest.
	var lan *store.Target
	for i := range targets {
		t := &targets[i]
		if !t.Enabled || t.Tier != 1 {
			continue
		}
		if lan == nil || t.SortOrder < lan.SortOrder || (t.SortOrder == lan.SortOrder && t.ID < lan.ID) {
			lan = t
		}
	}
	if lan != nil {
		med, loss, _ := windowStats(s.tracker.SamplesSince(lan.ID, since))
		if med != nil {
			m.LANMs = med
			resp.LANMs = ptr(round1(*med))
		}
		if loss != nil {
			m.LANLossPct = loss
			resp.LANLossPct = ptr(round1(*loss))
		}
		m.LANUnreachable = states[lan.ID] == outage.StateDown
	}

	// ISP: any tier-2 hop the detector calls down. Corroboration only; the
	// evaluator will not blame it without the internet also suffering.
	for _, t := range targets {
		if t.Enabled && t.Tier == 2 && states[t.ID] == outage.StateDown {
			m.ISPUnreachable = true
			break
		}
	}

	// Internet: the best tier-3 anchor, not their average.
	//
	// One anchor having a bad minute, or rate-limiting our ICMP, should not
	// paint the whole internet red. Loss is the lowest across anchors for
	// the same reason: it only counts when every path is losing.
	var bestMed *float64
	var bestRTTs []float64
	var bestLoss *float64
	for _, t := range targets {
		if !t.Enabled || t.Tier != 3 {
			continue
		}
		med, loss, rtts := windowStats(s.tracker.SamplesSince(t.ID, since))
		if loss != nil && (bestLoss == nil || *loss < *bestLoss) {
			bestLoss = loss
		}
		if med != nil && (bestMed == nil || *med < *bestMed) {
			bestMed, bestRTTs = med, rtts
		}
	}
	if bestMed != nil {
		m.InternetMs = bestMed
		resp.InternetMs = ptr(round1(*bestMed))
	}
	if bestLoss != nil {
		m.InternetLossPct = bestLoss
		resp.LossPct = ptr(round1(*bestLoss))
	}
	if j := jitter(bestRTTs); j != nil {
		m.JitterMs = j
		resp.JitterMs = ptr(round1(*j))
	}

	// Stale: no sample from any target recently. Checked against the whole
	// tracker rather than one target, because a single dead target is a
	// target problem and a silent tracker is a prober problem.
	last := s.tracker.LastSampleAt()
	staleAfter := int64(th.StaleAfterSeconds) * 1000
	m.Stale = last == 0 || now.UnixMilli()-last > staleAfter
	resp.Stale = m.Stale
	if last > 0 {
		resp.UpdatedAt = time.UnixMilli(last).Format(time.RFC3339)
	}

	v := health.Evaluate(m, th)
	resp.Status, resp.WorstHop = v.Status, v.Hop

	// A glance that is one poll stale is worse than no glance: the whole
	// point is "right now".
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

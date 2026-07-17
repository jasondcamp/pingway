package live

import (
	"testing"

	"pingway.net/pingway/internal/store"
)

func TestStatsExcludesDuringSpeedtestLoss(t *testing.T) {
	tr := NewTracker()
	base := int64(1_000_000)
	// 10 clean successes, then 10 during-speedtest failures
	for i := int64(0); i < 10; i++ {
		tr.Add(store.Sample{TargetID: 1, TS: base + i*1000, RTTMicros: 5000, Success: true})
	}
	for i := int64(10); i < 20; i++ {
		tr.Add(store.Sample{TargetID: 1, TS: base + i*1000, Success: false, DuringSpeedtest: true})
	}
	st := tr.Stats(1)
	if st.LossPct != 0 {
		t.Fatalf("loss = %v, want 0 (speedtest drops are self-inflicted)", st.LossPct)
	}
	if st.Sent != 10 {
		t.Fatalf("sent = %d, want 10 (flagged samples excluded)", st.Sent)
	}
	// a real (unflagged) failure still counts
	tr.Add(store.Sample{TargetID: 1, TS: base + 20_000, Success: false})
	st = tr.Stats(1)
	if st.LossPct == 0 {
		t.Fatal("real loss must still count")
	}
}

func TestStatsAllFlaggedWindow(t *testing.T) {
	tr := NewTracker()
	tr.Add(store.Sample{TargetID: 1, TS: 1000, Success: false, DuringSpeedtest: true})
	st := tr.Stats(1)
	if st.LossPct != 0 || st.Sent != 0 {
		t.Fatalf("all-flagged window: %+v, want zero loss/sent", st)
	}
}

package speedtest

import (
	"context"
	"testing"
	"time"

	"pingway.net/pingway/internal/store"
)

func TestInitialDelayOverdueRunsSoon(t *testing.T) {
	r, st := testRunner(t, false)
	// last test 2h ago with a 45m interval: overdue -> boot grace (90s)
	st.InsertSpeedTest(context.Background(), store.SpeedTestRow{
		Engine: "mock", RanAt: time.Now().Add(-2 * time.Hour).UnixMilli(),
	})
	d := r.initialDelay(context.Background())
	if d != 90*time.Second {
		t.Fatalf("initial delay = %v, want 90s (overdue)", d)
	}
}

func TestInitialDelayRecentTestKeepsSchedule(t *testing.T) {
	r, st := testRunner(t, false)
	// last test 10m ago with a 45m interval: ~35m remaining
	st.InsertSpeedTest(context.Background(), store.SpeedTestRow{
		Engine: "mock", RanAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
	})
	d := r.initialDelay(context.Background())
	if d < 34*time.Minute || d > 36*time.Minute {
		t.Fatalf("initial delay = %v, want ~35m", d)
	}
}

func TestInitialDelayNoHistoryUsesJitteredInterval(t *testing.T) {
	r, _ := testRunner(t, false)
	d := r.initialDelay(context.Background())
	// 45m ±10%
	if d < 40*time.Minute || d > 50*time.Minute {
		t.Fatalf("initial delay = %v, want 45m ±10%%", d)
	}
}

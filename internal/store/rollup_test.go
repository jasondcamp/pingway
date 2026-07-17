package store

import "testing"

func s(ts, rtt int64, ok bool) Sample {
	return Sample{TargetID: 1, TS: ts, RTTMicros: rtt, Success: ok}
}

func TestComputeBucketBasics(t *testing.T) {
	st := ComputeBucket([]Sample{
		s(1, 100, true), s(2, 200, true), s(3, 300, true), s(4, 0, false),
	})
	if st.Sent != 4 || st.Lost != 1 {
		t.Fatalf("sent/lost = %d/%d", st.Sent, st.Lost)
	}
	if st.RTTAvgUs != 200 || st.RTTMinUs != 100 || st.RTTMaxUs != 300 {
		t.Fatalf("avg/min/max = %d/%d/%d", st.RTTAvgUs, st.RTTMinUs, st.RTTMaxUs)
	}
}

func TestComputeBucketP95NearestRank(t *testing.T) {
	// 100 samples with rtt 1..100 -> p95 = 95th value = 95
	var in []Sample
	for i := int64(1); i <= 100; i++ {
		in = append(in, s(i, i, true))
	}
	st := ComputeBucket(in)
	if st.RTTP95Us != 95 {
		t.Fatalf("p95 = %d, want 95", st.RTTP95Us)
	}
	// 20 samples 1..20 -> rank ceil(19) = 19
	st = ComputeBucket(in[:20])
	if st.RTTP95Us != 19 {
		t.Fatalf("p95 = %d, want 19", st.RTTP95Us)
	}
	// single sample
	st = ComputeBucket(in[:1])
	if st.RTTP95Us != 1 {
		t.Fatalf("p95 = %d, want 1", st.RTTP95Us)
	}
}

func TestComputeBucketJitter(t *testing.T) {
	// consecutive successful RTTs 100, 150, 130 -> diffs 50, 20 -> mean 35
	st := ComputeBucket([]Sample{s(1, 100, true), s(2, 150, true), s(3, 130, true)})
	if st.JitterUs != 35 {
		t.Fatalf("jitter = %d, want 35", st.JitterUs)
	}
	// losses between successes don't contribute diffs but don't reset the chain
	st = ComputeBucket([]Sample{s(1, 100, true), s(2, 0, false), s(3, 150, true)})
	if st.JitterUs != 50 {
		t.Fatalf("jitter = %d, want 50", st.JitterUs)
	}
}

func TestComputeBucketAllLost(t *testing.T) {
	st := ComputeBucket([]Sample{s(1, 0, false), s(2, 0, false)})
	if st.Sent != 2 || st.Lost != 2 {
		t.Fatalf("sent/lost = %d/%d", st.Sent, st.Lost)
	}
	if st.RTTAvgUs != 0 || st.RTTP95Us != 0 {
		t.Fatalf("rtt stats should be zero for all-lost bucket")
	}
}

func TestBucketSamples(t *testing.T) {
	in := []Sample{s(59_999, 1, true), s(60_000, 2, true), s(60_001, 3, true), s(120_000, 4, true)}
	keys, buckets := BucketSamples(in, 60_000)
	if len(keys) != 3 {
		t.Fatalf("keys = %v", keys)
	}
	if keys[0] != 0 || keys[1] != 60_000 || keys[2] != 120_000 {
		t.Fatalf("keys = %v", keys)
	}
	if len(buckets[60_000]) != 2 {
		t.Fatalf("bucket 60000 has %d samples", len(buckets[60_000]))
	}
}

package store

import "sort"

// BucketStats is the computed aggregate for one rollup bucket. RTT fields
// are only meaningful when Sent > Lost.
type BucketStats struct {
	Sent     int64
	Lost     int64
	RTTAvgUs int64
	RTTMinUs int64
	RTTMaxUs int64
	RTTP95Us int64
	JitterUs int64
}

// ComputeBucket aggregates raw samples (ordered by ts) into rollup stats.
// p95 uses the nearest-rank method over successful RTTs. Jitter is the
// mean absolute difference of consecutive successful RTTs.
func ComputeBucket(samples []Sample) BucketStats {
	var st BucketStats
	st.Sent = int64(len(samples))
	var rtts []int64
	var sum int64
	prev := int64(-1)
	var jitterSum int64
	var jitterN int64
	for _, s := range samples {
		if !s.Success {
			st.Lost++
			continue
		}
		rtts = append(rtts, s.RTTMicros)
		sum += s.RTTMicros
		if prev >= 0 {
			d := s.RTTMicros - prev
			if d < 0 {
				d = -d
			}
			jitterSum += d
			jitterN++
		}
		prev = s.RTTMicros
	}
	if len(rtts) == 0 {
		return st
	}
	st.RTTAvgUs = sum / int64(len(rtts))
	sorted := append([]int64(nil), rtts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	st.RTTMinUs = sorted[0]
	st.RTTMaxUs = sorted[len(sorted)-1]
	// nearest-rank p95: ceil(0.95 * n), 1-indexed
	rank := (95*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	st.RTTP95Us = sorted[rank-1]
	if jitterN > 0 {
		st.JitterUs = jitterSum / jitterN
	}
	return st
}

// BucketSamples groups ordered samples into fixed-width time buckets.
// Returns bucket start timestamps (ms) in order with their samples.
func BucketSamples(samples []Sample, bucketMs int64) (keys []int64, buckets map[int64][]Sample) {
	buckets = make(map[int64][]Sample)
	for _, s := range samples {
		b := s.TS - s.TS%bucketMs
		if _, ok := buckets[b]; !ok {
			keys = append(keys, b)
		}
		buckets[b] = append(buckets[b], s)
	}
	return keys, buckets
}

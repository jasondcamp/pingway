package speedtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// measureThroughput runs `streams` workers that move bytes (each reporting
// via the shared counter) for `dur`, and computes bps from the bytes moved
// after the ramp-up window.
func measureThroughput(ctx context.Context, streams int, dur, ramp time.Duration,
	worker func(ctx context.Context, counted *atomic.Int64)) (float64, error) {

	var counted atomic.Int64
	wctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(wctx, &counted)
		}()
	}

	var rampBytes int64
	select {
	case <-time.After(ramp):
		rampBytes = counted.Load()
	case <-wctx.Done():
	}
	<-wctx.Done()
	wg.Wait()

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	total := counted.Load()
	window := (dur - ramp).Seconds()
	moved := total - rampBytes
	if moved <= 0 {
		return 0, errors.New("no bytes transferred in measurement window")
	}
	return float64(moved) * 8 / window, nil
}

// countingReader wraps an infinite random-ish payload source, counting
// bytes as they are consumed by the HTTP client during upload.
type countingReader struct {
	ctx     context.Context
	remain  int64
	block   []byte
	off     int
	counted *atomic.Int64
}

func newCountingReader(ctx context.Context, size int64, counted *atomic.Int64) *countingReader {
	block := make([]byte, 64*1024)
	rand.Read(block)
	return &countingReader{ctx: ctx, remain: size, block: block, counted: counted}
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	if r.remain <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remain {
		n = int(r.remain)
	}
	for i := 0; i < n; i++ {
		p[i] = r.block[r.off]
		r.off = (r.off + 1) % len(r.block)
	}
	r.remain -= int64(n)
	r.counted.Add(int64(n))
	return n, nil
}

// measureHTTPLatency issues small requests and returns the median
// round-trip in ms.
func measureHTTPLatency(ctx context.Context, client *http.Client, url string, n int) (float64, error) {
	var times []float64
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("latency probe: %w", err)
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		times = append(times, float64(time.Since(start).Microseconds())/1000)
	}
	if len(times) == 0 {
		return 0, errors.New("no latency samples")
	}
	sort.Float64s(times)
	return times[len(times)/2], nil
}

func drainDownload(ctx context.Context, client *http.Client, url string, counted *atomic.Int64) {
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		buf := make([]byte, 64*1024)
		for {
			n, err := resp.Body.Read(buf)
			counted.Add(int64(n))
			if err != nil {
				break
			}
		}
		resp.Body.Close()
	}
}

// measureUpload measures upload throughput by crediting a chunk's bytes
// only when its HTTP request completes (the server has responded, so the
// bytes genuinely crossed the wire). Counting bytes as they are written
// to the socket — the naive approach — measures the local socket/proxy
// buffer ingestion rate instead, which wildly overreports behind
// buffering middleboxes (Docker Desktop's VM proxy inflated uploads ~5x).
//
// Chunk size adapts upward until a chunk takes >=400ms, keeping the
// end-of-window truncation error small at any line speed. New chunks stop
// at the deadline; in-flight chunks get a grace period to finish and the
// rate divides completed bytes by the actual span they covered.
func measureUpload(ctx context.Context, client *http.Client, url string,
	dur, ramp time.Duration, streams int) (float64, error) {

	deadline := time.Now().Add(dur)
	rampEnd := time.Now().Add(ramp)
	graceCtx, cancelGrace := context.WithDeadline(ctx, deadline.Add(8*time.Second))
	defer cancelGrace()

	var mu sync.Mutex
	var completedBytes int64
	var lastCompletion time.Time

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := int64(512 * 1024)
			var dummy atomic.Int64
			for time.Now().Before(deadline) && graceCtx.Err() == nil {
				start := time.Now()
				body := newCountingReader(graceCtx, chunk, &dummy)
				req, err := http.NewRequestWithContext(graceCtx, http.MethodPost, url, body)
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/octet-stream")
				req.ContentLength = chunk
				resp, err := client.Do(req)
				if err != nil {
					if graceCtx.Err() != nil {
						return
					}
					continue
				}
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()

				now := time.Now()
				if now.After(rampEnd) {
					mu.Lock()
					completedBytes += chunk
					if now.After(lastCompletion) {
						lastCompletion = now
					}
					mu.Unlock()
				}
				if now.Sub(start) < 400*time.Millisecond && chunk < 32*1024*1024 {
					chunk *= 2
				}
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	mu.Lock()
	defer mu.Unlock()
	if completedBytes == 0 || lastCompletion.IsZero() {
		return 0, errors.New("no upload chunks completed in measurement window")
	}
	span := lastCompletion.Sub(rampEnd)
	if span < time.Second {
		span = time.Second
	}
	return float64(completedBytes) * 8 / span.Seconds(), nil
}

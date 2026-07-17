package sse

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestBroadcastReachesClient(t *testing.T) {
	h := NewHub(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := newPipeResponse()
	req := httptest.NewRequest("GET", "/api/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(pw, req)
		close(done)
	}()

	// wait for subscription
	waitFor(t, func() bool { return h.ClientCount() == 1 })
	h.Broadcast(Event{Name: "ping", Data: map[string]int{"x": 1}})

	sc := bufio.NewScanner(pr)
	var got []string
	for sc.Scan() {
		line := sc.Text()
		got = append(got, line)
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"x":1`) {
			break
		}
		if len(got) > 20 {
			t.Fatalf("event not found in stream: %v", got)
		}
	}
	cancel()
	<-done
}

// TestSlowClientEvicted floods the hub past a stalled client's buffer and
// asserts the broadcaster never blocks and the client is dropped.
func TestSlowClientEvicted(t *testing.T) {
	h := NewHub(testLogger())
	ctx := context.Background()

	// stalled client: its response writer blocks forever on write
	blocked := make(chan struct{})
	pw := &blockingWriter{unblock: blocked}
	req := httptest.NewRequest("GET", "/api/stream", nil).WithContext(ctx)
	served := make(chan struct{})
	go func() {
		h.ServeHTTP(pw, req)
		close(served)
	}()
	waitFor(t, func() bool { return h.ClientCount() == 1 })

	broadcastDone := make(chan struct{})
	go func() {
		// buffer is 64; the first event may be stuck in the blocked Write,
		// so send enough to overflow regardless
		for i := 0; i < 200; i++ {
			h.Broadcast(Event{Name: "ping", Data: i})
		}
		close(broadcastDone)
	}()

	select {
	case <-broadcastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcaster blocked on a slow client")
	}
	waitFor(t, func() bool { return h.ClientCount() == 0 })
	close(blocked)
	<-served
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- test doubles ---

func newPipeResponse() (*pipeReader, *pipeWriter) {
	ch := make(chan []byte, 256)
	return &pipeReader{ch: ch}, &pipeWriter{ResponseRecorder: httptest.NewRecorder(), ch: ch}
}

type pipeReader struct {
	ch  chan []byte
	buf []byte
}

func (r *pipeReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		r.buf = <-r.ch
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

type pipeWriter struct {
	*httptest.ResponseRecorder
	ch chan []byte
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	w.ch <- b
	return len(p), nil
}

func (w *pipeWriter) Flush() {}

// blockingWriter stalls forever on Write, simulating a dead TCP peer.
type blockingWriter struct {
	*httptest.ResponseRecorder
	unblock chan struct{}
	wrote   bool
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	if w.ResponseRecorder == nil {
		w.ResponseRecorder = httptest.NewRecorder()
	}
	if w.wrote {
		<-w.unblock
	}
	w.wrote = true
	return len(p), nil
}

func (w *blockingWriter) Header() http.Header {
	if w.ResponseRecorder == nil {
		w.ResponseRecorder = httptest.NewRecorder()
	}
	return w.ResponseRecorder.Header()
}

func (w *blockingWriter) WriteHeader(code int) {}

func (w *blockingWriter) Flush() {}

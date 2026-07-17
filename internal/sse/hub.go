// Package sse implements a broadcast hub for Server-Sent Events with
// per-client buffered channels and slow-client eviction.
package sse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// Event is one SSE message. Data is JSON-encoded at broadcast time.
type Event struct {
	Name string
	Data any
}

type client struct {
	ch chan []byte
}

// Hub fans events out to subscribed HTTP clients. Broadcast never blocks:
// a client whose buffer is full is evicted.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	log     *slog.Logger
	bufSize int
}

func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[*client]struct{}),
		log:     log.With("component", "sse"),
		bufSize: 64,
	}
}

// Broadcast encodes and queues the event for every client. Clients that
// cannot keep up are dropped immediately.
func (h *Hub) Broadcast(ev Event) {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		h.log.Error("marshal event", "event", ev.Name, "err", err)
		return
	}
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", ev.Name, data))

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.ch <- frame:
		default:
			// Slow client: evict. Closing the channel signals its serve
			// loop to return.
			delete(h.clients, c)
			close(c.ch)
			h.log.Warn("evicted slow sse client")
		}
	}
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// ServeHTTP streams events to one client until it disconnects or is
// evicted.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Tell the client how quickly to reconnect after a drop.
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	c := &client{ch: make(chan []byte, h.bufSize)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if _, still := h.clients[c]; still {
			delete(h.clients, c)
			close(c.ch)
		}
		h.mu.Unlock()
	}()

	for {
		select {
		case frame, ok := <-c.ch:
			if !ok {
				return // evicted
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

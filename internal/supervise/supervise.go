// Package supervise runs long-lived goroutines, recovering panics and
// restarting with backoff. A silently dead pinger goroutine is the worst
// failure mode this app can have.
package supervise

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

type Supervisor struct {
	log     *slog.Logger
	wg      sync.WaitGroup
	running atomic.Int64
}

func New(log *slog.Logger) *Supervisor {
	return &Supervisor{log: log.With("component", "supervisor")}
}

// Go runs fn under supervision until ctx is cancelled. Panics are recovered
// and logged; fn is restarted with exponential backoff (250ms..30s),
// resetting after a minute of healthy runtime. fn returning nil after ctx
// cancellation ends supervision; a non-nil error triggers a restart too.
func (s *Supervisor) Go(ctx context.Context, name string, fn func(context.Context) error) {
	s.wg.Add(1)
	s.running.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.running.Add(-1)
		backoff := 250 * time.Millisecond
		const maxBackoff = 30 * time.Second
		for {
			started := time.Now()
			err := s.runOnce(ctx, name, fn)
			if ctx.Err() != nil {
				return
			}
			if time.Since(started) > time.Minute {
				backoff = 250 * time.Millisecond
			}
			s.log.Error("goroutine exited, restarting", "name", name, "err", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}()
}

func (s *Supervisor) runOnce(ctx context.Context, name string, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			s.log.Error("panic recovered", "name", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	return fn(ctx)
}

// RunningCount reports how many supervised goroutines are alive. Used by
// /healthz.
func (s *Supervisor) RunningCount() int64 { return s.running.Load() }

// Wait blocks until all supervised goroutines have finished (after ctx
// cancellation).
func (s *Supervisor) Wait() { s.wg.Wait() }

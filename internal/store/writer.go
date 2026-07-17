package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Writer is the sole writer of ping samples. It drains a buffered channel
// and flushes batched transactions on an interval — never one transaction
// per sample.
type Writer struct {
	store    *Store
	in       chan Sample
	interval time.Duration
	log      *slog.Logger
}

func NewWriter(s *Store, log *slog.Logger) *Writer {
	return &Writer{
		store:    s,
		in:       make(chan Sample, 4096),
		interval: 1500 * time.Millisecond,
		log:      log.With("component", "writer"),
	}
}

// In returns the channel producers send samples into. If the channel is
// full the producer should drop the sample rather than block.
func (w *Writer) In() chan<- Sample { return w.in }

// Submit enqueues a sample, dropping it (with a log) if the buffer is full.
func (w *Writer) Submit(s Sample) {
	select {
	case w.in <- s:
	default:
		w.log.Warn("sample buffer full, dropping sample", "target_id", s.TargetID)
	}
}

// Run drains and flushes until ctx is cancelled, then does a final flush of
// everything pending. Run under the supervisor.
func (w *Writer) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	batch := make([]Sample, 0, 1024)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.Flush(batch); err != nil {
			w.log.Error("flush failed", "err", err, "samples", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case s := <-w.in:
			batch = append(batch, s)
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// drain whatever is already queued, then final flush
			for {
				select {
				case s := <-w.in:
					batch = append(batch, s)
				default:
					flush()
					return nil
				}
			}
		}
	}
}

func (w *Writer) Flush(batch []Sample) error {
	tx, err := w.store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO ping_samples (target_id, ts, rtt_us, success, during_speedtest) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	for _, s := range batch {
		var rtt any
		if s.Success {
			rtt = s.RTTMicros
		}
		if _, err := stmt.Exec(s.TargetID, s.TS, rtt, s.Success, s.DuringSpeedtest); err != nil {
			stmt.Close()
			tx.Rollback()
			return fmt.Errorf("insert sample: %w", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

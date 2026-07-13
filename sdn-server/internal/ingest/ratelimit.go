package ingest

import (
	"context"
	"sync"
	"time"
)

// rateLimiter enforces Space-Track's hard M2M limits inside the ingest lane
// itself (not the caller's discipline): a minimum gap between consecutive
// requests plus rolling per-minute and per-hour caps. Space-Track documents
// <30 requests/min and <300/hr with suspension risk; the defaults below sit
// safely under both. All Space-Track requests in the supplemental lane pass
// through Wait, so a single shared limiter bounds the whole lane regardless of
// how many files or classes a cycle touches.
type rateLimiter struct {
	mu      sync.Mutex
	minGap  time.Duration
	perMin  int
	perHour time.Duration
	perMinN int
	perHrN  int
	// history holds request timestamps within the last hour (pruned lazily).
	history []time.Time
	last    time.Time

	// injectable for tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newRateLimiter(minGap time.Duration, perMinN, perHrN int) *rateLimiter {
	if minGap < 0 {
		minGap = 0
	}
	if perMinN <= 0 {
		perMinN = 25
	}
	if perHrN <= 0 {
		perHrN = 250
	}
	return &rateLimiter{
		minGap:  minGap,
		perMin:  60,
		perHour: time.Hour,
		perMinN: perMinN,
		perHrN:  perHrN,
		now:     time.Now,
		sleep:   sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wait blocks until a new request may proceed under all limits, then records
// it. It respects context cancellation.
func (l *rateLimiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay, at := l.reserve(l.now())
	if err := l.sleep(ctx, delay); err != nil {
		return err
	}
	l.commit(at)
	return nil
}

// reserve computes how long the caller must wait before issuing the next
// request given the limiter state at time now, and the wall-clock instant that
// request will occur. It does not mutate history (commit does), so a cancelled
// Wait does not consume a slot.
func (l *rateLimiter) reserve(now time.Time) (time.Duration, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)

	earliest := now
	// Minimum inter-request gap.
	if !l.last.IsZero() {
		if gapReady := l.last.Add(l.minGap); gapReady.After(earliest) {
			earliest = gapReady
		}
	}
	// Per-minute rolling window: if the window is full, wait until the oldest
	// in-window request ages out.
	if n := len(l.history); n >= l.perMinN {
		oldest := l.history[n-l.perMinN]
		if ready := oldest.Add(time.Duration(l.perMin) * time.Second); ready.After(earliest) {
			earliest = ready
		}
	}
	// Per-hour rolling window.
	if n := len(l.history); n >= l.perHrN {
		oldest := l.history[n-l.perHrN]
		if ready := oldest.Add(l.perHour); ready.After(earliest) {
			earliest = ready
		}
	}

	delay := earliest.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, earliest
}

func (l *rateLimiter) commit(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(at)
	l.history = append(l.history, at)
	l.last = at
}

// pruneLocked drops timestamps older than one hour. Callers hold l.mu.
func (l *rateLimiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.perHour)
	i := 0
	for i < len(l.history) && !l.history[i].After(cutoff) {
		i++
	}
	if i > 0 {
		l.history = append(l.history[:0], l.history[i:]...)
	}
}

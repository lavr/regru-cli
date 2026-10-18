package regruapi

import (
	"context"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
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

type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	clock    Clock
	next     time.Time
}

func NewLimiter(interval time.Duration, c Clock) *Limiter {
	if c == nil {
		c = SystemClock{}
	}
	return &Limiter{interval: interval, clock: c}
}

func (l *Limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	now := l.clock.Now()
	slot := now
	if l.next.After(now) {
		slot = l.next
	}
	l.next = slot.Add(l.interval)
	wait := slot.Sub(now)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	return l.clock.Sleep(ctx, wait)
}

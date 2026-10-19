package regruapi

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1000, 0)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	return nil
}

func (f *fakeClock) total() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t time.Duration
	for _, d := range f.sleeps {
		t += d
	}
	return t
}

func (f *fakeClock) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}

type blockingClock struct {
	now time.Time
}

func (b *blockingClock) Now() time.Time { return b.now }

func (b *blockingClock) Sleep(ctx context.Context, d time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLimiterSpacing(t *testing.T) {
	clk := newFakeClock()
	l := NewLimiter(3*time.Second, clk)
	for i := 0; i < 4; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.total(); got != 9*time.Second {
		t.Fatalf("total sleep %v", got)
	}
}

func TestLimiterConcurrent(t *testing.T) {
	clk := newFakeClock()
	l := NewLimiter(3*time.Second, clk)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := clk.total(); got < 12*time.Second {
		t.Fatalf("total sleep %v", got)
	}
}

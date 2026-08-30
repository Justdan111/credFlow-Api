package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryAllowsUpToLimitThenDenies(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		res, err := m.Allow(ctx, "k", 3, time.Minute)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("request %d denied, want allowed", i)
		}
		if want := 3 - i; res.Remaining != want {
			t.Errorf("request %d remaining = %d, want %d", i, res.Remaining, want)
		}
	}

	res, _ := m.Allow(ctx, "k", 3, time.Minute)
	if res.Allowed {
		t.Fatal("the fourth request was allowed past a limit of 3")
	}
	if res.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want positive so a client knows when to retry", res.RetryAfter)
	}
}

// Keys must not interfere. This is what stops two users behind one NAT from
// consuming each other's login allowance.
func TestMemoryKeysAreIndependent(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		m.Allow(ctx, "user-a", 3, time.Minute)
	}
	if res, _ := m.Allow(ctx, "user-a", 3, time.Minute); res.Allowed {
		t.Fatal("user-a was not limited")
	}

	res, _ := m.Allow(ctx, "user-b", 3, time.Minute)
	if !res.Allowed {
		t.Fatal("user-b was limited by user-a's traffic — keys are leaking")
	}
}

// A sliding window must not let a caller double the rate across a boundary,
// which is exactly what a fixed-window counter permits.
func TestMemoryWindowSlides(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	const window = 100 * time.Millisecond

	for i := 0; i < 2; i++ {
		m.Allow(ctx, "k", 2, window)
	}
	if res, _ := m.Allow(ctx, "k", 2, window); res.Allowed {
		t.Fatal("allowed past the limit inside the window")
	}

	time.Sleep(window + 20*time.Millisecond)

	if res, _ := m.Allow(ctx, "k", 2, window); !res.Allowed {
		t.Fatal("still denied after the window elapsed")
	}
}

// RetryAfter must point at when the oldest live hit expires, not the full window.
func TestMemoryRetryAfterShrinksAsTheWindowAges(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	const window = 200 * time.Millisecond

	m.Allow(ctx, "k", 1, window)
	first, _ := m.Allow(ctx, "k", 1, window)

	time.Sleep(80 * time.Millisecond)
	second, _ := m.Allow(ctx, "k", 1, window)

	if second.RetryAfter >= first.RetryAfter {
		t.Errorf("RetryAfter did not shrink: %v then %v", first.RetryAfter, second.RetryAfter)
	}
}

func TestMemoryZeroLimitAllowsEverything(t *testing.T) {
	m := NewMemory()
	// A zero limit means "not configured", not "block everything" — a
	// misconfiguration must not take the endpoint offline.
	for i := 0; i < 100; i++ {
		if res, _ := m.Allow(context.Background(), "k", 0, time.Minute); !res.Allowed {
			t.Fatal("a zero limit blocked a request")
		}
	}
}

// Buckets must not accumulate for every address that has ever connected.
func TestMemorySweepEvictsIdleBuckets(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		m.Allow(ctx, fmt.Sprintf("key-%d", i), 5, time.Minute)
	}
	if m.Len() != 50 {
		t.Fatalf("tracked %d keys, want 50", m.Len())
	}

	// Nothing is idle yet.
	if removed := m.Sweep(time.Hour); removed != 0 {
		t.Errorf("swept %d buckets that were still fresh", removed)
	}

	time.Sleep(30 * time.Millisecond)
	if removed := m.Sweep(10 * time.Millisecond); removed != 50 {
		t.Errorf("swept %d buckets, want 50", removed)
	}
	if m.Len() != 0 {
		t.Errorf("%d buckets remain after a full sweep", m.Len())
	}
}

// The limiter is shared across every request, so concurrent access must be safe
// and must not over-admit. Run with -race.
func TestMemoryIsConcurrencySafe(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	const goroutines, limit = 50, 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, _ := m.Allow(ctx, "shared", limit, time.Minute); res.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != limit {
		t.Fatalf("%d of %d concurrent requests were allowed, want exactly %d",
			allowed, goroutines, limit)
	}
}

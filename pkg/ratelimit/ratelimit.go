// Package ratelimit throttles abusive traffic.
//
// The interface exists so the storage can change without touching callers:
// an in-memory implementation now, Redis when the API runs more than one
// replica. Same pattern as pkg/mailer.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Result describes one decision. Remaining and RetryAfter feed the response
// headers, so a well-behaved client can back off instead of hammering.
type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// Memory is a sliding-window-log limiter.
//
// It stores the timestamps of recent hits per key rather than a fixed-window
// counter. A fixed window lets a caller fire `limit` requests at 0:59 and
// `limit` more at 1:01 — double the intended rate across the boundary. Keeping
// the timestamps costs at most `limit` entries per key and has no such edge.
//
// LIMITATION: counters live in this process. With N replicas the effective
// limit is roughly N times what is configured, and a restart clears them. That
// is a real weakness, documented rather than hidden — and still far better than
// no limit at all on a single instance.
type Memory struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	hits     []time.Time
	lastSeen time.Time
}

func NewMemory() *Memory {
	return &Memory{buckets: make(map[string]*bucket)}
}

func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 {
		return Result{Allowed: true, Remaining: 0}, nil
	}

	now := time.Now()
	cutoff := now.Add(-window)

	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{hits: make([]time.Time, 0, limit)}
		m.buckets[key] = b
	}
	b.lastSeen = now

	// Drop hits that have aged out of the window. The slice is ordered, so the
	// first index still inside the window is where the live run begins.
	i := 0
	for i < len(b.hits) && b.hits[i].Before(cutoff) {
		i++
	}
	b.hits = b.hits[i:]

	if len(b.hits) >= limit {
		// The oldest live hit is what has to expire before another is allowed.
		retry := b.hits[0].Add(window).Sub(now)
		if retry < 0 {
			retry = 0
		}
		return Result{Allowed: false, Remaining: 0, RetryAfter: retry}, nil
	}

	b.hits = append(b.hits, now)
	return Result{Allowed: true, Remaining: limit - len(b.hits)}, nil
}

// Sweep evicts buckets untouched for longer than idleFor, so memory does not
// grow with every IP that has ever connected. Returns how many were removed.
func (m *Memory) Sweep(idleFor time.Duration) int {
	cutoff := time.Now().Add(-idleFor)

	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for k, b := range m.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(m.buckets, k)
			removed++
		}
	}
	return removed
}

// Len reports the number of tracked keys. Used by tests and diagnostics.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.buckets)
}

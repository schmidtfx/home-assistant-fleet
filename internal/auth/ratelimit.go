package auth

import (
	"sync"
	"time"
)

// RateLimiter blocks a key after max failures within window.
type RateLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
	now      func() time.Time
}

func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	return &RateLimiter{max: max, window: window, failures: map[string][]time.Time{}, now: time.Now}
}

func (l *RateLimiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	fs := l.failures[key]
	i := 0
	for i < len(fs) && fs[i].Before(cutoff) {
		i++
	}
	fs = fs[i:]
	if len(fs) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = fs
	}
	return fs
}

func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) < l.max
}

func (l *RateLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.recent(key), l.now())
}

func (l *RateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *RateLimiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.failures {
		l.recent(k)
	}
}

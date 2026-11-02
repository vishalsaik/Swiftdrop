package auth

import (
	"sync"
	"time"
)

// limiter allows at most max hits per key in any window. It is kept in memory, so
// each server process counts for itself: with several servers the real limit is
// that many times larger. Guessing a password across servers would still need
// more attempts than the window allows, but it is a limit to know about.
type limiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time // oldest first
	max     int
	window  time.Duration
	maxKeys int
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, max: max, window: window, maxKeys: 10_000}
}

// blocked says whether the key has used up its hits, and if so how long until
// one of them ages out of the window.
func (l *limiter) blocked(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.prune(key, now)
	if len(hits) < l.max {
		return 0, false
	}
	return hits[0].Add(l.window).Sub(now), true
}

// hit records one use of the key.
func (l *limiter) hit(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.prune(key, now)
	if _, known := l.hits[key]; !known && len(l.hits) >= l.maxKeys {
		l.evictOldest(now)
	}
	l.hits[key] = append(hits, now)
}

// reset forgets the key, after a success.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// prune drops the key's hits that fell out of the window and returns the rest.
// The caller holds the lock.
func (l *limiter) prune(key string, now time.Time) []time.Time {
	hits := l.hits[key]
	cut := 0
	for cut < len(hits) && !hits[cut].After(now.Add(-l.window)) {
		cut++
	}
	if cut == len(hits) {
		delete(l.hits, key)
		return nil
	}
	if cut > 0 {
		hits = hits[cut:]
		l.hits[key] = hits
	}
	return hits
}

// evictOldest makes room for a new key: it removes keys whose hits have all aged
// out and, if the table is still full, the key used least recently. A key that is
// being hammered is recent, so flooding the table with other keys cannot push it
// out. The caller holds the lock.
func (l *limiter) evictOldest(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k := range l.hits {
		if l.prune(k, now) == nil {
			continue // pruned away
		}
		last := l.hits[k][len(l.hits[k])-1]
		if oldestKey == "" || last.Before(oldest) {
			oldestKey, oldest = k, last
		}
	}
	if len(l.hits) >= l.maxKeys && oldestKey != "" {
		delete(l.hits, oldestKey)
	}
}

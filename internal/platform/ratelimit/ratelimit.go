// Package ratelimit provides a small in-memory fixed-window limiter.
//
// In memory, not Redis: the MVP runs a single instance, and adding a datastore to count
// login attempts would be exactly the infrastructure this project set out not to add
// (SPEC.md section 5). The trade-off is explicit — with more than one instance the
// effective limit multiplies by the instance count. That is the trigger to revisit, and
// the interface here does not change when it happens.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter counts events per key within a fixed window.
type Limiter struct {
	mu        sync.Mutex
	entries   map[string]*entry
	lastSweep time.Time

	limit  int
	window time.Duration

	// now is injectable so tests can advance time without sleeping.
	now func() time.Time
}

type entry struct {
	count     int
	windowEnd time.Time
}

// New returns a limiter allowing limit events per key per window.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		entries: make(map[string]*entry),
		limit:   limit,
		window:  window,
		now:     time.Now,
	}
}

// Allow records an event for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	e, ok := l.entries[key]
	if !ok || now.After(e.windowEnd) {
		l.entries[key] = &entry{count: 1, windowEnd: now.Add(l.window)}
		return true
	}

	e.count++
	return e.count <= l.limit
}

// Reset clears a key's counter. Login calls this on success so a user who mistyped their
// password three times is not locked out immediately after getting it right.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep drops expired entries. Without it the map grows once per distinct key forever,
// which turns the limiter itself into a memory-exhaustion vector — the keys are
// attacker-chosen e-mail addresses and IPs.
//
// Callers must hold the mutex.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now

	for key, e := range l.entries {
		if now.After(e.windowEnd) {
			delete(l.entries, key)
		}
	}
}

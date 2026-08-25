package ratelimit

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// withClock returns a limiter whose time source the test controls.
func withClock(limit int, window time.Duration) (*Limiter, func(time.Duration)) {
	now := time.Now()
	l := New(limit, window)
	l.now = func() time.Time { return now }
	return l, func(d time.Duration) { now = now.Add(d) }
}

func TestAllowUpToLimit(t *testing.T) {
	t.Parallel()

	l, _ := withClock(3, time.Minute)

	for i := 1; i <= 3; i++ {
		if !l.Allow("ana@example.com") {
			t.Fatalf("attempt %d was denied, want allowed", i)
		}
	}
	if l.Allow("ana@example.com") {
		t.Error("the fourth attempt was allowed, want denied")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	t.Parallel()

	l, _ := withClock(1, time.Minute)

	if !l.Allow("ana@example.com") {
		t.Fatal("first key was denied")
	}
	if !l.Allow("bruno@example.com") {
		t.Error("a different key was denied: counters are leaking between keys")
	}
}

func TestWindowExpiry(t *testing.T) {
	t.Parallel()

	l, advance := withClock(1, time.Minute)

	if !l.Allow("ana@example.com") {
		t.Fatal("first attempt was denied")
	}
	if l.Allow("ana@example.com") {
		t.Fatal("second attempt inside the window was allowed")
	}

	advance(time.Minute + time.Second)

	if !l.Allow("ana@example.com") {
		t.Error("attempt after the window expired was denied")
	}
}

// A user who mistypes their password and then gets it right must not stay one attempt
// from a lockout.
func TestResetClearsCounter(t *testing.T) {
	t.Parallel()

	l, _ := withClock(2, time.Minute)

	l.Allow("ana@example.com")
	l.Allow("ana@example.com")
	if l.Allow("ana@example.com") {
		t.Fatal("limit was not enforced before Reset")
	}

	l.Reset("ana@example.com")

	if !l.Allow("ana@example.com") {
		t.Error("Reset did not clear the counter")
	}
}

// Keys are attacker-chosen e-mail addresses and IPs. Without the sweep the map grows
// forever, turning the limiter into a memory-exhaustion vector.
func TestSweepEvictsExpiredEntries(t *testing.T) {
	t.Parallel()

	l, advance := withClock(1, time.Minute)

	for i := range 500 {
		l.Allow("key-" + strconv.Itoa(i))
	}
	if got := len(l.entries); got != 500 {
		t.Fatalf("entries = %d, want 500", got)
	}

	advance(2 * time.Minute)
	l.Allow("gatilho-do-sweep")

	if got := len(l.entries); got != 1 {
		t.Errorf("entries after sweep = %d, want 1 (only the triggering key)", got)
	}
}

func TestAllowIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	const goroutines = 50
	l := New(goroutines, time.Minute)

	var wg sync.WaitGroup
	allowed := make([]bool, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed[i] = l.Allow("mesma-chave")
		}()
	}
	wg.Wait()

	for i, ok := range allowed {
		if !ok {
			t.Fatalf("attempt %d denied: the limit should have covered all %d",
				i, goroutines)
		}
	}
	if l.Allow("mesma-chave") {
		t.Error("attempt beyond the limit was allowed: counts were lost to a race")
	}
}

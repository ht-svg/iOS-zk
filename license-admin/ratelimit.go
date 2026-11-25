package main

import (
	"sync"
	"time"
)

// attemptLimiter locks a key out after too many failures, used for the admin
// login form so the password cannot be brute forced over the network.
type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]*attemptEntry
	max     int
	window  time.Duration
	lockout time.Duration
}

type attemptEntry struct {
	fails       int
	windowStart time.Time
	lockedUntil time.Time
}

func newAttemptLimiter(max int, window, lockout time.Duration) *attemptLimiter {
	return &attemptLimiter{
		entries: make(map[string]*attemptEntry),
		max:     max,
		window:  window,
		lockout: lockout,
	}
}

// allow reports whether the key may try again right now.
func (l *attemptLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return true
	}
	now := time.Now()
	if now.Before(entry.lockedUntil) {
		return false
	}
	if now.Sub(entry.windowStart) > l.window {
		delete(l.entries, key)
		return true
	}
	return entry.fails < l.max
}

// fail records a failed attempt.
func (l *attemptLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.windowStart) > l.window {
		entry = &attemptEntry{windowStart: now}
		l.entries[key] = entry
	}
	entry.fails++
	if entry.fails >= l.max {
		entry.lockedUntil = now.Add(l.lockout)
	}
}

// reset clears the counter after a successful login.
func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// windowLimiter allows at most max events per key inside a rolling window. It
// keeps a public bind endpoint from being used to burn through the code pool.
type windowLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

func newWindowLimiter(max int, window time.Duration) *windowLimiter {
	return &windowLimiter{hits: make(map[string][]time.Time), max: max, window: window}
}

func (l *windowLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)

	kept := l.hits[key][:0]
	for _, at := range l.hits[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

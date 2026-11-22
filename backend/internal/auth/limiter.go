package auth

import (
	"net/netip"
	"sync"
	"time"
)

// maxEntries bounds memory used by tracking; beyond it, idle keys are dropped first.
const maxEntries = 100_000

// ipKey groups IPv6 clients by /64, since one host usually controls a whole /64.
func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Is4() {
		return ip
	}
	p, _ := a.Prefix(64)
	return p.String()
}

// limiter blocks a key (client IP or username) after repeated failures.
// Each new block doubles in length.
type limiter struct {
	mu       sync.Mutex
	entries  map[string]*attempts
	maxFails int
	window   time.Duration
	baseLock time.Duration
	maxLock  time.Duration
}

type attempts struct {
	fails        int
	windowStart  time.Time
	blockedUntil time.Time
	strikes      int
	lastSeen     time.Time
}

func newLimiter(maxFails int, window, baseLock, maxLock time.Duration) *limiter {
	return &limiter{entries: map[string]*attempts{}, maxFails: maxFails, window: window, baseLock: baseLock, maxLock: maxLock}
}

// blocked returns how long the key stays blocked, or 0.
func (l *limiter) blocked(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.entries[key]
	if a == nil || !now.Before(a.blockedUntil) {
		return 0
	}
	return a.blockedUntil.Sub(now)
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.entries[key]
	if a == nil {
		if len(l.entries) >= maxEntries {
			l.evictLocked(now)
		}
		a = &attempts{windowStart: now}
		l.entries[key] = a
	}
	a.lastSeen = now
	if now.Sub(a.windowStart) > l.window {
		a.fails, a.windowStart = 0, now
	}
	a.fails++
	if a.fails >= l.maxFails {
		lock := l.baseLock << a.strikes
		if lock > l.maxLock || lock <= 0 {
			lock = l.maxLock
		}
		a.blockedUntil = now.Add(lock)
		a.strikes++
		a.fails = 0
		a.windowStart = now
	}
}

func (l *limiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// evictLocked drops keys that are not blocked right now.
func (l *limiter) evictLocked(now time.Time) {
	for k, a := range l.entries {
		if now.After(a.blockedUntil) {
			delete(l.entries, k)
		}
	}
}

// sweep forgets keys that have been quiet for a day.
func (l *limiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, a := range l.entries {
		if now.After(a.blockedUntil) && now.Sub(a.lastSeen) > 24*time.Hour {
			delete(l.entries, k)
		}
	}
}

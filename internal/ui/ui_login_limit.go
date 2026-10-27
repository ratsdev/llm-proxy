package ui

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// uiLoginMaxFailures is the number of rejected sign-ins one key may collect
	// inside one window before it is throttled.
	uiLoginMaxFailures = 5
	uiLoginWindow      = 5 * time.Minute
	// uiRegisterMaxAttempts caps how many registrations one source address may
	// start inside one window. Each attempt derives a PBKDF2 verifier and, when
	// accepted, writes an account, so this bounds both CPU work and account
	// growth.
	uiRegisterMaxAttempts = 20
	uiRegisterWindow      = time.Hour
	// uiLimiterMaxKeys bounds every limiter: a spray of usernames or source
	// addresses must not grow one without limit.
	uiLimiterMaxKeys = 4096
)

// attemptLimiter throttles repeated console attempts per key inside a window.
// Sign-in counts every attempt and clears the counters after a successful one;
// registration counts every attempt that reaches the account write.
type attemptLimiter struct {
	max    int
	window time.Duration

	mu      sync.Mutex
	entries map[string]attemptEntry
}

type attemptEntry struct {
	count   int
	resetAt time.Time
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{
		max:     max,
		window:  window,
		entries: make(map[string]attemptEntry),
	}
}

// reserve reports how long the caller must wait before trying again. A zero
// duration means the attempt was accepted and counted for every key. Checking
// and counting share one critical section, so a burst of concurrent requests
// cannot exceed the budget.
func (l *attemptLimiter) reserve(keys []string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	var wait time.Duration
	for _, key := range keys {
		entry, ok := l.entries[key]
		if !ok || !now.Before(entry.resetAt) {
			continue
		}
		if remaining := entry.resetAt.Sub(now); entry.count >= l.max && remaining > wait {
			wait = remaining
		}
	}
	if wait > 0 {
		return wait
	}
	for _, key := range keys {
		entry := l.entries[key]
		if !now.Before(entry.resetAt) {
			entry = attemptEntry{resetAt: now.Add(l.window)}
		}
		entry.count++
		l.entries[key] = entry
	}
	return 0
}

// reset clears the counters for keys after a successful sign-in.
func (l *attemptLimiter) reset(keys []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, key := range keys {
		delete(l.entries, key)
	}
}

// pruneLocked keeps the table bounded. Expired entries go first; if the table is
// still full, entries are dropped until it is half empty, which costs at most one
// extra attempt per dropped key.
func (l *attemptLimiter) pruneLocked(now time.Time) {
	if len(l.entries) < uiLimiterMaxKeys {
		return
	}
	for key, entry := range l.entries {
		if !now.Before(entry.resetAt) {
			delete(l.entries, key)
		}
	}
	for key := range l.entries {
		if len(l.entries) < uiLimiterMaxKeys/2 {
			return
		}
		delete(l.entries, key)
	}
}

// limiterKeys scopes one console attempt to the account (when known) and the
// source address. An empty username relies on the address bucket alone.
func limiterKeys(r *http.Request, username string) []string {
	keys := make([]string, 0, 2)
	if username != "" {
		keys = append(keys, "user:"+strings.ToLower(username))
	}
	if host := clientHost(r); host != "" {
		keys = append(keys, "ip:"+host)
	}
	return keys
}

// clientHost is the direct peer address. X-Forwarded-For is deliberately not
// consulted: any client can set it, and honouring it would hand an attacker a
// fresh bucket per attempt.
func clientHost(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

// retryAfterSeconds renders a wait for the Retry-After header, rounding up so a
// client that honours the header never retries inside the window.
func retryAfterSeconds(wait time.Duration) string {
	seconds := int(wait / time.Second)
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

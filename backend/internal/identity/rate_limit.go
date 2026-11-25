package identity

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"bettercontent/backend/internal/transport"
)

const (
	defaultRateWindow = 10 * time.Second
	defaultRateLimit  = 100
	authRateLimit     = 3
	maxRateLimitKeys  = 100_000
)

type rateEntry struct {
	started time.Time
	lastHit time.Time
	count   int
}

type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
	checks  uint64
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{entries: make(map[string]rateEntry)}
}

func (l *rateLimiter) Consume(key string, now time.Time, limit int, window time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.checks++
	if l.checks%1024 == 0 {
		for candidate, entry := range l.entries {
			if now.Sub(entry.lastHit) >= window {
				delete(l.entries, candidate)
			}
		}
	}
	entry, exists := l.entries[key]
	if !exists || now.Before(entry.started) || now.Sub(entry.started) >= window {
		if len(l.entries) >= maxRateLimitKeys {
			for candidate := range l.entries {
				delete(l.entries, candidate)
				break
			}
		}
		l.entries[key] = rateEntry{started: now, lastHit: now, count: 1}
		return true, 0
	}
	if entry.count >= limit {
		retryAfter := window - now.Sub(entry.started)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	entry.count++
	entry.lastHit = now
	l.entries[key] = entry
	return true, 0
}

func rateLimitKey(r *http.Request) string {
	client := "no-trusted-ip"
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded != "" && !strings.Contains(forwarded, ",") {
		if address := net.ParseIP(forwarded); address != nil {
			client = address.String()
		}
	}
	return client + "\x00" + r.URL.Path
}

func (s *Service) allowRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		if !s.validOrigin(r) {
			transport.WriteError(w, http.StatusForbidden, transport.INVALIDORIGIN, nil)
			return false
		}
	} else if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		transport.WriteError(w, http.StatusForbidden, transport.INVALIDORIGIN, nil)
		return false
	}
	if !s.production {
		return true
	}
	limit := defaultRateLimit
	if r.URL.Path == "/api/v1/auth/sign-in" || r.URL.Path == "/api/v1/auth/sign-up" {
		limit = authRateLimit
	}
	allowed, retryAfter := s.rateLimiter.Consume(rateLimitKey(r), s.clock.Now(), limit, defaultRateWindow)
	if !allowed {
		w.Header().Set("X-Retry-After", intDurationSeconds(retryAfter))
		transport.WriteError(w, http.StatusTooManyRequests, transport.RATELIMITED, nil)
		return false
	}
	return true
}

func intDurationSeconds(duration time.Duration) string {
	seconds := (duration + time.Second - 1) / time.Second
	return strconv.Itoa(int(seconds))
}

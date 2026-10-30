package auth

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit configures the per-IP limiter on /auth/{provider}/start and /verify.
type RateLimit struct {
	Every time.Duration // one token per Every
	Burst int
}

// DefaultRateLimit allows bursts of 20 requests, then one every 3 seconds per IP.
var DefaultRateLimit = RateLimit{Every: 3 * time.Second, Burst: 20}

// ipLimiter is an in-memory, per-process token bucket per client IP. Idle entries are swept lazily.
// Behind multiple replicas the effective limit multiplies; fine for this deployment size.
type ipLimiter struct {
	cfg       RateLimit
	now       Clock
	mu        sync.Mutex
	clients   map[string]*client
	lastSweep time.Time
}

type client struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

const limiterIdle = 10 * time.Minute

func newIPLimiter(cfg RateLimit, now Clock) *ipLimiter {
	return &ipLimiter{cfg: cfg, now: now, clients: map[string]*client{}}
}

// allow reports whether ip may proceed and, if not, how long until it may.
func (l *ipLimiter) allow(ip string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		for k, c := range l.clients {
			if now.Sub(c.lastSeen) > limiterIdle {
				delete(l.clients, k)
			}
		}
		l.lastSweep = now
	}
	c, ok := l.clients[ip]
	if !ok {
		c = &client{lim: rate.NewLimiter(rate.Every(l.cfg.Every), l.cfg.Burst)}
		l.clients[ip] = c
	}
	c.lastSeen = now
	r := c.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

// middleware rejects over-limit clients with 429 and Retry-After. It keys on RemoteAddr, which the router's
// RealIP middleware has already replaced with X-Forwarded-For / X-Real-IP when present.
func (l *ipLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
		if ok, wait := l.allow(ip); !ok {
			writeError(w, r, tooManyRequests("too many requests, slow down", wait))
			return
		}
		next.ServeHTTP(w, r)
	})
}

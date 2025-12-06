package middleware

import (
	"compress/gzip"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------- Gzip Compression ----------------

// WithGzip adds gzip compression to responses
func WithGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if client accepts gzip encoding
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Wrap the response writer with gzip
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length") // Length will change after compression

		gz := gzip.NewWriter(w)
		defer gz.Close()

		gzw := &gzipResponseWriter{
			ResponseWriter: w,
			Writer:         gz,
		}

		next.ServeHTTP(gzw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

// ---------------- Logging ----------------

// WithLogging logs HTTP requests and responses
func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lw, r)

		// Skip logging for 302 redirects to reduce probe noise
		if lw.status == http.StatusFound {
			return
		}

		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, lw.status, time.Since(start).Truncate(time.Millisecond))
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.status = code
	lw.ResponseWriter.WriteHeader(code)
}

// ---------------- Panic Recovery ----------------

// WithRecover recovers from panics and returns 500 errors
func WithRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic: %v", v)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------- Throttle (max in-flight) ----------------

// Semaphore limits concurrent requests
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore creates a new semaphore with max concurrent requests
func NewSemaphore(max int) *Semaphore {
	if max < 1 {
		max = 1
	}
	return &Semaphore{ch: make(chan struct{}, max)}
}

func (s *Semaphore) acquire(ctx context.Context) bool {
	select {
	case s.ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Semaphore) release() {
	select {
	case <-s.ch:
	default:
	}
}

// WithThrottle limits concurrent requests
func WithThrottle(sem *Semaphore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok := sem.acquire(r.Context()); !ok {
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		}
		defer sem.release()
		next.ServeHTTP(w, r)
	})
}

// ---------------- Rate Limiting (token bucket) ----------------

// TokenBucket implements a token bucket rate limiter
type TokenBucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	last     time.Time
	ttl      time.Duration
	lastSeen time.Time
}

// NewTokenBucket creates a new token bucket
func NewTokenBucket(rate, burst float64, ttl time.Duration) *TokenBucket {
	now := time.Now()
	if rate <= 0 {
		rate = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{
		rate:     rate,
		burst:    burst,
		tokens:   burst,
		last:     now,
		ttl:      ttl,
		lastSeen: now,
	}
}

func (b *TokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+(elapsed*b.rate))
		b.last = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// PerKeyTokenBucket maintains a bucket per key (e.g., client IP)
type PerKeyTokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*TokenBucket
	rate    float64
	burst   float64
	ttl     time.Duration
}

// NewPerKeyTokenBucket creates a new per-key token bucket
func NewPerKeyTokenBucket(rate, burst float64, ttl time.Duration) *PerKeyTokenBucket {
	p := &PerKeyTokenBucket{
		buckets: make(map[string]*TokenBucket),
		rate:    rate,
		burst:   burst,
		ttl:     ttl,
	}
	go p.cleanupLoop()
	return p
}

func (p *PerKeyTokenBucket) get(key string) *TokenBucket {
	p.mu.Lock()
	defer p.mu.Unlock()

	if b, ok := p.buckets[key]; ok {
		return b
	}
	b := NewTokenBucket(p.rate, p.burst, p.ttl)
	p.buckets[key] = b
	return b
}

func (p *PerKeyTokenBucket) allow(key string, now time.Time) bool {
	return p.get(key).allow(now)
}

func (p *PerKeyTokenBucket) cleanupLoop() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		p.mu.Lock()
		for k, b := range p.buckets {
			b.mu.Lock()
			seen := b.lastSeen
			ttl := b.ttl
			b.mu.Unlock()

			if ttl > 0 && now.Sub(seen) > ttl {
				delete(p.buckets, k)
			}
		}
		p.mu.Unlock()
	}
}

// WithRateLimit applies global and per-IP rate limiting
func WithRateLimit(global *TokenBucket, perIP *PerKeyTokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()

		// Global limit first (protects upstream)
		if !global.allow(now) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded (global)", http.StatusTooManyRequests)
			return
		}

		// Per-IP limit
		ip := ExtractClientIP(r)
		if ip == "" {
			ip = "unknown"
		}
		if !perIP.allow(ip, now) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded (per-ip)", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ---------------- Utilities ----------------

// ExtractClientIP extracts the client IP from request
func ExtractClientIP(r *http.Request) string {
	// Prefer X-Forwarded-For if present (first IP)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	// Fallback: RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return ""
}

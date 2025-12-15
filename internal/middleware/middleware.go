package middleware

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"apigateway/internal/logger"

	"github.com/google/uuid"
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

// ---------------- Request ID ----------------

type contextKey string

const requestIDKey contextKey = "request_id"

// WithRequestID adds a request ID to each request
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID extracts the request ID from context
func GetRequestID(r *http.Request) string {
	if reqID, ok := r.Context().Value(requestIDKey).(string); ok {
		return reqID
	}
	return ""
}

// ---------------- Logging ----------------

// WithLogging logs HTTP requests and responses with structured logging
func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w, status: 200}

		// Log request started
		logger.Log.Info("request_started",
			slog.String("request_id", GetRequestID(r)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("client_ip", ExtractClientIP(r)),
			slog.String("user_agent", r.UserAgent()),
		)

		next.ServeHTTP(lw, r)

		duration := time.Since(start)

		// Determine log level based on status code
		logLevel := slog.LevelInfo
		if lw.status >= 500 {
			logLevel = slog.LevelError
		} else if lw.status >= 400 {
			logLevel = slog.LevelWarn
		}

		// Skip logging 302 redirects at debug level only (reduces probe noise)
		if lw.status == http.StatusFound {
			logLevel = slog.LevelDebug
		}

		// Log request completed
		logger.Log.Log(r.Context(), logLevel, "request_completed",
			slog.String("request_id", GetRequestID(r)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", lw.status),
			slog.Duration("duration_ms", duration),
			slog.Int("bytes", lw.bytes),
		)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.status = code
	lw.ResponseWriter.WriteHeader(code)
}

func (lw *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := lw.ResponseWriter.Write(b)
	lw.bytes += n
	return n, err
}

// ---------------- Panic Recovery ----------------

// WithRecover recovers from panics and returns 500 errors with stack traces
func WithRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logger.Log.Error("panic_recovered",
					slog.String("request_id", GetRequestID(r)),
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
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
		ip := ExtractClientIP(r)
		if ip == "" {
			ip = "unknown"
		}

		// Global limit first (protects upstream)
		if !global.allow(now) {
			logger.Log.Warn("rate_limit_exceeded",
				slog.String("request_id", GetRequestID(r)),
				slog.String("type", "global"),
				slog.String("client_ip", ip),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded (global)", http.StatusTooManyRequests)
			return
		}

		// Per-IP limit
		if !perIP.allow(ip, now) {
			logger.Log.Warn("rate_limit_exceeded",
				slog.String("request_id", GetRequestID(r)),
				slog.String("type", "per-ip"),
				slog.String("client_ip", ip),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)
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

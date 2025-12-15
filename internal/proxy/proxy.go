package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"apigateway/internal/logger"
	"apigateway/internal/middleware"
)

// Config holds reverse proxy configuration
type Config struct {
	Attempts     int
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	TargetServer string
}

// NewReverseProxy creates a reverse proxy with retries and proper header handling
func NewReverseProxy(target *url.URL, cfg Config) *httputil.ReverseProxy {
	// Base transport with sane timeouts + SNI
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: cfg.TargetServer,
			MinVersion: tls.VersionTLS12,
		},
	}

	// Wrap transport with retries
	retrying := &retryingRoundTripper{
		next:      base,
		attempts:  cfg.Attempts,
		baseDelay: cfg.BaseBackoff,
		maxDelay:  cfg.MaxBackoff,
	}

	director := func(r *http.Request) {
		// Set upstream target scheme/host
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host

		// Set Host header to upstream host
		r.Host = target.Host

		// Set X-Real-IP header
		clientIP := middleware.ExtractClientIP(r)
		if clientIP != "" {
			r.Header.Set("X-Real-IP", clientIP)
		}

		// Set X-Forwarded-For header (append client IP)
		if clientIP != "" {
			prior := r.Header.Get("X-Forwarded-For")
			if prior == "" {
				r.Header.Set("X-Forwarded-For", clientIP)
			} else {
				r.Header.Set("X-Forwarded-For", prior+", "+clientIP)
			}
		}

		// Set X-Forwarded-Proto header
		if r.Header.Get("X-Forwarded-Proto") == "" {
			if r.TLS != nil {
				r.Header.Set("X-Forwarded-Proto", "https")
			} else {
				r.Header.Set("X-Forwarded-Proto", "http")
			}
		}

		// Remove hop-by-hop headers
		r.Header.Del("Connection")
	}

	rp := &httputil.ReverseProxy{
		Director:  director,
		Transport: retrying,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
			logger.Log.Error("proxy_error",
				slog.String("request_id", middleware.GetRequestID(r)),
				slog.String("upstream", target.Host),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("error", e.Error()),
			)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
		ModifyResponse: func(resp *http.Response) error {
			return nil
		},
	}

	return rp
}

// ---------------- Retries ----------------

// isIdempotent checks if HTTP method is safe to retry
func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

type retryingRoundTripper struct {
	next      http.RoundTripper
	attempts  int
	baseDelay time.Duration
	maxDelay  time.Duration
}

func (rt *retryingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	attempts := rt.attempts
	if attempts < 1 {
		attempts = 1
	}

	// If non-idempotent AND body can't be replayed, do not retry
	canRetry := isIdempotent(req.Method)
	if !canRetry && req.GetBody == nil && req.Body != nil {
		return rt.next.RoundTrip(req)
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		// Clone the request for each attempt
		tryReq := req.Clone(req.Context())
		if req.Body != nil && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			tryReq.Body = body
		}

		resp, err := rt.next.RoundTrip(tryReq)
		// Network/transport error: retry if allowed
		if err != nil {
			lastErr = err
			if !canRetry || i == attempts-1 {
				return nil, err
			}
			logger.Log.Warn("proxy_retry",
				slog.String("request_id", middleware.GetRequestID(req)),
				slog.String("upstream", req.URL.Host),
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Int("attempt", i+1),
				slog.Int("max_attempts", attempts),
				slog.String("error", err.Error()),
			)
			sleepBackoff(req.Context(), rt.baseDelay, rt.maxDelay, i)
			continue
		}

		// If upstream returns 5xx, retry for idempotent requests
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && canRetry && i < attempts-1 {
			logger.Log.Warn("proxy_retry_5xx",
				slog.String("request_id", middleware.GetRequestID(req)),
				slog.String("upstream", req.URL.Host),
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Int("status", resp.StatusCode),
				slog.Int("attempt", i+1),
				slog.Int("max_attempts", attempts),
			)
			// Must close body before retrying to avoid leaks
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			sleepBackoff(req.Context(), rt.baseDelay, rt.maxDelay, i)
			continue
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("proxy retry attempts exhausted")
	}
	return nil, lastErr
}

func sleepBackoff(ctx context.Context, base, max time.Duration, attempt int) {
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	if max <= 0 {
		max = 2 * time.Second
	}
	// Exponential backoff: base * 2^attempt, capped
	mult := math.Pow(2, float64(attempt))
	d := time.Duration(float64(base) * mult)
	if d > max {
		d = max
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}

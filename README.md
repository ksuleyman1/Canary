
<p align="center">
  <img src="logo.png" alt="Canary Logo" width="400"/>
</p>

# Canary

Canary is a lightweight and easy to use API gateway written in Golang. It comes packaged with built-in throttling, rate-limiting, retries, auth, gzip compression, and comprehensive request handling out-of-the-box.

## Features

- **Structured Logging**: Production-grade observability with slog (JSON/text formats)
- **Request Tracing**: Automatic request ID generation and propagation
- **Automatic Gzip Compression**: Reduces bandwidth by 60-80% for JSON/text responses
- **Rate Limiting**: Global and per-IP token bucket rate limiting
- **Request Throttling**: Maximum concurrent request limits
- **Automatic Retries**: Exponential backoff for failed upstream requests
- **Health Checks**: Simple endpoint for load balancer probes
- **Authentication**: Secure endpoints by validating user credentials/JWT tokens before proxying
- **Panic Recovery**: Graceful error handling with stack traces
- **Modular Architecture**: Clean separation of concerns for easy maintenance

## Project Structure

```
API_Gateway_ACA/
├── apig.go                          # Main application entry point
├── internal/
│   ├── config/
│   │   └── config.go               # Configuration management
│   ├── logger/
│   │   └── logger.go               # Structured logging with slog
│   ├── middleware/
│   │   └── middleware.go           # All middleware (gzip, logging, rate limiting, etc.)
│   ├── proxy/
│   │   └── proxy.go                # Reverse proxy with retry logic
│   └── router/
│       └── router.go               # Route registration and management
```

## Configuration

All configuration is managed through environment variables with sensible defaults:

### Server Configuration
- **`PORT`**: Server listening port (default: `80`)

### Throttling
- **`MAX_IN_FLIGHT`**: Maximum concurrent requests (default: `256`)

### Rate Limiting
- **`PER_IP_RPS`**: Requests per second per IP (default: `10`)
- **`PER_IP_BURST`**: Burst capacity per IP (default: `20`)
- **`GLOBAL_RPS`**: Global requests per second (default: `200`)
- **`GLOBAL_BURST`**: Global burst capacity (default: `400`)
- **`LIMITER_TTL`**: Cleanup interval for idle IP limiters (default: `10m`)

### Retry Behavior
- **`RETRY_ATTEMPTS`**: Number of retry attempts for idempotent requests (default: `3`)
- **`RETRY_BACKOFF`**: Initial backoff delay (default: `150ms`)
- **`RETRY_MAX_BACKOFF`**: Maximum backoff delay (default: `1500ms`)

### Logging Configuration
- **`LOG_LEVEL`**: Log level - `DEBUG`, `INFO`, `WARN`, or `ERROR` (default: `INFO`)
- **`LOG_FORMAT`**: Output format - `json` or `text` (default: `json`)

## Structured Logging

Canary uses Go's built-in `log/slog` package for production-grade structured logging with full observability.

### Log Levels

- **DEBUG**: Detailed diagnostic information (includes 302 redirects)
- **INFO**: General informational messages (request started/completed)
- **WARN**: Warning conditions (rate limits, 4xx errors, retries)
- **ERROR**: Error conditions (5xx errors, panics, proxy failures)

### Automatic Request Tracing

Every request is automatically assigned a unique request ID (UUID) that's:
- Generated if not provided in the `X-Request-ID` header
- Returned in the response `X-Request-ID` header
- Included in all log entries for that request
- Used to correlate logs across retries and middleware

### Structured Log Output

Example JSON logs:

```json
{"time":"2025-12-15T10:30:45Z","level":"INFO","msg":"gateway_starting","port":"80","log_level":"INFO","log_format":"json"}
{"time":"2025-12-15T10:31:12Z","level":"INFO","msg":"request_started","request_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","method":"GET","path":"/api/auth/login","client_ip":"10.0.0.5","user_agent":"Mozilla/5.0"}
{"time":"2025-12-15T10:31:12Z","level":"INFO","msg":"request_completed","request_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479","method":"GET","path":"/api/auth/login","status":200,"duration_ms":45,"bytes":1024}
{"time":"2025-12-15T10:31:15Z","level":"WARN","msg":"rate_limit_exceeded","request_id":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","type":"per-ip","client_ip":"10.0.0.8","method":"POST","path":"/api/auth/signup"}
{"time":"2025-12-15T10:31:18Z","level":"WARN","msg":"proxy_retry","request_id":"b2c3d4e5-f6a7-8901-bcde-f12345678901","upstream":"exampleservice1.com","method":"GET","path":"/api/auth/user","attempt":2,"max_attempts":3,"error":"dial tcp: connection refused"}
{"time":"2025-12-15T10:31:20Z","level":"ERROR","msg":"panic_recovered","request_id":"c3d4e5f6-a7b8-9012-cdef-123456789012","panic":"runtime error: index out of range","stack":"goroutine 42 [running]:\n...","method":"GET","path":"/api/data"}
```

### Log Events

| Event | Level | Fields |
|-------|-------|--------|
| `gateway_starting` | INFO | port, log_level, log_format |
| `gateway_listening` | INFO | port, auth_service, onboarding_service |
| `request_started` | INFO | request_id, method, path, client_ip, user_agent |
| `request_completed` | INFO/WARN/ERROR | request_id, method, path, status, duration_ms, bytes |
| `rate_limit_exceeded` | WARN | request_id, type, client_ip, method, path |
| `proxy_retry` | WARN | request_id, upstream, method, path, attempt, max_attempts, error |
| `proxy_retry_5xx` | WARN | request_id, upstream, method, path, status, attempt, max_attempts |
| `proxy_error` | ERROR | request_id, upstream, method, path, error |
| `panic_recovered` | ERROR | request_id, panic, stack, method, path |


## Adding New Endpoints

To add a new upstream service:

### 1. Add Configuration
Edit `internal/config/config.go` and add to `UpstreamConfig`:

```go
type UpstreamConfig struct {
    AuthURL       string
    OnboardingURL string
    NewServiceURL string  // Add this
}
```

Then update the `Load()` function:

```go
Upstream: UpstreamConfig{
    // ... existing config ...
    NewServiceURL: env("NEW_SERVICE_URL", "https://new-service.example.com"),
}
```

### 2. Create Proxy
Edit `apig.go` and add proxy creation:

```go
newServiceURL, err := url.Parse(cfg.Upstream.NewServiceURL)
if err != nil {
    log.Fatalf("invalid NEW_SERVICE_URL: %v", err)
}

newServiceProxy := proxy.NewReverseProxy(newServiceURL, proxy.Config{
    Attempts:     cfg.Retry.Attempts,
    BaseBackoff:  cfg.Retry.BaseBackoff,
    MaxBackoff:   cfg.Retry.MaxBackoff,
    TargetServer: newServiceURL.Hostname(),
})
```

### 3. Register Routes
Edit `internal/router/router.go`:

- Update the `Router` struct to include the new proxy:

```go
type Router struct {
    mux             *http.ServeMux
    authProxy       *httputil.ReverseProxy
    onboardingProxy *httputil.ReverseProxy
    courseProxy     *httputil.ReverseProxy
    newServiceProxy *httputil.ReverseProxy  // Add this
}
```

- Update `New()` function to accept the new proxy parameter:

```go
// New creates a new router with the given proxies
func New(authProxy, onboardingProxy, courseProxy, newServiceProxy *httputil.ReverseProxy) *Router {
	return &Router{
		mux:             http.NewServeMux(),
		authProxy:       authProxy,
		onboardingProxy: onboardingProxy,
		courseProxy:     courseProxy,
		newServiceProxy: newServiceProxy,  // Add this
	}
}
```

- Add routing logic in `handleAPI()`:

```go
// Route /api/newservice/* to new service
if strings.HasPrefix(r.URL.Path, "/api/newservice") { //will proxy to https://new-service.example.com/api/newservice
    rt.newServiceProxy.ServeHTTP(w, r)
    return
}
```

### 4. Update Router Initialization
Edit `apig.go` and update the `router.New()` call to pass all proxies including the new one:

```go
// Setup routes
rt := router.New(authProxy, onboardingProxy, courseProxy, newServiceProxy)
rt.RegisterRoutes()
```

## Enabling Authentication

Canary includes a commented-out authentication function that can validate user tokens before proxying requests to upstream services.

### To Enable Authentication:

1. Edit `internal/router/router.go`
2. Uncomment the `authenticateRequest` function at the bottom of the file
3. Add the following authentication check in the `handleAPI` function of the relevant endpoint:

```go
if strings.HasPrefix(r.URL.Path, "/api/newservice") {

    if !rt.authenticateRequest(r) {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }

    rt.newServiceProxy.ServeHTTP(w, r)
    return
}
```

4. Update the validation URL in `authenticateRequest` to point to your IAM service's token validation endpoint

The included example validates tokens by:
- Extracting the `Authorization: Bearer <token>` header
- Making an HTTP request to your IAM service's validation endpoint
- Forwarding the token and checking for a `200 OK` response

You can customize this to use JWT validation, Redis caching, or any other authentication method.

## Customizing Behavior

### Adjust Rate Limits
Edit `internal/config/config.go` and modify the default values in the `Load()` function, or set environment variables.

### Modify Retry Logic
Edit `internal/proxy/proxy.go` to customize retry behavior, backoff strategies, or which HTTP methods are retryable.

### Add/Remove Middleware
Edit `apig.go` and modify the middleware chain:

```go
handler := middleware.WithRecover(
    middleware.WithLogging(
        middleware.WithGzip(
            // Add custom middleware here
            middleware.WithThrottle(throttle,
                middleware.WithRateLimit(globalLimiter, perIPLimiter,
                    rt.Handler(),
                ),
            ),
        ),
    ),
)
```

### Customize Middleware
Edit `internal/middleware/middleware.go` to modify existing middleware behavior (logging format, gzip settings, etc.).

## Monitoring & Observability

### Request Tracing

Each request receives a unique ID that can be used to trace it through your entire system:

```bash
# Client sends request with custom ID
curl -H "X-Request-ID: custom-trace-123" https://api.example.com/api/auth/login

# Gateway logs all events with this ID
# Response includes the same ID
```

### Performance Metrics

All request completion logs include:
- **duration_ms**: Request processing time
- **bytes**: Response size
- **status**: HTTP status code

Use these for SLA monitoring and performance analysis.

### Error Debugging

When issues occur, logs include full context:
- **Retries**: See each retry attempt with upstream and error details
- **Rate Limits**: Identify which clients are hitting limits
- **Panics**: Full stack traces for debugging crashes
- **Proxy Errors**: Connection failures with upstream context

### Example Queries

Find slow requests:
```json
{"level":"INFO","msg":"request_completed","duration_ms":{"$gt":1000}}
```

Track retry patterns:
```json
{"level":"WARN","msg":"proxy_retry","upstream":"exampleservice1.com"}
```

Monitor rate limit violations by IP:
```json
{"level":"WARN","msg":"rate_limit_exceeded","type":"per-ip"}
```

## Request Flow

1. **Recovery**: Catches panics and prevents server crashes (logs stack traces)
2. **Request ID**: Assigns unique UUID to each request for tracing
3. **Logging**: Logs request start with context (method, path, client IP, user agent)
4. **Gzip**: Compresses responses if client supports it
5. **Throttling**: Limits concurrent requests
6. **Rate Limiting**: Enforces global and per-IP rate limits (logs violations)
7. **Routing**: Determines which upstream service to proxy to
8. **Proxy**: Forwards request with proper headers and retry logic (logs retries)
9. **Logging**: Logs request completion with status, duration, and bytes transferred

## Development

### Running Locally
```bash
# Default settings (INFO level, JSON format)
go run apig.go

# With custom log settings
LOG_LEVEL=DEBUG LOG_FORMAT=text go run apig.go

# Production settings
LOG_LEVEL=WARN LOG_FORMAT=json go run apig.go
```

### Building
```bash
go build -o gateway apig.go
```

### Docker
```bash
docker build -t api-gateway .
docker run -p 80:80 \
  -e LOG_LEVEL=INFO \
  -e LOG_FORMAT=json \
  api-gateway
```

## Features in Detail

### Gzip Compression
- Automatically compresses responses when client sends `Accept-Encoding: gzip`
- Typical compression: 60-80% size reduction for JSON/text
- Transparent to clients

### Retry Logic
- Only retries idempotent methods (GET, HEAD, OPTIONS, PUT, DELETE)
- Exponential backoff with jitter
- Retries on network errors and 5xx responses
- Configurable attempts and backoff delays

### Header Management
- Strips `/api` prefix from paths
- Sets `X-Real-IP`, `X-Forwarded-For`, `X-Forwarded-Proto`
- Removes hop-by-hop headers
- Preserves upstream host for SNI

### Rate Limiting
- Token bucket algorithm
- Separate limits for global and per-IP
- Automatic cleanup of idle IP buckets
- Returns `429 Too Many Requests` with `Retry-After` header





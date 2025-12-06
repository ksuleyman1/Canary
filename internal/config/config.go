package config

import (
	"fmt"
	"log"
	"os"
	"time"
)

// Config holds all application configuration
type Config struct {
	Server     ServerConfig
	Upstream   UpstreamConfig
	Throttle   ThrottleConfig
	RateLimit  RateLimitConfig
	Retry      RetryConfig
	LimiterTTL time.Duration
}

// ServerConfig holds HTTP server settings
type ServerConfig struct {
	Port              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// UpstreamConfig holds upstream service URLs
type UpstreamConfig struct {
	AuthURL       string
	OnboardingURL string
}

// ThrottleConfig holds concurrent request limits
type ThrottleConfig struct {
	MaxInFlight int
}

// RateLimitConfig holds token bucket rate limiting settings
type RateLimitConfig struct {
	PerIPRPS    float64
	PerIPBurst  float64
	GlobalRPS   float64
	GlobalBurst float64
}

// RetryConfig holds retry behavior settings
type RetryConfig struct {
	Attempts    int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// Load reads configuration from environment variables with defaults
func Load() (*Config, error) {
	return &Config{
		Server: ServerConfig{
			Port:              env("PORT", "80"),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		Upstream: UpstreamConfig{
			AuthURL:       env("IAM_SERVICE_URL", "https://exampleservice1.com"),
			OnboardingURL: env("ONBOARDING_TARGET_URL", "https://exampleservice2.com"),
		},
		Throttle: ThrottleConfig{
			MaxInFlight: mustInt(env("MAX_IN_FLIGHT", "256")),
		},
		RateLimit: RateLimitConfig{
			PerIPRPS:    mustFloat(env("PER_IP_RPS", "10")),
			PerIPBurst:  mustFloat(env("PER_IP_BURST", "20")),
			GlobalRPS:   mustFloat(env("GLOBAL_RPS", "200")),
			GlobalBurst: mustFloat(env("GLOBAL_BURST", "400")),
		},
		Retry: RetryConfig{
			Attempts:    mustInt(env("RETRY_ATTEMPTS", "3")),
			BaseBackoff: mustDuration(env("RETRY_BACKOFF", "150ms")),
			MaxBackoff:  mustDuration(env("RETRY_MAX_BACKOFF", "1500ms")),
		},
		LimiterTTL: mustDuration(env("LIMITER_TTL", "10m")),
	}, nil
}

// env returns environment variable value or default
func env(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// mustInt parses string to int or fails
func mustInt(s string) int {
	var x int
	_, err := fmt.Sscanf(s, "%d", &x)
	if err != nil {
		log.Fatalf("invalid int %q", s)
	}
	return x
}

// mustFloat parses string to float64 or fails
func mustFloat(s string) float64 {
	var x float64
	_, err := fmt.Sscanf(s, "%f", &x)
	if err != nil {
		log.Fatalf("invalid float %q", s)
	}
	return x
}

// mustDuration parses string to time.Duration or fails
func mustDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Fatalf("invalid duration %q", s)
	}
	return d
}

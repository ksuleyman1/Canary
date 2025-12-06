// main.go
//
// API Gateway - modular reverse proxy with rate limiting, retries, and compression
//

package main

import (
	"log"
	"net/http"
	"net/url"

	"apigateway/internal/config"
	"apigateway/internal/middleware"
	"apigateway/internal/proxy"
	"apigateway/internal/router"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Parse upstream URLs
	authTargetURL, err := url.Parse(cfg.Upstream.AuthURL)
	if err != nil {
		log.Fatalf("invalid AUTH_TARGET_URL: %v", err)
	}

	onboardingURL, err := url.Parse(cfg.Upstream.OnboardingURL)
	if err != nil {
		log.Fatalf("invalid ONBOARDING_TARGET_URL: %v", err)
	}

	// Create reverse proxies
	authProxy := proxy.NewReverseProxy(authTargetURL, proxy.Config{
		Attempts:     cfg.Retry.Attempts,
		BaseBackoff:  cfg.Retry.BaseBackoff,
		MaxBackoff:   cfg.Retry.MaxBackoff,
		TargetServer: authTargetURL.Hostname(),
	})

	onboardingProxy := proxy.NewReverseProxy(onboardingURL, proxy.Config{
		Attempts:     cfg.Retry.Attempts,
		BaseBackoff:  cfg.Retry.BaseBackoff,
		MaxBackoff:   cfg.Retry.MaxBackoff,
		TargetServer: onboardingURL.Hostname(),
	})

	// Initialize middleware
	throttle := middleware.NewSemaphore(cfg.Throttle.MaxInFlight)
	globalLimiter := middleware.NewTokenBucket(cfg.RateLimit.GlobalRPS, cfg.RateLimit.GlobalBurst, cfg.LimiterTTL)
	perIPLimiter := middleware.NewPerKeyTokenBucket(cfg.RateLimit.PerIPRPS, cfg.RateLimit.PerIPBurst, cfg.LimiterTTL)

	// Setup routes
	rt := router.New(authProxy, onboardingProxy)
	rt.RegisterRoutes()

	// Build middleware chain
	handler := middleware.WithRecover(
		middleware.WithLogging(
			middleware.WithGzip(
				middleware.WithThrottle(throttle,
					middleware.WithRateLimit(globalLimiter, perIPLimiter,
						rt.Handler(),
					),
				),
			),
		),
	)

	// Create HTTP server
	srv := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	log.Printf("Gateway listening on :%s", cfg.Server.Port)
	log.Printf("  Auth service: %s", cfg.Upstream.AuthURL)
	log.Printf("  Onboarding service: %s", cfg.Upstream.OnboardingURL)
	log.Fatal(srv.ListenAndServe())
}

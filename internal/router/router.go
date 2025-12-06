package router

import (
	"net/http"
	"net/http/httputil"
	"strings"
)

// Router manages all route registrations
type Router struct {
	mux             *http.ServeMux
	authProxy       *httputil.ReverseProxy
	onboardingProxy *httputil.ReverseProxy
}

// New creates a new router with the given proxies
func New(authProxy, onboardingProxy *httputil.ReverseProxy) *Router {
	return &Router{
		mux:             http.NewServeMux(),
		authProxy:       authProxy,
		onboardingProxy: onboardingProxy,
	}
}

// RegisterRoutes sets up all application routes
// This is the central place to add/modify endpoints
func (rt *Router) RegisterRoutes() {
	// Health check endpoint - returns 200 OK for Azure App Gateway health checks
	rt.mux.HandleFunc("/", rt.handleRoot)

	// API routes - all requests to /api/* are handled here
	rt.mux.Handle("/api/", http.HandlerFunc(rt.handleAPI))
}

// handleRoot handles the root path for health checks
func (rt *Router) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleAPI routes API requests to appropriate upstream services
// To add new endpoints:
// 1. Add a new if block with strings.HasPrefix check
// 2. Call the appropriate proxy's ServeHTTP method
func (rt *Router) handleAPI(w http.ResponseWriter, r *http.Request) {
	// Route /api/auth/* to IAM service
	// Examples: /api/auth/login, /api/auth/signup, /api/auth/admin/users
	if strings.HasPrefix(r.URL.Path, "/api/auth") {
		rt.authProxy.ServeHTTP(w, r)
		return
	}

	// Route /api/onboarding/* to onboarding service
	// Examples: /api/onboarding/profile, /api/onboarding/setup
	if strings.HasPrefix(r.URL.Path, "/api/onboarding") {
		rt.onboardingProxy.ServeHTTP(w, r)
		return
	}

	// No matching route found
	http.NotFound(w, r)
}

// Handler returns the underlying http.Handler
func (rt *Router) Handler() http.Handler {
	return rt.mux
}

package router

import (
	"net/http"
	"net/http/httputil"
	"strings"
)

// Router manages all route registrations
type Router struct {
	mux          *http.ServeMux
	authProxy    *httputil.ReverseProxy
	exampleProxy *httputil.ReverseProxy
}

// New creates a new router with the given proxies
func New(authProxy, exampleProxy *httputil.ReverseProxy) *Router {
	return &Router{
		mux:          http.NewServeMux(),
		authProxy:    authProxy,
		exampleProxy: exampleProxy,
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
	// Uncomment to enable authentication for all API routes
	// if !rt.authenticateRequest(r) {
	// 	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	// 	return
	// }

	// Route /api/auth/* to IAM service
	// Examples: /api/auth/login, /api/auth/signup, /api/auth/admin/users
	if strings.HasPrefix(r.URL.Path, "/api/auth") {
		rt.authProxy.ServeHTTP(w, r)
		return
	}

	// Route /api/example/* to example service
	// Examples: /api/example/timestamp
	if strings.HasPrefix(r.URL.Path, "/api/example") {
		rt.exampleProxy.ServeHTTP(w, r)
		return
	}

	// No matching route found
	http.NotFound(w, r)
}

// Handler returns the underlying http.Handler
func (rt *Router) Handler() http.Handler {
	return rt.mux
}

// authenticateRequest validates the user's authentication token
// Uncomment this function to enable authentication before proxying
// func (rt *Router) authenticateRequest(r *http.Request) bool {
// 	// Extract token from Authorization header
// 	authHeader := r.Header.Get("Authorization")
// 	if authHeader == "" {
// 		return false
// 	}
//
// 	// Expected format: "Bearer <token>"
// 	const bearerPrefix = "Bearer "
// 	if !strings.HasPrefix(authHeader, bearerPrefix) {
// 		return false
// 	}
//
// 	token := strings.TrimPrefix(authHeader, bearerPrefix)
// 	if token == "" {
// 		return false
// 	}
//
// 	// Example: Validate token by calling IAM service
// 	// Make HTTP request to IAM service's token validation endpoint
// 	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
// 	defer cancel()
//
// 	// Construct validation request
// 	// Assuming IAM service has a /api/auth/validate endpoint
// 	validationURL := "https://your-iam-service.com/api/auth/validate"
// 	req, err := http.NewRequestWithContext(ctx, http.MethodGet, validationURL, nil)
// 	if err != nil {
// 		return false
// 	}
//
// 	// Forward the Authorization header to IAM service
// 	req.Header.Set("Authorization", authHeader)
//
// 	// Send request to IAM service
// 	client := &http.Client{Timeout: 2 * time.Second}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		return false
// 	}
// 	defer resp.Body.Close()
//
// 	// Token is valid if IAM service returns 200 OK
// 	return resp.StatusCode == http.StatusOK
// }

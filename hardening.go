package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	MaxRequestSize  = 1 << 20 // 1MB max request body
	RequestTimeout  = 30 * time.Second
	MaxPromptLength = 10000   // 10k chars max
	MaxResponseSize = 5 << 20 // 5MB max response
)

// validateRequest checks request size, headers, and input
func validateRequest(r *http.Request) error {
	if r.ContentLength > MaxRequestSize {
		return fmt.Errorf("request too large: %d bytes (max %d)", r.ContentLength, MaxRequestSize)
	}

	r.Body = http.MaxBytesReader(nil, r.Body, MaxRequestSize)

	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("invalid content type: %s", r.Header.Get("Content-Type"))
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return fmt.Errorf("invalid Authorization format, must be 'Bearer <key>'")
	}

	tenantID := r.Header.Get("X-Gateway-Token")
	if tenantID == "" {
		return fmt.Errorf("missing X-Gateway-Token header")
	}

	for _, c := range tenantID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("invalid tenant ID: %s (use alphanumeric, hyphens, underscores only)", tenantID)
		}
	}

	return nil
}

// sanitizePrompt removes potentially dangerous content
func sanitizePrompt(prompt string) string {
	if len(prompt) > MaxPromptLength {
		prompt = prompt[:MaxPromptLength]
	}

	prompt = strings.ReplaceAll(prompt, "\x00", "")

	var result strings.Builder
	for _, r := range prompt {
		if r == '\n' || r == '\t' || r >= 32 {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// writeError writes a safe error response (no stack traces)
func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	safeMsg := strings.TrimSpace(message)
	if safeMsg == "" {
		safeMsg = "Internal server error"
	}

	if len(safeMsg) > 200 {
		safeMsg = safeMsg[:200] + "..."
	}

	json.NewEncoder(w).Encode(map[string]string{
		"error": safeMsg,
	})
}

// timeoutHandler wraps handler with timeout
func timeoutHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
		defer cancel()

		r = r.WithContext(ctx)

		done := make(chan bool, 1)

		go func() {
			next(w, r)
			done <- true
		}()

		select {
		case <-done:
			// Handler completed normally
		case <-ctx.Done():
			log.Printf("Request timeout: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			writeError(w, http.StatusGatewayTimeout, "Request timeout")
		}
	}
}

// recoverHandler recovers from panics and returns safe error
func recoverHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("PANIC recovered: %v", err)
				writeError(w, http.StatusInternalServerError, "Internal server error")
			}
		}()
		next(w, r)
	}
}

// healthCheckDetailed checks all dependencies
func healthCheckDetailed(w http.ResponseWriter, r *http.Request) {
	loopStatus := "disabled"
	if loopKiller != nil {
		loopStatus = "active"
	}

	checks := map[string]interface{}{
		"status": "ok",
		"checks": map[string]interface{}{
			"cache":       checkCache(),
			"providers":   checkProviders(),
			"analytics":   analytics != nil,
			"loop_killer": loopStatus,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checks)
}

func checkCache() string {
	if cache == nil {
		return "in-memory (no redis)"
	}
	return "redis-connected"
}

func checkProviders() string {
	if primaryProvider != nil {
		return "configured"
	}
	return "not-configured"
}

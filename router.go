package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// HandleGatewayRequest acts as an alternate route pipeline mapping directly to our real global cache engine
func HandleGatewayRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()

	userPrompt, bodyBytes, err := ExtractPrompt(r)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Invalid request payload layout"}`))
		return
	}

	if userPrompt == "" {
		userPrompt = string(bodyBytes)
	}

	startupID := "startup_arnab_dev"

	// Extract user's API key from Authorization header
	userAPIKey := r.Header.Get("Authorization")
	userAPIKey = strings.TrimPrefix(userAPIKey, "Bearer ")

	// Match signatures against your true multi-return cache Search execution logic
	if cachedResponse, bestScore, found := cache.Search(startupID, userPrompt, 0.75); found {
		timeSavedMS := 1500 - time.Since(startTime).Milliseconds()
		if timeSavedMS < 0 {
			timeSavedMS = 0
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Cache", "HIT")
		w.Header().Set("X-Gateway-Similarity", fmt.Sprintf("%.2f", bestScore))
		w.Header().Set("X-Gateway-Time-Saved", fmt.Sprintf("%dms", timeSavedMS))

		w.WriteHeader(http.StatusOK)
		w.Write(cachedResponse)
		log.Printf("[ROUTER HIT] Served via semantic engine. Similarity: %.2f", bestScore)
		return
	}

	// Fallback onto your local simulated proxy engine if cache drops through
	liveResponsePayload := buildProxyResponse(bodyBytes, userAPIKey)

	if err := cache.Store(startupID, userPrompt, liveResponsePayload); err != nil {
		log.Printf("[ROUTER WARN] Failed to commit response tracking token to Redis: %v", err)
	}

	totalDurationMS := time.Since(startTime).Milliseconds()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Gateway-Cache", "MISS")
	w.Header().Set("X-Gateway-Duration", fmt.Sprintf("%dms", totalDurationMS))

	w.WriteHeader(http.StatusOK)
	w.Write(liveResponsePayload)
}

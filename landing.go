package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// getStatsForLanding returns live stats for the landing page counters
func getStatsForLanding() map[string]interface{} {
	stats := map[string]interface{}{}

	if analytics != nil {
		report := analytics.GetReport()
		if report.TotalRequests > 0 {
			stats["requests_processed"] = fmt.Sprintf("%d", report.TotalRequests)
		}
		if report.TokensSaved > 0 {
			stats["tokens_saved"] = fmt.Sprintf("%d", report.TokensSaved)
		}
	}

	// Count trials from Redis if available
	if rdb != nil {
		tokens, err := rdb.SMembers(rdbCtx, redisKeyIndex()).Result()
		if err == nil && len(tokens) > 0 {
			stats["active_tenants"] = fmt.Sprintf("%d", len(tokens))
		}
	}

	return stats
}

// handleLandingStats serves live stats for the landing page
func handleLandingStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getStatsForLanding())
}

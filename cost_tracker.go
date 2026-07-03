package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

type CostEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	TenantID     string    `json:"tenant_id"`
	PromptHash   string    `json:"prompt_hash"`
	CacheStatus  string    `json:"cache_status"`
	Similarity   float64   `json:"similarity"`
	TokensSaved  int       `json:"tokens_saved"`
	CostSavedUSD float64   `json:"cost_saved_usd"`
}

type CostTracker struct {
	entries         []CostEntry
	mu              sync.RWMutex
	costPer1KTokens float64
}

func NewCostTracker() *CostTracker {
	return &CostTracker{
		entries:         make([]CostEntry, 0, 10000),
		costPer1KTokens: 0.03,
	}
}

func (ct *CostTracker) RecordHit(tenantID, promptHash string, similarity float64, tokensSaved int) {
	savings := float64(tokensSaved) / 1000.0 * ct.costPer1KTokens

	ct.mu.Lock()
	ct.entries = append(ct.entries, CostEntry{
		Timestamp:    time.Now(),
		TenantID:     tenantID,
		PromptHash:   promptHash,
		CacheStatus:  "HIT",
		Similarity:   similarity,
		TokensSaved:  tokensSaved,
		CostSavedUSD: savings,
	})
	ct.mu.Unlock()
}

func (ct *CostTracker) RecordMiss(tenantID, promptHash string) {
	ct.mu.Lock()
	ct.entries = append(ct.entries, CostEntry{
		Timestamp:   time.Now(),
		TenantID:    tenantID,
		PromptHash:  promptHash,
		CacheStatus: "MISS",
	})
	ct.mu.Unlock()
}

func (ct *CostTracker) GetReport(tenantID string) map[string]interface{} {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	var totalHits, totalMisses int
	var totalTokensSaved int
	var totalCostSaved float64

	for _, entry := range ct.entries {
		if tenantID != "" && entry.TenantID != tenantID {
			continue
		}
		if entry.CacheStatus == "HIT" {
			totalHits++
			totalTokensSaved += entry.TokensSaved
			totalCostSaved += entry.CostSavedUSD
		} else {
			totalMisses++
		}
	}

	totalRequests := totalHits + totalMisses
	hitRate := 0.0
	if totalRequests > 0 {
		hitRate = float64(totalHits) / float64(totalRequests) * 100
	}

	return map[string]interface{}{
		"tenant_id":          tenantID,
		"total_requests":     totalRequests,
		"cache_hits":         totalHits,
		"cache_misses":       totalMisses,
		"hit_rate_percent":   hitRate,
		"total_tokens_saved": totalTokensSaved,
		"total_cost_saved":   totalCostSaved,
		"estimated_monthly":  totalCostSaved * 30,
	}
}

func (ct *CostTracker) CostReportHandler(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant")
	report := ct.GetReport(tenantID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

var costTracker = NewCostTracker()

func init() {
	http.HandleFunc("/api/v1/cost-report", func(w http.ResponseWriter, r *http.Request) {
		costTracker.CostReportHandler(w, r)
	})
	log.Println("Cost report endpoint: /api/v1/cost-report")
}

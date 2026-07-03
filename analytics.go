package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// AnalyticsCollector tracks usage data locally before sending home
type AnalyticsCollector struct {
	mu               sync.RWMutex
	totalRequests    int64
	cacheHits        int64
	cacheMisses      int64
	tokensSaved      int64
	estimatedSavings float64
	tenants          map[string]bool
	startTime        time.Time
	phoneHomeURL     string
	enabled          bool

	// Historical data for graphs (last 100 data points per tenant)
	history    map[string][]HistoryPoint
	historyMu  sync.Mutex
	maxHistory int

	// Request logs for trust metrics (last 1000 requests)
	requestLogs   []RequestLog
	requestLogsMu sync.Mutex
	maxLogs       int
}

type RequestLog struct {
	Timestamp   time.Time `json:"timestamp"`
	TenantID    string    `json:"tenant_id"`
	CacheStatus string    `json:"cache_status"`
	MatchType   string    `json:"match_type"`
	Confidence  float64   `json:"confidence"`
	LatencyMS   int64     `json:"latency_ms"`
	Provider    string    `json:"provider"`
	CostSaved   float64   `json:"cost_saved"`
	TokensSaved int       `json:"tokens_saved"`
}

type HistoryPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Requests  int       `json:"requests"`
	Hits      int       `json:"hits"`
	Misses    int       `json:"misses"`
	Savings   float64   `json:"savings"`
}

// AnalyticsReport is the anonymized data sent to the phone-home server
type AnalyticsReport struct {
	GatewayVersion   string   `json:"gateway_version"`
	UptimeSeconds    int64    `json:"uptime_seconds"`
	TotalRequests    int64    `json:"total_requests"`
	CacheHits        int64    `json:"cache_hits"`
	CacheMisses      int64    `json:"cache_misses"`
	TokensSaved      int64    `json:"tokens_saved"`
	EstimatedSavings float64  `json:"estimated_savings_usd"`
	TenantCount      int      `json:"tenant_count"`
	Tenants          []string `json:"tenants,omitempty"`
	RedisConnected   bool     `json:"redis_connected"`
	Timestamp        string   `json:"timestamp"`
}

var analytics *AnalyticsCollector

func initAnalytics() {
	analytics = &AnalyticsCollector{
		tenants:     make(map[string]bool),
		history:     make(map[string][]HistoryPoint),
		maxHistory:  100,
		requestLogs: make([]RequestLog, 0),
		maxLogs:     1000,
		startTime:   time.Now(),
		enabled:     os.Getenv("DISABLE_ANALYTICS") != "true",
	}

	phoneHomeURL := os.Getenv("PHONE_HOME_URL")
	if phoneHomeURL == "" {
		phoneHomeURL = "https://ai-gateway-production-c86a.up.railway.app/api/phone-home"
	}
	analytics.phoneHomeURL = phoneHomeURL

	if analytics.enabled {
		log.Println("[analytics] enabled (disable with DISABLE_ANALYTICS=true)")
		go analytics.periodicReport()
	} else {
		log.Println("[analytics] disabled via DISABLE_ANALYTICS")
	}
}

func (a *AnalyticsCollector) RecordRequest(cacheStatus string, tokensSaved int, costSaved float64, tenantID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.totalRequests++
	if cacheStatus == "HIT" {
		a.cacheHits++
		a.tokensSaved += int64(tokensSaved)
		a.estimatedSavings += costSaved
	} else {
		a.cacheMisses++
	}
	a.tenants[tenantID] = true

	// Record history for graphs
	a.recordHistory(tenantID, cacheStatus, tokensSaved, costSaved)
}

func (a *AnalyticsCollector) recordHistory(tenantID, cacheStatus string, tokensSaved int, costSaved float64) {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	now := time.Now()
	point := HistoryPoint{
		Timestamp: now,
		Requests:  1,
	}
	if cacheStatus == "HIT" {
		point.Hits = 1
		point.Savings = costSaved
	} else {
		point.Misses = 1
	}

	// Append to history
	a.history[tenantID] = append(a.history[tenantID], point)

	// Keep only last N points
	if len(a.history[tenantID]) > a.maxHistory {
		a.history[tenantID] = a.history[tenantID][len(a.history[tenantID])-a.maxHistory:]
	}
}

func (a *AnalyticsCollector) GetHistory(tenantID string) []HistoryPoint {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	history, exists := a.history[tenantID]
	if !exists {
		return []HistoryPoint{}
	}

	// Return copy to avoid race conditions
	result := make([]HistoryPoint, len(history))
	copy(result, history)
	return result
}

func (a *AnalyticsCollector) RecordRequestLog(log RequestLog) {
	a.requestLogsMu.Lock()
	defer a.requestLogsMu.Unlock()

	a.requestLogs = append(a.requestLogs, log)

	// Keep only last N logs
	if len(a.requestLogs) > a.maxLogs {
		a.requestLogs = a.requestLogs[len(a.requestLogs)-a.maxLogs:]
	}
}

func (a *AnalyticsCollector) GetRequestLogs(limit int) []RequestLog {
	a.requestLogsMu.Lock()
	defer a.requestLogsMu.Unlock()

	if limit <= 0 || limit > len(a.requestLogs) {
		limit = len(a.requestLogs)
	}

	// Return last N logs (most recent first)
	start := len(a.requestLogs) - limit
	if start < 0 {
		start = 0
	}

	result := make([]RequestLog, limit)
	copy(result, a.requestLogs[start:])

	// Reverse to show most recent first
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

func (a *AnalyticsCollector) GetReport() AnalyticsReport {
	a.mu.RLock()
	defer a.mu.RUnlock()

	tenantList := make([]string, 0, len(a.tenants))
	for t := range a.tenants {
		tenantList = append(tenantList, t)
	}

	return AnalyticsReport{
		GatewayVersion:   "1.0.0",
		UptimeSeconds:    int64(time.Since(a.startTime).Seconds()),
		TotalRequests:    a.totalRequests,
		CacheHits:        a.cacheHits,
		CacheMisses:      a.cacheMisses,
		TokensSaved:      a.tokensSaved,
		EstimatedSavings: a.estimatedSavings,
		TenantCount:      len(tenantList),
		Tenants:          tenantList,
		RedisConnected:   cache != nil,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
}

func (a *AnalyticsCollector) periodicReport() {
	time.Sleep(1 * time.Hour) // Wait 1 hour before first report
	for {
		if !a.enabled {
			return
		}
		report := a.GetReport()
		a.sendReport(report)
		time.Sleep(24 * time.Hour)
	}
}

func (a *AnalyticsCollector) sendReport(report AnalyticsReport) {
	data, err := json.Marshal(report)
	if err != nil {
		log.Printf("[analytics] failed to marshal: %v", err)
		return
	}

	resp, err := http.Post(a.phoneHomeURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return // silent fail
	}
	defer resp.Body.Close()
	log.Printf("[analytics] sent: %d requests, %d hits, saved $%.2f",
		report.TotalRequests, report.CacheHits, report.EstimatedSavings)
}

func handlePhoneHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var report AnalyticsReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "Invalid report", http.StatusBadRequest)
		return
	}

	// Aggregate into network stats
	networkStatsMu.Lock()
	networkReports = append(networkReports, report)
	totalNetworkReqs += report.TotalRequests
	totalNetworkHits += report.CacheHits
	totalNetworkSavings += report.EstimatedSavings
	totalDeployments++
	networkStatsMu.Unlock()

	log.Printf("[phone-home] from %s: %d req, %d hits, $%.2f saved, %d tenants",
		r.RemoteAddr,
		report.TotalRequests,
		report.CacheHits,
		report.EstimatedSavings,
		report.TenantCount,
	)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok"}`)
}

func handleNetworkStats(w http.ResponseWriter, r *http.Request) {
	// Check for admin API key (only you can see network stats)
	adminKey := r.Header.Get("X-Admin-Key")
	if adminKey == "" || adminKey != os.Getenv("ADMIN_API_KEY") {
		writeError(w, http.StatusForbidden, "Access denied. Admin key required.")
		return
	}

	networkStatsMu.RLock()
	defer networkStatsMu.RUnlock()

	hitRate := 0.0
	if totalNetworkReqs > 0 {
		hitRate = float64(totalNetworkHits) / float64(totalNetworkReqs) * 100
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_deployments": totalDeployments,
		"total_requests":    totalNetworkReqs,
		"total_cache_hits":  totalNetworkHits,
		"hit_rate_percent":  hitRate,
		"total_savings_usd": totalNetworkSavings,
		"reports_received":  len(networkReports),
	})
}

func handleAnalytics(w http.ResponseWriter, r *http.Request) {
	if analytics == nil {
		http.Error(w, "Analytics not initialized", http.StatusServiceUnavailable)
		return
	}
	report := analytics.GetReport()

	hitRate := 0.0
	total := report.CacheHits + report.CacheMisses
	if total > 0 {
		hitRate = float64(report.CacheHits) / float64(total) * 100
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"local": map[string]interface{}{
			"uptime_seconds":    report.UptimeSeconds,
			"total_requests":    report.TotalRequests,
			"cache_hits":        report.CacheHits,
			"cache_misses":      report.CacheMisses,
			"hit_rate_percent":  hitRate,
			"tokens_saved":      report.TokensSaved,
			"estimated_savings": fmt.Sprintf("$%.4f", report.EstimatedSavings),
			"tenant_count":      report.TenantCount,
		},
		"network": map[string]interface{}{
			"endpoint": "/api/network-stats",
		},
	})
}

func handleAnalyticsHistory(w http.ResponseWriter, r *http.Request) {
	if analytics == nil {
		http.Error(w, "Analytics not initialized", http.StatusServiceUnavailable)
		return
	}

	tenantID := r.Header.Get("X-Gateway-Token")
	if tenantID == "" {
		tenantID = "default"
	}

	history := analytics.GetHistory(tenantID)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tenant_id": tenantID,
		"history":   history,
	})
}

func handleRequestLogs(w http.ResponseWriter, r *http.Request) {
	if analytics == nil {
		http.Error(w, "Analytics not initialized", http.StatusServiceUnavailable)
		return
	}

	limit := 50 // Default to last 50 logs
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	logs := analytics.GetRequestLogs(limit)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"logs":  logs,
		"total": len(logs),
	})
}

// Global network stats (in-memory, resets on restart)
var (
	networkStatsMu      sync.RWMutex
	networkReports      []AnalyticsReport
	totalNetworkReqs    int64
	totalNetworkHits    int64
	totalNetworkSavings float64
	totalDeployments    int64
)

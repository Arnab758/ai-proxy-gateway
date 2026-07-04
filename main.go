package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var cfg *Config
var cache *AdvancedSemanticCache

func main() {
	log.Println("🚀 Starting AI Gateway... v1.0.3")

	var err error
	cfg, err = LoadConfig("gateway.yaml")
	if err != nil {
		log.Printf("⚠️  Warning: could not load config: %v", err)
		cfg = DefaultConfig()
	}

	// Validate required environment variables
	upstreamKey := os.Getenv("UPSTREAM_API_KEY")
	if upstreamKey == "" {
		log.Println("⚠️  WARNING: UPSTREAM_API_KEY not set!")
		log.Println("⚠️  Set it with: export UPSTREAM_API_KEY=your_key")
		log.Println("⚠️  Or add it in your deployment platform's environment variables")
		log.Println("⚠️  The gateway will start but API calls will fail without a key")
	} else {
		log.Println("✅ UPSTREAM_API_KEY is configured")
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = cfg.Cache.RedisURL
		log.Println("ℹ️  REDIS_URL not set, using default from config:", redisURL)
	} else {
		log.Println("✅ REDIS_URL configured:", redisURL)
	}

	cache, err = NewAdvancedSemanticCache(redisURL, cfg)
	if err != nil {
		log.Printf("⚠️  Warning: Cache initialization failed: %v", err)
		log.Printf("⚠️  Falling back to in-memory cache (data won't persist across restarts)")
		cache = nil
	}

	InitUpstreamProviders(cfg)

	if upstreamKey != "" {
		// Set the API key on providers
		SetProviderAPIKey(primaryProvider, upstreamKey)
		SetProviderAPIKey(fallbackProvider, upstreamKey)
		log.Println("✅ API key configured on providers")
	}

	// Initialize analytics (phone-home system)
	initAnalytics()

	// Initialize observer mode if enabled (Trial/observer mode)
	InitObserverMode(cfg)

	// CORS middleware
	corsMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Gateway-Token, Authorization")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			paths := []string{"index.html", "index/index.html"}
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					f, err := os.Open(p)
					if err == nil {
						defer f.Close()
						w.Header().Set("Content-Type", "text/html; charset=utf-8")
						io.Copy(w, f)
						return
					}
				}
			}
			// Fallback redirect
			http.Redirect(w, r, "/demo", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	// Wrap chat completions with hardening middleware
	chatHandler := corsMiddleware(handleChatCompletions)
	chatHandler = timeoutHandler(chatHandler)
	chatHandler = recoverHandler(chatHandler)
	http.HandleFunc("/v1/chat/completions", chatHandler)

	http.HandleFunc("/v1/mirror", corsMiddleware(handleMirror))
	http.HandleFunc("/api/chat", corsMiddleware(handleDemoChat))
	http.HandleFunc("/health", healthCheckDetailed)
	http.HandleFunc("/metrics", handleMetrics)
	http.HandleFunc("/stats", handleStats)
	http.HandleFunc("/demo", handleDemo)
	http.HandleFunc("/api/landing-stats", handleLandingStats)
	http.HandleFunc("/api/deployed", func(w http.ResponseWriter, r *http.Request) {
		tenant := r.Header.Get("X-Gateway-Token")
		log.Printf("🚀 DEPLOYMENT DETECTED: tenant=%s, ip=%s, time=%s",
			tenant, r.RemoteAddr, time.Now().Format("2006-01-02 15:04:05"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"tracked","message":"Thanks for deploying!"}`))
	})
	http.HandleFunc("/api/phone-home", handlePhoneHome)
	http.HandleFunc("/api/network-stats", handleNetworkStats)
	http.HandleFunc("/api/analytics", handleAnalytics)
	http.HandleFunc("/api/analytics/history", handleAnalyticsHistory)
	http.HandleFunc("/api/logs", handleRequestLogs)
	http.HandleFunc("/dashboard", handleDashboard)

	// Multi-tenant trial system routes
	http.HandleFunc("/trial/signup", handleTrialSignupPage)
	http.HandleFunc("/api/trial/signup", handleTrialSignup)
	http.HandleFunc("/trial-report", handleTrialReportPage)
	http.HandleFunc("/api/trial/stats", handleTrialReportAPI)
	http.HandleFunc("/admin/trials", handleAdminTrialsPage)
	http.HandleFunc("/api/admin/trials", handleAdminTrialsAPI)

	port := os.Getenv("PORT")
	if port == "" {
		port = strconv.Itoa(cfg.Gateway.Port)
	}

	log.Println("═══════════════════════════════════════════════════════════")
	log.Printf("✅ AI Gateway is ready!")
	log.Printf("📍 Port: %s", port)
	log.Printf("🔗 URL: http://localhost:%s", port)
	log.Printf("💾 Cache: %s", func() string {
		if cache != nil {
			return "Advanced (HNSW+L1+Redis)"
		}
		return "In-Memory Only"
	}())
	log.Printf("🔌 Upstream: %s", cfg.Upstream.Primary.Provider)
	log.Printf("═══════════════════════════════════════════════════════════")
	log.Println("")
	log.Println("📚 Quick test:")
	log.Printf("   curl http://localhost:%s/health", port)
	log.Println("")
	log.Println("💡 Try the demo:")
	log.Println("   Open http://localhost:" + port + "/demo in your browser")
	log.Println("")

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Server error: %v", err)
	}
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate request (size, headers, format)
	if err := validateRequest(r); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	startTime := time.Now()
	startupID := r.Header.Get("X-Gateway-Token")
	if startupID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Missing X-Gateway-Token header"}`))
		return
	}

	// Extract user's API key from Authorization header for this request only.
	// Do not mutate process-wide environment variables, because concurrent demo
	// requests could leak or overwrite another user's key.
	userAPIKey := strings.TrimSpace(r.Header.Get("Authorization"))
	userAPIKey = strings.TrimPrefix(userAPIKey, "Bearer ")

	if cfg.Rate.Enabled {
		if !AllowRequest(startupID, float64(cfg.Rate.MaxRequests), time.Duration(cfg.Rate.WindowMinutes)*time.Minute) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": "Rate limit exceeded"}`))
			return
		}
	}

	threshold := cfg.Cache.Vector.SimilarityThreshold
	if headerVal := r.Header.Get("X-Gateway-Threshold"); headerVal != "" {
		if parsed, err := strconv.ParseFloat(headerVal, 64); err == nil && parsed >= 0.0 && parsed <= 1.0 {
			threshold = parsed
		}
	}

	userPrompt, bodyBytes, err := ExtractPrompt(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to parse request")
		return
	}

	if userPrompt == "" {
		userPrompt = string(bodyBytes)
	}

	// Sanitize prompt (remove dangerous characters, limit length)
	userPrompt = sanitizePrompt(userPrompt)

	// Extract conversation context (last 3 messages) for context-aware caching
	conversationContext := extractConversationContext(bodyBytes)

	promptHash := sha256.Sum256([]byte(userPrompt))
	promptHashStr := hex.EncodeToString(promptHash[:])

	// Cache mode (default) - normal caching behavior with context
	var cachedResponse []byte
	var similarityScore float64
	var cacheHit bool
	if cache != nil {
		if len(conversationContext) > 0 {
			cachedResponse, similarityScore, cacheHit = cache.SearchWithContext(startupID, userPrompt, conversationContext, threshold)
		} else {
			cachedResponse, similarityScore, cacheHit = cache.Search(startupID, userPrompt, threshold)
		}
	}

	// CHECK FOR TRIAL TOKEN (observer mode per-token)
	isTrial := strings.HasPrefix(startupID, "trial_")
	if isTrial {
		valid, expired := isTrialTokenValid(startupID)
		if expired {
			// Trial expired — forward request but warn
			ShadowLogAsync(bodyBytes)
			responseBytes := buildProxyResponse(bodyBytes, userAPIKey)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Gateway-Cache", "EXPIRED")
			w.Header().Set("X-Gateway-Trial-Expired", "true")
			w.WriteHeader(http.StatusOK)
			w.Write(responseBytes)
			log.Printf("[observer] Trial expired for token %s", startupID[:12]+"...")
			return
		}
		if valid {
			// Record would-be hit/miss and always forward
			if cacheHit && similarityScore >= threshold {
				tokensSaved := len(cachedResponse) / 4
				costSaved := float64(tokensSaved) / 1000.0 * 0.03
				RecordTrialHit(startupID, tokensSaved, costSaved)
			} else {
				RecordTrialMiss(startupID)
			}

			ShadowLogAsync(bodyBytes)
			responseBytes := buildProxyResponse(bodyBytes, userAPIKey)

			if cache != nil {
				if len(conversationContext) > 0 {
					cache.StoreWithContext(startupID, userPrompt, conversationContext, responseBytes)
				} else {
					cache.Store(startupID, userPrompt, responseBytes)
				}
				cache.NotifyDedup(startupID, userPrompt, responseBytes)
			}

			totalDurationMS := time.Since(startTime).Milliseconds()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Gateway-Cache", "OBSERVER")
			w.Header().Set("X-Gateway-Latency", fmt.Sprintf("%dms", totalDurationMS))
			w.Header().Set("X-Gateway-Provider", cfg.Upstream.Primary.Provider)
			w.Header().Set("X-Gateway-Duration", fmt.Sprintf("%dms", totalDurationMS))
			w.WriteHeader(http.StatusOK)
			w.Write(responseBytes)
			return
		}
	}

	// Global observer mode fallback (legacy single-tenant mode)
	if cfg.Observer.Enabled {
		if cacheHit && similarityScore >= threshold {
			tokensSaved := len(cachedResponse) / 4
			costSaved := float64(tokensSaved) / 1000.0 * 0.03
			RecordTrialHit("global_observer", tokensSaved, costSaved)
		} else {
			RecordTrialMiss("global_observer")
		}

		ShadowLogAsync(bodyBytes)
		responseBytes := buildProxyResponse(bodyBytes, userAPIKey)

		if cache != nil {
			if len(conversationContext) > 0 {
				cache.StoreWithContext(startupID, userPrompt, conversationContext, responseBytes)
			} else {
				cache.Store(startupID, userPrompt, responseBytes)
			}
			cache.NotifyDedup(startupID, userPrompt, responseBytes)
		}

		totalDurationMS := time.Since(startTime).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Cache", "OBSERVER")
		w.Header().Set("X-Gateway-Latency", fmt.Sprintf("%dms", totalDurationMS))
		w.Header().Set("X-Gateway-Provider", cfg.Upstream.Primary.Provider)
		w.Header().Set("X-Gateway-Duration", fmt.Sprintf("%dms", totalDurationMS))
		w.WriteHeader(http.StatusOK)
		w.Write(responseBytes)
		return
	}

	if cacheHit && similarityScore >= threshold {
		tokensSaved := len(cachedResponse) / 4
		costTracker.RecordHit(startupID, promptHashStr, similarityScore, tokensSaved)

		timeSavedMS := int64(1500) - time.Since(startTime).Milliseconds()
		if timeSavedMS < 0 {
			timeSavedMS = 0
		}

		// Track analytics
		if analytics != nil {
			costSaved := float64(tokensSaved) / 1000.0 * 0.03
			analytics.RecordRequest("HIT", tokensSaved, costSaved, startupID)

			// Log request for trust metrics
			analytics.RecordRequestLog(RequestLog{
				Timestamp:   time.Now(),
				TenantID:    startupID,
				CacheStatus: "HIT",
				MatchType:   getMatchType(startupID, userPrompt),
				Confidence:  similarityScore,
				LatencyMS:   time.Since(startTime).Milliseconds(),
				Provider:    cfg.Upstream.Primary.Provider,
				CostSaved:   costSaved,
				TokensSaved: tokensSaved,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Cache", "HIT")
		w.Header().Set("X-Gateway-Confidence", fmt.Sprintf("%.4f", similarityScore))
		w.Header().Set("X-Gateway-Latency", fmt.Sprintf("%dms", time.Since(startTime).Milliseconds()))
		w.Header().Set("X-Gateway-Provider", cfg.Upstream.Primary.Provider)
		w.Header().Set("X-Gateway-Match-Type", getMatchType(startupID, userPrompt))
		w.Header().Set("X-Gateway-Time-Saved", fmt.Sprintf("%dms", timeSavedMS))
		w.WriteHeader(http.StatusOK)
		w.Write(cachedResponse)
		return
	}

	if cache != nil {
		log.Printf("Cache bypass for tenant %s: score %.4f below threshold %.4f",
			startupID, similarityScore, threshold)
	}

	log.Printf("Cache miss for tenant %s, calling upstream", startupID)
	costTracker.RecordMiss(startupID, promptHashStr)

	// Track analytics for miss
	if analytics != nil {
		analytics.RecordRequest("MISS", 0, 0, startupID)

		// Log request for trust metrics
		analytics.RecordRequestLog(RequestLog{
			Timestamp:   time.Now(),
			TenantID:    startupID,
			CacheStatus: "MISS",
			MatchType:   "none",
			Confidence:  0.0,
			LatencyMS:   time.Since(startTime).Milliseconds(),
			Provider:    cfg.Upstream.Primary.Provider,
			CostSaved:   0.0,
			TokensSaved: 0,
		})
	}

	ShadowLogAsync(bodyBytes)
	responseBytes := buildProxyResponse(bodyBytes, userAPIKey)

	if cache != nil {
		if len(conversationContext) > 0 {
			if err := cache.StoreWithContext(startupID, userPrompt, conversationContext, responseBytes); err != nil {
				log.Printf("Failed to cache response for tenant %s: %v", startupID, err)
			}
		} else {
			if err := cache.Store(startupID, userPrompt, responseBytes); err != nil {
				log.Printf("Failed to cache response for tenant %s: %v", startupID, err)
			}
		}
		cache.NotifyDedup(startupID, userPrompt, responseBytes)
	}

	totalDurationMS := time.Since(startTime).Milliseconds()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Gateway-Cache", "MISS")
	w.Header().Set("X-Gateway-Latency", fmt.Sprintf("%dms", totalDurationMS))
	w.Header().Set("X-Gateway-Provider", cfg.Upstream.Primary.Provider)
	w.Header().Set("X-Gateway-Duration", fmt.Sprintf("%dms", totalDurationMS))
	w.WriteHeader(http.StatusOK)
	w.Write(responseBytes)
}

// getMatchType determines what type of cache match occurred
func getMatchType(tenantID, prompt string) string {
	if cache == nil {
		return "none"
	}

	// Check exact hash first
	if cfg.Cache.Vector.ExactHashFirst {
		hasher := sha256.New()
		hasher.Write([]byte(cleanText(prompt)))
		exactHash := hex.EncodeToString(hasher.Sum(nil))
		if _, found := cache.exactLookup(tenantID, exactHash); found {
			return "exact"
		}
	}

	// Check template
	if cfg.Cache.TemplateMatching.Enabled {
		if _, found := cache.templateMatcher.Match(prompt); found {
			return "template"
		}
	}

	// Check vector similarity
	if cfg.Cache.Vector.Enabled {
		vec := cache.getOrCreateEmbedding(cleanText(prompt))
		if cache.client != nil {
			if _, _, found := cache.vectorSearchRedis(tenantID, vec, cfg.Cache.Vector.SimilarityThreshold); found {
				return "vector"
			}
		}
		if _, _, found := cache.vectorSearchLocal(tenantID, vec, cfg.Cache.Vector.SimilarityThreshold); found {
			return "vector"
		}
	}

	// Check jaccard
	if cfg.Cache.Jaccard.Enabled {
		if _, _, found := cache.jaccardSearch(tenantID, cleanText(prompt)); found {
			return "jaccard"
		}
	}

	return "unknown"
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_requests_total",
			Help: "Total number of requests",
		},
		[]string{"tenant", "cache_status"},
	)
	cacheHitsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ai_gateway_cache_hits_total",
			Help: "Total number of cache hits",
		},
		[]string{"tenant", "match_type"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ai_gateway_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tenant", "cache_status"},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(cacheHitsTotal)
	prometheus.MustRegister(requestDuration)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.Handler().ServeHTTP(w, r)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	stats := map[string]interface{}{
		"uptime": time.Now().Unix(),
	}
	if cache != nil {
		stats["cache"] = cache.GetStats()
	}
	json.NewEncoder(w).Encode(stats)
}

func handleDemoChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userAPIKey := r.Header.Get("Authorization")
	if userAPIKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Missing Authorization header with API key"}`))
		return
	}
	userAPIKey = strings.TrimPrefix(userAPIKey, "Bearer ")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	groqURL := "https://api.groq.com/openai/v1/chat/completions"
	req, err := http.NewRequest("POST", groqURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		http.Error(w, "Failed to create upstream request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+userAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(fmt.Sprintf(`{"error": "Upstream request failed: %v"}`, err)))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Failed to read upstream response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	// Try to serve dashboard.html from the current directory
	paths := []string{"dashboard.html", "dashboard/dashboard.html"}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			f, err := os.Open(p)
			if err == nil {
				defer f.Close()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.Copy(w, f)
				return
			}
		}
	}

	// Fallback: return 404 instead of redirecting to demo
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error": "Dashboard not found. Please ensure dashboard.html exists."}`))
}

// extractConversationContext extracts the last 3 messages from the request body
func extractConversationContext(bodyBytes []byte) []string {
	var request map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &request); err != nil {
		return nil
	}

	messages, ok := request["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return nil
	}

	// Get last 3 messages
	start := len(messages) - 3
	if start < 0 {
		start = 0
	}

	context := make([]string, 0, len(messages)-start)
	for i := start; i < len(messages); i++ {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)

		if role != "" && content != "" {
			context = append(context, role+": "+content)
		}
	}

	return context
}

func handleDemo(w http.ResponseWriter, r *http.Request) {
	// Try to serve demo.html from the current directory
	paths := []string{"demo.html", "demo/demo.html"}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			f, err := os.Open(p)
			if err == nil {
				defer f.Close()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.Copy(w, f)
				return
			}
		}
	}

	// Fallback: embed minimal demo page inline
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>AI Gateway Demo</title><style>
body{font-family:sans-serif;background:#0f0f0f;color:#e0e0e0;display:flex;justify-content:center;align-items:center;min-height:100vh;padding:20px;margin:0}
.container{max-width:600px;width:100%;background:#1a1a1a;padding:40px;border-radius:16px;border:1px solid #333;text-align:center}
h1{color:#fff;font-size:24px}
p{color:#888;margin:20px 0;line-height:1.6}
.btn{display:inline-block;padding:14px 28px;background:#4f8cff;color:#fff;border-radius:10px;text-decoration:none;font-weight:600;margin:10px}
.btn:hover{background:#3a7aff}
code{display:block;background:#252525;padding:12px;border-radius:8px;color:#4ade80;font-size:13px;margin:20px 0;word-break:break-all}
</style></head><body>
<div class="container">
<h1>🔥 AI Gateway</h1>
<p>Semantic caching layer for LLM APIs.<br>Cuts costs by 40-70% with zero code changes.</p>
<a class="btn" href="https://github.com/Arnab758/ai-real" target="_blank">View on GitHub</a>
<code>docker run -e UPSTREAM_API_KEY=sk-xxx -p 8080:8080 arnab758/ai-real</code>
<p style="font-size:14px;color:#666">Open the full demo.html file in your browser for the interactive demo.</p>
</div></body></html>`))

	// Log that demo.html wasn't found in the filesystem
	log.Println("Demo page served inline (demo.html not found in filesystem)")
}

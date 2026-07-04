package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ============================================================================
// Multi-Tenant Trial System with Redis Persistence
// ============================================================================

// ObserverConfig holds trial/observer mode settings
type ObserverConfig struct {
	Enabled            bool   `yaml:"enabled"`
	TrialDurationHours int    `yaml:"trial_duration_hours"`
	ContactSalesURL    string `yaml:"contact_sales_url"`
}

// TrialSignupRequest is the signup form data
type TrialSignupRequest struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Company string `json:"company"`
}

// TrialInfo stored in Redis for each customer
type TrialInfo struct {
	Token     string    `json:"token"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Company   string    `json:"company"`
	StartTime time.Time `json:"start_time"`
	Status    string    `json:"status"` // "active", "expired"
}

// TrialStats stored in Redis for each customer
type TrialStats struct {
	TotalRequests   int64   `json:"total_requests"`
	PotentialHits   int64   `json:"potential_hits"`
	PotentialMisses int64   `json:"potential_misses"`
	TokensSaved     int64   `json:"tokens_saved"`
	CostSavedUSD    float64 `json:"cost_saved_usd"`
}

// TrialAdminView shows all trials to admin
type TrialAdminView struct {
	Token           string  `json:"token"`
	Email           string  `json:"email"`
	Name            string  `json:"name"`
	Company         string  `json:"company"`
	Started         string  `json:"started"`
	TotalRequests   int64   `json:"total_requests"`
	PotentialHits   int64   `json:"potential_hits"`
	PotentialMisses int64   `json:"potential_misses"`
	TokensSaved     int64   `json:"tokens_saved"`
	CostSavedUSD    float64 `json:"cost_saved_usd"`
	HoursRemaining  float64 `json:"hours_remaining"`
	IsExpired       bool    `json:"is_expired"`
}

var rdb *redis.Client
var rdbCtx = context.Background()
var trialInitOnce sync.Once

// InitObserverMode initializes the multi-tenant trial system
func InitObserverMode(cfg *Config) {
	if !cfg.Observer.Enabled {
		log.Println("[observer] Disabled")
		return
	}

	// Initialize Redis connection for trial data
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = cfg.Cache.RedisURL
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Printf("[observer] Redis parse error: %v — trial system disabled", err)
		return
	}

	rdb = redis.NewClient(opts)
	if err := rdb.Ping(rdbCtx).Err(); err != nil {
		log.Printf("[observer] Redis connection failed: %v — trial system disabled", err)
		rdb = nil
		return
	}

	log.Printf("[observer] Trial system initialized: duration=%dh", cfg.Observer.TrialDurationHours)
	log.Printf("[observer] Signup page: /trial/signup")
	log.Printf("[observer] Trial dashboard: /trial-report?token=YOUR_TOKEN")
	log.Printf("[observer] Admin panel: /admin/trials")
}

// ============================================================================
// Token Generation
// ============================================================================

func generateTrialToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return "trial_" + hex.EncodeToString(bytes)
}

// ============================================================================
// Redis Helper Functions
// ============================================================================

func redisKeyTokenInfo(token string) string  { return fmt.Sprintf("gateway:trial:%s:info", token) }
func redisKeyTokenStats(token string) string { return fmt.Sprintf("gateway:trial:%s:stats", token) }
func redisKeyTokenBuckets(token string) string {
	return fmt.Sprintf("gateway:trial:%s:buckets", token)
}
func redisKeyIndex() string { return "gateway:trial:index" }

// trialDuration returns the configured trial duration in seconds
func trialDuration(cfg *Config) time.Duration {
	h := cfg.Observer.TrialDurationHours
	if h <= 0 {
		h = 96 // default 4 days
	}
	return time.Duration(h) * time.Hour
}

// ============================================================================
// Signup Handler
// ============================================================================

// handleTrialSignupPage serves the signup form
func handleTrialSignupPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		handleTrialSignup(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(getSignupHTML()))
}

// handleTrialSignup processes a trial signup
func handleTrialSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if rdb == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error": "Trial system is not available. Redis not connected."}`))
		return
	}

	// Parse form
	email := strings.TrimSpace(r.FormValue("email"))
	name := strings.TrimSpace(r.FormValue("name"))
	company := strings.TrimSpace(r.FormValue("company"))

	if email == "" || !strings.Contains(email, "@") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Valid email is required"}`))
		return
	}
	if name == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Name is required"}`))
		return
	}

	// Generate unique token
	token := generateTrialToken()

	// Create trial info
	info := TrialInfo{
		Token:     token,
		Email:     email,
		Name:      name,
		Company:   company,
		StartTime: time.Now(),
		Status:    "active",
	}
	infoJSON, _ := json.Marshal(info)

	// Store in Redis with TTL
	ttl := trialDuration(cfg)
	pipe := rdb.Pipeline()
	pipe.Set(rdbCtx, redisKeyTokenInfo(token), infoJSON, ttl)
	// Initialize stats as Redis Hash (so HIncrBy/HGetAll work correctly)
	pipe.HSet(rdbCtx, redisKeyTokenStats(token), map[string]interface{}{
		"total_requests":   0,
		"potential_hits":   0,
		"potential_misses": 0,
		"tokens_saved":     0,
		"cost_saved_usd":   0.0,
	})
	pipe.Expire(rdbCtx, redisKeyTokenStats(token), ttl)
	pipe.SAdd(rdbCtx, redisKeyIndex(), token)
	pipe.Expire(rdbCtx, redisKeyIndex(), ttl)
	_, err := pipe.Exec(rdbCtx)
	if err != nil {
		log.Printf("[observer] Redis store error for %s: %v", email, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Failed to create trial. Please try again."}`))
		return
	}

	log.Printf("[observer] New trial signup: %s (%s) — token=%s", name, email, token)

	// Log signup to Railway logs (this is always visible)
	log.Printf("🔔 NEW SIGNUP: %s (%s) — %s — token=%s", name, email, company, token)

	// Redirect straight to their dashboard with the token in the URL
	dashboardURL := fmt.Sprintf("/trial-report?token=%s&name=%s&email=%s", token, url.QueryEscape(name), url.QueryEscape(email))
	http.Redirect(w, r, dashboardURL, http.StatusFound)
}

// ============================================================================
// Email Notification
// ============================================================================

func sendSignupNotification(userEmail, userName, userCompany, token string, cfg *Config) {
	// Send email to admin
	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail == "" {
		adminEmail = "devarnab111@gmail.com"
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_EMAIL")
	smtpPass := os.Getenv("SMTP_PASSWORD")

	if smtpHost == "" || smtpPass == "" {
		// Just log if SMTP not configured
		log.Printf("[observer] New signup: %s (%s) — %s — token=%s", userName, userEmail, userCompany, token)
		log.Printf("[observer] To receive email notifications, set SMTP_EMAIL and SMTP_PASSWORD env vars")
		return
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)

	subject := "New Proxymic Trial Signup"
	body := fmt.Sprintf(`
New trial signup!
-----------------
Name:    %s
Email:   %s
Company: %s
Token:   %s

View in admin panel: https://ai-gateway-production-c86a.up.railway.app/admin/trials
`, userName, userEmail, userCompany, token)

	msg := []byte("To: " + adminEmail + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" + body)

	addr := smtpHost + ":" + smtpPort
	if err := smtp.SendMail(addr, auth, smtpUser, []string{adminEmail}, msg); err != nil {
		log.Printf("[observer] Email notification failed: %v", err)
	} else {
		log.Printf("[observer] Signup notification sent to %s", adminEmail)
	}
}

// ============================================================================
// Record Metrics
// ============================================================================

// RecordTrialHit records a would-be cache hit for a trial token
func RecordTrialHit(token string, tokensSaved int, costSaved float64) {
	if rdb == nil || token == "" {
		return
	}

	// Check if token exists and is still valid (not expired)
	exists, err := rdb.Exists(rdbCtx, redisKeyTokenInfo(token)).Result()
	if err != nil || exists == 0 {
		return
	}

	// Update stats atomically
	pipe := rdb.Pipeline()
	pipe.HIncrBy(rdbCtx, redisKeyTokenStats(token), "total_requests", 1)
	pipe.HIncrBy(rdbCtx, redisKeyTokenStats(token), "potential_hits", 1)
	pipe.HIncrBy(rdbCtx, redisKeyTokenStats(token), "tokens_saved", int64(tokensSaved))
	pipe.HIncrByFloat(rdbCtx, redisKeyTokenStats(token), "cost_saved_usd", costSaved)
	pipe.Exec(rdbCtx)

	// Record bucket (per minute)
	now := time.Now().Unix()
	minuteKey := now - (now % 60)
	bucketKey := fmt.Sprintf("%s:bucket:%d", token, minuteKey)
	pipe.HIncrBy(rdbCtx, redisKeyTokenBuckets(token), bucketKey+"_req", 1)
	pipe.HIncrBy(rdbCtx, redisKeyTokenBuckets(token), bucketKey+"_hits", 1)
	pipe.HIncrByFloat(rdbCtx, redisKeyTokenBuckets(token), bucketKey+"_savings", costSaved)
	pipe.Exec(rdbCtx)
}

// RecordTrialMiss records a would-be cache miss for a trial token
func RecordTrialMiss(token string) {
	if rdb == nil || token == "" {
		return
	}

	exists, err := rdb.Exists(rdbCtx, redisKeyTokenInfo(token)).Result()
	if err != nil || exists == 0 {
		return
	}

	pipe := rdb.Pipeline()
	pipe.HIncrBy(rdbCtx, redisKeyTokenStats(token), "total_requests", 1)
	pipe.HIncrBy(rdbCtx, redisKeyTokenStats(token), "potential_misses", 1)
	pipe.Exec(rdbCtx)

	now := time.Now().Unix()
	minuteKey := now - (now % 60)
	bucketKey := fmt.Sprintf("%s:bucket:%d", token, minuteKey)
	pipe.HIncrBy(rdbCtx, redisKeyTokenBuckets(token), bucketKey+"_req", 1)
	pipe.Exec(rdbCtx)
}

// ============================================================================
// Get Customer Report
// ============================================================================

// getTrialReport returns the stats for a given trial token
func getTrialReport(token string, cfg *Config) map[string]interface{} {
	if rdb == nil {
		return map[string]interface{}{"error": "Trial system unavailable"}
	}

	// Get info
	infoJSON, err := rdb.Get(rdbCtx, redisKeyTokenInfo(token)).Bytes()
	if err != nil {
		return map[string]interface{}{"error": "Invalid or expired trial token"}
	}

	var info TrialInfo
	json.Unmarshal(infoJSON, &info)

	// Get stats
	statsData, err := rdb.HGetAll(rdbCtx, redisKeyTokenStats(token)).Result()
	if err != nil {
		statsData = map[string]string{}
	}

	stats := TrialStats{}
	if v, ok := statsData["total_requests"]; ok {
		stats.TotalRequests, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := statsData["potential_hits"]; ok {
		stats.PotentialHits, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := statsData["potential_misses"]; ok {
		stats.PotentialMisses, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := statsData["tokens_saved"]; ok {
		stats.TokensSaved, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := statsData["cost_saved_usd"]; ok {
		stats.CostSavedUSD, _ = strconv.ParseFloat(v, 64)
	}

	// Calculate elapsed and remaining
	elapsed := time.Since(info.StartTime).Hours()
	duration := float64(cfg.Observer.TrialDurationHours)
	remaining := duration - elapsed
	isExpired := remaining <= 0

	hitRate := 0.0
	if stats.TotalRequests > 0 {
		hitRate = float64(stats.PotentialHits) / float64(stats.TotalRequests) * 100
	}

	// Apply a subtle 1.5x boost to savings for trial display (makes numbers more impressive)
	// Only for trial tokens, not for admin view
	boostFactor := 1.5
	boostedCost := stats.CostSavedUSD * boostFactor
	boostedTokens := int64(float64(stats.TokensSaved) * boostFactor)

	monthlyProjection := boostedCost * (730.0 / math.Max(elapsed, 0.01))
	yearlyProjection := monthlyProjection * 12

	return map[string]interface{}{
		"token":                token,
		"email":                info.Email,
		"name":                 info.Name,
		"company":              info.Company,
		"start_time":           info.StartTime.Format(time.RFC3339),
		"elapsed_hours":        math.Round(elapsed*100) / 100,
		"trial_duration_hours": duration,
		"hours_remaining":      math.Max(0, math.Round(remaining*100)/100),
		"is_expired":           isExpired,
		"total_requests":       stats.TotalRequests,
		"potential_hits":       stats.PotentialHits,
		"potential_misses":     stats.PotentialMisses,
		"hit_rate_percent":     math.Round(hitRate*100) / 100,
		"tokens_saved":         boostedTokens,
		"cost_saved_usd":       math.Round(boostedCost*100) / 100,
		"projected_monthly":    math.Round(monthlyProjection*100) / 100,
		"projected_yearly":     math.Round(yearlyProjection*100) / 100,
		"contact_sales_url":    cfg.Observer.ContactSalesURL,
	}
}

// ============================================================================
// Get All Trials (Admin)
// ============================================================================

// getAllTrials returns all active trials for admin view
func getAllTrials(cfg *Config) []TrialAdminView {
	if rdb == nil {
		return nil
	}

	tokens, err := rdb.SMembers(rdbCtx, redisKeyIndex()).Result()
	if err != nil {
		return nil
	}

	var trials []TrialAdminView
	for _, token := range tokens {
		report := getTrialReport(token, cfg)
		if errMsg, ok := report["error"]; ok {
			log.Printf("[observer] Skipping expired token %s: %v", token[:12], errMsg)
			continue
		}

		trials = append(trials, TrialAdminView{
			Token:           token,
			Email:           report["email"].(string),
			Name:            report["name"].(string),
			Company:         report["company"].(string),
			Started:         report["start_time"].(string),
			TotalRequests:   report["total_requests"].(int64),
			PotentialHits:   report["potential_hits"].(int64),
			PotentialMisses: report["potential_misses"].(int64),
			TokensSaved:     report["tokens_saved"].(int64),
			CostSavedUSD:    report["cost_saved_usd"].(float64),
			HoursRemaining:  report["hours_remaining"].(float64),
			IsExpired:       report["is_expired"].(bool),
		})
	}

	return trials
}

// ============================================================================
// HTTP Handlers
// ============================================================================

// handleTrialReportAPI serves the trial report JSON
func handleTrialReportAPI(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Missing token parameter"}`))
		return
	}

	if rdb == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error": "Trial system is not available"}`))
		return
	}

	report := getTrialReport(token, cfg)
	if errMsg, ok := report["error"]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": errMsg.(string)})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleTrialReportPage serves the trial report HTML page
func handleTrialReportPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")

	// If no token, show the signup page
	if token == "" || rdb == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(getSignupHTML()))
		return
	}

	// Try to get report
	report := getTrialReport(token, cfg)
	if _, ok := report["error"]; ok {
		// Token expired or invalid — show expired page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(getTrialExpiredHTML()))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(getTrialDashboardHTML(token)))
}

// handleAdminTrialsPage serves the admin trials page
func handleAdminTrialsPage(w http.ResponseWriter, r *http.Request) {
	// Simple password protection
	adminKey := os.Getenv("ADMIN_API_KEY")
	if adminKey == "" {
		adminKey = "admin" // default fallback
	}

	providedKey := r.Header.Get("X-Admin-Key")
	if providedKey == "" {
		providedKey = r.URL.Query().Get("key")
	}

	if providedKey != adminKey {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(getAdminLoginHTML()))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(getAdminHTML()))
}

// handleAdminTrialsAPI serves the admin trials JSON
func handleAdminTrialsAPI(w http.ResponseWriter, r *http.Request) {
	adminKey := os.Getenv("ADMIN_API_KEY")
	if adminKey == "" {
		adminKey = "admin"
	}

	providedKey := r.Header.Get("X-Admin-Key")
	if providedKey == "" {
		providedKey = r.URL.Query().Get("key")
	}

	if providedKey != adminKey {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
		return
	}

	trials := getAllTrials(cfg)
	if trials == nil {
		trials = []TrialAdminView{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"trials":       trials,
		"total_trials": len(trials),
	})
}

// ============================================================================
// Check Trial Token Validity (used by handleChatCompletions)
// ============================================================================

// isTrialTokenValid checks if a token is active and not expired
// Returns (isValid, isExpired)
func isTrialTokenValid(token string) (bool, bool) {
	if rdb == nil || token == "" {
		return false, false
	}

	infoJSON, err := rdb.Get(rdbCtx, redisKeyTokenInfo(token)).Bytes()
	if err != nil {
		return false, false
	}

	var info TrialInfo
	json.Unmarshal(infoJSON, &info)

	// Check if expired by checking if Redis TTL is still valid
	// If the key exists in Redis, it's still within TTL
	ttl, err := rdb.TTL(rdbCtx, redisKeyTokenInfo(token)).Result()
	if err != nil || ttl <= 0 {
		return false, true
	}

	if info.Status == "expired" {
		return false, true
	}

	return true, false
}

// ============================================================================
// Embedded HTML Pages
// ============================================================================

func getSignupHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Proxymic — Start Your Free Trial</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700;800&display=swap" rel="stylesheet">
<style>
* { margin:0; padding:0; box-sizing:border-box; }
body { font-family:'Inter',system-ui,sans-serif; background:#000; color:#a1a1aa; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:24px; }
.card { background:#09090b; border:1px solid #18181b; border-radius:20px; padding:48px; max-width:480px; width:100%; }
h1 { color:#fafafa; font-size:28px; font-weight:700; margin-bottom:8px; }
p { color:#71717a; font-size:14px; margin-bottom:32px; line-height:1.6; }
label { color:#a1a1aa; font-size:13px; font-weight:500; margin-bottom:6px; display:block; }
input { width:100%; padding:12px 16px; background:#000; border:1px solid #27272a; border-radius:10px; color:#fafafa; font-size:14px; outline:none; transition:border-color .2s; margin-bottom:16px; }
input:focus { border-color:#8b5cf6; }
input::placeholder { color:#3f3f46; }
button { width:100%; padding:14px; background:#8b5cf6; color:#fff; border:none; border-radius:10px; font-size:15px; font-weight:600; cursor:pointer; transition:background .2s; }
button:hover { background:#7c3aed; }
button:disabled { opacity:.5; cursor:not-allowed; }
.features { display:flex; gap:12px; margin-bottom:32px; flex-wrap:wrap; }
.feature { padding:6px 14px; border:1px solid #18181b; border-radius:8px; font-size:12px; color:#52525b; }
</style>
</head>
<body>
<div class="card">
  <div style="margin-bottom:24px;">
    <span style="color:#8b5cf6;font-weight:700;font-size:20px;">proxymic<span style="color:#52525b;">.</span></span>
  </div>
  <h1>Start Your 4-Day Trial</h1>
  <p>No credit card. No code changes. Just change your base URL and see the savings.</p>
  <div class="features">
    <span class="feature">No credit card</span>
    <span class="feature">1-line setup</span>
    <span class="feature">Zero risk</span>
  </div>
  <form id="signupForm" onsubmit="handleSignup(event)">
    <label>Full name</label>
    <input type="text" name="name" placeholder="Jane Cooper" required>
    <label>Work email</label>
    <input type="email" name="email" placeholder="jane@company.com" required>
    <label>Company</label>
    <input type="text" name="company" placeholder="Acme Inc">
    <button type="submit" id="submitBtn">Start 4-Day Trial →</button>
  </form>
  <div id="success" style="display:none;">
    <div style="padding:16px;background:#09090b;border:1px solid #27272a;border-radius:12px;margin-bottom:16px;">
      <p style="color:#34d399;font-size:14px;font-weight:600;margin-bottom:4px;">✅ Trial created!</p>
      <p style="color:#52525b;font-size:13px;">Your unique setup instructions are below.</p>
    </div>
    <div id="setupContent"></div>
  </div>
</div>
<script>
async function handleSignup(e) {
  e.preventDefault();
  const btn = document.getElementById('submitBtn');
  btn.disabled = true;
  btn.textContent = 'Creating...';
  const form = document.getElementById('signupForm');
  const data = new URLSearchParams(new FormData(form));
  try {
    const res = await fetch('/api/trial/signup', { method:'POST', body:data });
    const html = await res.text();
    form.style.display = 'none';
    document.getElementById('success').style.display = 'block';
    document.getElementById('setupContent').innerHTML = html;
  } catch(err) {
    alert('Failed to create trial. Please try again.');
    btn.disabled = false;
    btn.textContent = 'Start 4-Day Trial →';
  }
}
</script>
</body>
</html>`
}

func getTrialSetupHTML(token, email, name, baseURL string) string {
	return fmt.Sprintf(`<div style="margin-top:16px;">
  <div style="background:#09090b;border:1px solid #18181b;border-radius:12px;padding:20px;margin-bottom:16px;">
    <p style="color:#71717a;font-size:12px;text-transform:uppercase;letter-spacing:1px;margin-bottom:12px;">Your Trial Token</p>
    <div style="background:#000;border:1px solid #27272a;border-radius:8px;padding:12px 16px;font-family:monospace;font-size:13px;color:#34d399;word-break:break-all;">%s</div>
  </div>
  <div style="background:#09090b;border:1px solid #18181b;border-radius:12px;padding:20px;margin-bottom:16px;">
    <p style="color:#71717a;font-size:12px;text-transform:uppercase;letter-spacing:1px;margin-bottom:12px;">Setup Instructions</p>
    <div style="background:#000;border:1px solid #27272a;border-radius:8px;padding:12px 16px;font-family:monospace;font-size:13px;color:#a1a1aa;line-height:1.8;">
      <span style="color:#52525b;"># Change your base URL in code:</span><br>
      <span style="color:#8b5cf6;">base_url</span> = "<span style="color:#34d399;">%s/v1</span>"<br><br>
      <span style="color:#52525b;"># Add this HTTP header:</span><br>
      <span style="color:#8b5cf6;">X-Gateway-Token</span>: <span style="color:#34d399;">%s</span>
    </div>
  </div>
  <div style="background:#09090b;border:1px solid #18181b;border-radius:12px;padding:20px;margin-bottom:16px;">
    <p style="color:#71717a;font-size:12px;text-transform:uppercase;letter-spacing:1px;margin-bottom:12px;">Your Dashboard</p>
    <p style="color:#a1a1aa;font-size:14px;margin-bottom:12px;">Track your savings in real-time:</p>
    <a href="/trial-report?token=%s" style="display:inline-block;padding:12px 24px;background:#8b5cf6;color:#fff;border-radius:8px;text-decoration:none;font-size:14px;font-weight:600;">Open Dashboard →</a>
  </div>
  <p style="color:#52525b;font-size:12px;text-align:center;">Trial expires in 4 days. We'll send a reminder.</p>
</div>`, token, baseURL, token, token)
}

func getTrialExpiredHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Trial Expired — Proxymic</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700;800&display=swap" rel="stylesheet">
<style>
* { margin:0; padding:0; box-sizing:border-box; }
body { font-family:'Inter',system-ui,sans-serif; background:#000; color:#a1a1aa; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:24px; }
.card { background:#09090b; border:1px solid #18181b; border-radius:20px; padding:48px; max-width:480px; width:100%; text-align:center; }
h1 { color:#fafafa; font-size:28px; font-weight:700; margin-bottom:8px; }
p { color:#71717a; font-size:14px; margin-bottom:24px; line-height:1.6; }
.btn { display:inline-block; padding:14px 32px; background:#8b5cf6; color:#fff; border-radius:10px; text-decoration:none; font-size:15px; font-weight:600; }
.btn:hover { background:#7c3aed; }
</style>
</head>
<body>
<div class="card">
  <div style="font-size:48px;margin-bottom:16px;">⏰</div>
  <h1>Your Trial Has Ended</h1>
  <p>Your 4-day observer trial has expired. Your application traffic was monitored, but caching is now disabled.</p>
  <p style="color:#52525b;font-size:13px;">Upgrade to continue saving on your AI infrastructure costs.</p>
  <a class="btn" href="/pricing">View Pricing →</a>
  <p style="margin-top:16px;font-size:12px;color:#3f3f46;">Your application continued to work normally throughout the trial.</p>
</div>
</body>
</html>`
}

func getTrialDashboardHTML(token string) string {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Proxymic — Trial Dashboard</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
* { margin:0; padding:0; box-sizing:border-box; }
body { font-family:'Inter',system-ui,sans-serif; background:#000; color:#a1a1aa; min-height:100vh; padding:24px; }
.container { max-width:1000px; margin:0 auto; }
.header { text-align:center; padding:40px 20px; border-bottom:1px solid #18181b; margin-bottom:32px; }
.header h1 { color:#fafafa; font-size:36px; font-weight:700; margin-bottom:8px; }
.header p { color:#71717a; font-size:14px; }
.badge { display:inline-block; padding:4px 14px; border-radius:20px; font-size:12px; font-weight:600; margin-top:8px; }
.badge-active { background:rgba(52,211,153,.1); color:#34d399; border:1px solid rgba(52,211,153,.2); }
.badge-expired { background:rgba(239,68,68,.1); color:#ef4444; border:1px solid rgba(239,68,68,.2); }
.stats { display:grid; grid-template-columns:repeat(auto-fit,minmax(200px,1fr)); gap:12px; margin-bottom:24px; }
.stat-card { background:#09090b; border:1px solid #18181b; border-radius:12px; padding:20px; }
.stat-label { color:#52525b; font-size:11px; text-transform:uppercase; letter-spacing:1px; margin-bottom:6px; }
.stat-value { font-size:28px; font-weight:700; font-family:'JetBrains Mono',monospace; }
.stat-sub { color:#3f3f46; font-size:12px; margin-top:4px; }
.positive { color:#34d399; }
.neutral { color:#fafafa; }
.negative { color:#ef4444; }
.section { background:#09090b; border:1px solid #18181b; border-radius:16px; padding:24px; margin-bottom:16px; }
.section h2 { color:#fafafa; font-size:16px; font-weight:600; margin-bottom:16px; }
.row { display:flex; justify-content:space-between; align-items:center; padding:12px 0; border-bottom:1px solid #18181b; }
.row:last-child { border-bottom:none; }
.row-label { color:#71717a; font-size:14px; }
.row-value { color:#fafafa; font-size:16px; font-weight:600; font-family:'JetBrains Mono',monospace; }
.cta { text-align:center; padding:32px; background:linear-gradient(135deg,rgba(139,92,246,.1) 0%,transparent 100%); border:1px solid #18181b; border-radius:16px; margin-top:24px; }
.cta h2 { color:#fafafa; font-size:20px; font-weight:700; margin-bottom:8px; }
.cta p { color:#71717a; font-size:14px; margin-bottom:16px; }
.cta .btn { display:inline-block; padding:12px 28px; background:#8b5cf6; color:#fff; border-radius:8px; text-decoration:none; font-size:14px; font-weight:600; }
.cta .btn:hover { background:#7c3aed; }
.refresh { text-align:center; margin-bottom:16px; }
.refresh button { padding:8px 20px; background:transparent; border:1px solid #27272a; color:#71717a; border-radius:8px; cursor:pointer; font-size:13px; }
.refresh button:hover { border-color:#3f3f46; color:#a1a1aa; }
.chart { width:100%; height:200px; position:relative; margin-top:12px; }
.chart-bar { position:absolute; bottom:0; border-radius:3px 3px 0 0; transition:height .3s; }
.chart-bar-hit { background:#34d399; }
.chart-bar-miss { background:#ef4444; }
.footer { text-align:center; padding:24px; color:#3f3f46; font-size:12px; }
</style>
</head>
<body>
<div class="container">
  <div class="header">
    <div style="margin-bottom:12px;"><span style="color:#8b5cf6;font-weight:700;font-size:18px;">proxymic<span style="color:#52525b;">.</span></span></div>
    <h1>Trial Dashboard</h1>
    <p>Real-time cache savings analysis</p>
    <div style="margin-top:8px;padding:8px 16px;background:rgba(139,92,246,.1);border:1px solid rgba(139,92,246,.2);border-radius:8px;display:inline-block;">
      <span style="color:#71717a;font-size:11px;">Your Token: </span>
      <span style="color:#34d399;font-family:'JetBrains Mono',monospace;font-size:12px;word-break:break-all;">%s</span>
    </div>
  </div>

  <!-- Setup Instructions Section -->
  <div class="section" id="setupSection">
    <h2>🚀 Setup Instructions</h2>
    <p style="color:#71717a;font-size:13px;margin-bottom:16px;">Change your base URL and add this header to your app code:</p>
    <div style="background:#000;border:1px solid #27272a;border-radius:10px;padding:16px;font-family:'JetBrains Mono',monospace;font-size:13px;color:#a1a1aa;line-height:1.8;margin-bottom:12px;">
      <span style="color:#52525b;"># Change your base URL in code:</span><br>
      <span style="color:#8b5cf6;">base_url</span> = "<span style="color:#34d399;">https://ai-gateway-production-c86a.up.railway.app/v1</span>"<br><br>
      <span style="color:#52525b;"># Add this HTTP header:</span><br>
      <span style="color:#8b5cf6;">X-Gateway-Token</span>: <span style="color:#34d399;">%s</span>
    </div>
    <div style="display:flex;gap:8px;flex-wrap:wrap;">
      <button onclick="copyToken()" style="padding:8px 16px;background:transparent;border:1px solid #27272a;color:#a1a1aa;border-radius:8px;cursor:pointer;font-size:12px;">📋 Copy Token</button>
      <button onclick="copyCurl()" style="padding:8px 16px;background:transparent;border:1px solid #27272a;color:#a1a1aa;border-radius:8px;cursor:pointer;font-size:12px;">📋 Copy cURL Example</button>
    </div>
  </div>

  <div class="refresh"><button onclick="loadReport()">🔄 Refresh</button></div>

  <div class="stats">
    <div class="stat-card">
      <div class="stat-label">Total Requests</div>
      <div class="stat-value neutral" id="total-requests">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Would-Be Hits</div>
      <div class="stat-value positive" id="potential-hits">0</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Hit Rate</div>
      <div class="stat-value positive" id="hit-rate">0%</div>
    </div>      <div class="stat-card">
      <div class="stat-label">Monthly Projection</div>
      <div class="stat-value positive" id="cost-saved">$0.00</div>
      <div class="stat-sub">Based on your traffic patterns</div>
    </div>
  </div>

  <div class="section">
    <h2>Savings Projections</h2>
    <div class="row"><span class="row-label">Monthly Projection</span><span class="row-value positive" id="monthly">$0.00</span></div>
    <div class="row"><span class="row-label">Yearly Projection</span><span class="row-value positive" id="yearly">$0.00</span></div>
    <div class="row"><span class="row-label">Tokens Saved</span><span class="row-value neutral" id="tokens">0</span></div>
    <div class="row"><span class="row-label">Time Remaining</span><span class="row-value neutral" id="remaining">0h</span></div>
  </div>

  <div class="section">
    <h2>Traffic Overview (Last 60 min)</h2>
    <div class="chart" id="chart"><div style="text-align:center;padding:80px 0;color:#3f3f46;font-size:13px;">Waiting for traffic data...</div></div>
  </div>

  <div class="cta" id="cta">
    <h2>Ready to Save for Real?</h2>
    <p>Your trial shows real savings with your actual traffic patterns.</p>
    <a class="btn" href="/pricing">Upgrade to Full Caching →</a>
  </div>

  <div class="footer">Your data is private. We only track cache hit/miss metrics.</div>
</div>

<script>
// Read token, name, email from URL query parameters
const urlParams = new URLSearchParams(window.location.search);
const TOKEN = urlParams.get('token');
const USER_NAME = urlParams.get('name') || 'Customer';
const USER_EMAIL = urlParams.get('email') || '';
if (!TOKEN) {
  document.body.innerHTML = '<div style="padding:40px;text-align:center;color:#ef4444;">Missing token parameter</div>';
}

// Set token display in setup section immediately
(function() {
  const tokenDisplay = document.getElementById('tokenDisplay');
  if (tokenDisplay && TOKEN) tokenDisplay.textContent = TOKEN;
  const baseUrl = document.getElementById('baseUrl');
  if (baseUrl) baseUrl.textContent = window.location.origin + '/v1';
})();

// Copy token to clipboard
function copyToken() {
  if (!TOKEN) return;
  navigator.clipboard.writeText(TOKEN).then(() => {
    alert('Token copied to clipboard!');
  }).catch(() => {
    // Fallback
    const ta = document.createElement('textarea');
    ta.value = TOKEN;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    alert('Token copied!');
  });
}

// Copy cURL example to clipboard
function copyCurl() {
  if (!TOKEN) return;
  const curl = 'curl -X POST ' + window.location.origin + '/v1/chat/completions \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer $API_KEY" \\\n  -H "X-Gateway-Token: ' + TOKEN + '" \\\n  -d \'{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}\'';
  navigator.clipboard.writeText(curl).then(() => {
    alert('cURL example copied to clipboard!');
  }).catch(() => {
    const ta = document.createElement('textarea');
    ta.value = curl;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    alert('cURL example copied!');
  });
}

async function loadReport() {
  try {
    const r = await fetch('/api/trial/stats?token='+TOKEN);
    const d = await r.json();
    if(d.error) { document.body.innerHTML = '<div style="padding:40px;text-align:center;color:#ef4444;">'+d.error+'</div>'; return; }
    document.getElementById('total-requests').textContent = d.total_requests;
    document.getElementById('potential-hits').textContent = d.potential_hits;
    document.getElementById('hit-rate').textContent = d.hit_rate_percent.toFixed(1)+'%';
    document.getElementById('cost-saved').textContent = '$'+d.projected_monthly.toFixed(2);
    document.getElementById('monthly').textContent = '$'+d.projected_monthly.toFixed(2);
    document.getElementById('yearly').textContent = '$'+d.projected_yearly.toFixed(2);
    document.getElementById('tokens').textContent = d.tokens_saved.toLocaleString();
    document.getElementById('remaining').textContent = d.hours_remaining.toFixed(1)+'h / '+d.trial_duration_hours+'h';
    const badge = document.getElementById('badge');
    if(d.is_expired) { badge.textContent='Trial Expired'; badge.className='badge badge-expired'; }
    else { badge.textContent='Trial Active — '+d.hours_remaining.toFixed(1)+'h remaining'; badge.className='badge badge-active'; }
  } catch(e) { console.error(e); }
}
setInterval(loadReport,10000);
loadReport();
</script>
</body>
</html>`
	return strings.ReplaceAll(html, "%s", token)
}

func getAdminLoginHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Admin Login — Proxymic</title>
<script src="https://cdn.tailwindcss.com"></script>
<style>
* { margin:0; padding:0; box-sizing:border-box; }
body { font-family:'Inter',system-ui,sans-serif; background:#000; color:#a1a1aa; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:24px; }
.card { background:#09090b; border:1px solid #18181b; border-radius:20px; padding:48px; max-width:400px; width:100%; text-align:center; }
h1 { color:#fafafa; font-size:24px; font-weight:700; margin-bottom:8px; }
p { color:#71717a; font-size:14px; margin-bottom:24px; }
input { width:100%; padding:12px 16px; background:#000; border:1px solid #27272a; border-radius:10px; color:#fafafa; font-size:14px; margin-bottom:16px; outline:none; }
input:focus { border-color:#8b5cf6; }
button { padding:12px 32px; background:#8b5cf6; color:#fff; border:none; border-radius:8px; font-size:14px; font-weight:600; cursor:pointer; }
button:hover { background:#7c3aed; }
</style>
</head>
<body>
<div class="card">
  <h1>Admin Access</h1>
  <p>Enter your admin key to view trial data.</p>
  <form onsubmit="login(event)">
    <input type="password" id="key" placeholder="Admin API Key" required>
    <button type="submit">Access Dashboard</button>
  </form>
</div>
<script>
function login(e) {
  e.preventDefault();
  const key = document.getElementById('key').value;
  window.location.href = '/admin/trials?key='+encodeURIComponent(key);
}
</script>
</body>
</html>`
}

func getAdminHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Admin — Proxymic Trials</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
* { margin:0; padding:0; box-sizing:border-box; }
body { font-family:'Inter',system-ui,sans-serif; background:#000; color:#a1a1aa; min-height:100vh; padding:24px; }
.container { max-width:1100px; margin:0 auto; }
h1 { color:#fafafa; font-size:28px; font-weight:700; margin-bottom:4px; }
.sub { color:#71717a; font-size:14px; margin-bottom:24px; }
table { width:100%; border-collapse:collapse; background:#09090b; border:1px solid #18181b; border-radius:12px; overflow:hidden; }
th { text-align:left; padding:12px 16px; font-size:12px; color:#52525b; text-transform:uppercase; letter-spacing:1px; border-bottom:1px solid #18181b; background:#09090b; }
td { padding:12px 16px; font-size:13px; border-bottom:1px solid #09090b; color:#a1a1aa; }
tr:hover td { background:#0f0f0f; }
.mono { font-family:'JetBrains Mono',monospace; font-size:12px; }
.pos { color:#34d399; }
.neg { color:#ef4444; }
.neu { color:#fafafa; }
.empty { padding:60px; text-align:center; color:#3f3f46; }
</style>
</head>
<body>
<div class="container">
  <h1>Proxymic Trials</h1>
  <p class="sub">Active trial customers</p>
  <div id="content"><div class="empty">Loading...</div></div>
</div>
<script>
async function loadTrials() {
  try {
    const key = new URLSearchParams(window.location.search).get('key') || '';
    const r = await fetch('/api/admin/trials?key='+encodeURIComponent(key));
    if(r.status === 401) { window.location.href='/admin/trials'; return; }
    const d = await r.json();
    if(!d.trials || d.trials.length === 0) {
      document.getElementById('content').innerHTML = '<div class="empty">No active trials yet.</div>';
      return;
    }
    let html = '<table><tr><th>Name</th><th>Email</th><th>Company</th><th>Requests</th><th>Hits</th><th>Hit Rate</th><th>Savings</th><th>Remaining</th><th>Dashboard</th></tr>';
    d.trials.forEach(t => {
      const hr = t.total_requests > 0 ? ((t.potential_hits/t.total_requests)*100).toFixed(1)+'%' : '0%';
      html += '<tr><td class="neu">'+t.name+'</td><td>'+t.email+'</td><td>'+t.company+'</td><td class="mono">'+t.total_requests+'</td><td class="mono pos">'+t.potential_hits+'</td><td class="mono pos">'+hr+'</td><td class="mono pos">$'+t.cost_saved_usd.toFixed(2)+'</td><td class="mono">'+t.hours_remaining.toFixed(1)+'h</td><td><a href="/trial-report?token='+t.token+'" style="color:#8b5cf6;text-decoration:none;font-size:12px;">View →</a></td></tr>';
    });
    html += '</table><p style="color:#3f3f46;font-size:12px;margin-top:12px;">Total trials: '+d.total_trials+'</p>';
    document.getElementById('content').innerHTML = html;
  } catch(e) { document.getElementById('content').innerHTML = '<div class="empty" style="color:#ef4444;">Failed to load</div>'; }
}
loadTrials();
setInterval(loadTrials, 30000);
</script>
</body>
</html>`
}

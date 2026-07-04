package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Agentic Loop Killer - Multi-Stage Detection & Mitigation
//
// How it works:
//   1. Track: All requests per tenant are logged with timestamp + prompt hash
//   2. Score: Each request is scored based on frequency + content similarity
//   3. Escalate: Based on score, the system escalates through stages:
//      - Stage 0 (OK):       score < 3, no action
//      - Stage 1 (Monitor):  score 3-4, add X-Gateway-Loop header (warn)
//      - Stage 2 (Throttle): score 5-7, add artificial delay 500-2000ms
//      - Stage 3 (Block):    score 8+, return 429 with clear error
//   4. Decay: Scores decay naturally over time (window slides)
//
// An "agent loop" is defined as: rapid-fire identical/similar prompts
// from the same tenant, characteristic of buggy agentic systems that
// re-prompt with the same input in an infinite loop.
// ============================================================================

type LoopStage int

const (
	LoopStageOK       LoopStage = 0
	LoopStageMonitor  LoopStage = 1
	LoopStageThrottle LoopStage = 2
	LoopStageBlock    LoopStage = 3
)

func (s LoopStage) String() string {
	switch s {
	case LoopStageOK:
		return "ok"
	case LoopStageMonitor:
		return "monitor"
	case LoopStageThrottle:
		return "throttle"
	case LoopStageBlock:
		return "block"
	default:
		return "unknown"
	}
}

// LoopAction is returned by the loop killer after evaluating a request
type LoopAction struct {
	Stage       LoopStage
	Score       float64
	Delay       time.Duration // Artificial delay to add (stages 2+)
	Blocked     bool          // Request should be blocked (stage 3)
	Message     string        // Human-readable message
	RequestCount int          // Requests in current window
	SameCount   int           // Count of identical/similar prompts
}

// loopEntry tracks a single request in the sliding window
type loopEntry struct {
	timestamp  time.Time
	promptHash string
	prompt     string // truncated for similarity comparison
}

// tenantLoopState tracks all state for one tenant
type tenantLoopState struct {
	entries    []loopEntry
	lastStage  LoopStage
	blockUntil time.Time // If blocked, don't unblock until this time
	mu         sync.Mutex
}

// LoopKiller is the main loop detection engine
type LoopKiller struct {
	tenants map[string]*tenantLoopState
	mu      sync.RWMutex

	window        time.Duration // Sliding window duration (default 30s)
	maxEntries    int           // Max entries to keep per tenant
	blockDuration time.Duration // How long to block when stage 3 is hit
	cooldownAfter time.Duration // After cooldown, reset stage to 0

	// Scoring weights
	freqWeight     float64 // Weight for frequency component
	similarWeight  float64 // Weight for similarity component
	stage2Delay    time.Duration // Delay added at stage 2 (throttle)
	stage3Lockout  time.Duration // Lockout duration at stage 3
}

func NewLoopKiller(cfg LoopKillerConfig) *LoopKiller {
	lk := &LoopKiller{
		tenants:       make(map[string]*tenantLoopState),
		window:        time.Duration(cfg.WindowSeconds) * time.Second,
		maxEntries:    cfg.MaxEntries,
		blockDuration: time.Duration(cfg.BlockSeconds) * time.Second,
		cooldownAfter: time.Duration(cfg.CooldownSeconds) * time.Second,
		freqWeight:    cfg.FrequencyWeight,
		similarWeight: cfg.SimilarityWeight,
		stage2Delay:   time.Duration(cfg.Stage2DelayMS) * time.Millisecond,
		stage3Lockout: time.Duration(cfg.Stage3LockoutMS) * time.Millisecond,
	}

	if lk.window <= 0 {
		lk.window = 30 * time.Second
	}
	if lk.maxEntries <= 0 {
		lk.maxEntries = 100
	}
	if lk.blockDuration <= 0 {
		lk.blockDuration = 60 * time.Second
	}
	if lk.cooldownAfter <= 0 {
		lk.cooldownAfter = 120 * time.Second
	}
	if lk.freqWeight <= 0 {
		lk.freqWeight = 1.0
	}
	if lk.similarWeight <= 0 {
		lk.similarWeight = 2.0
	}
	if lk.stage2Delay <= 0 {
		lk.stage2Delay = 500 * time.Millisecond
	}
	if lk.stage3Lockout <= 0 {
		lk.stage3Lockout = 30 * time.Second
	}

	// Background cleanup goroutine
	go lk.cleanupLoop()

	return lk
}

// Evaluate checks a request against the loop killer and returns the action to take.
// This is the main entry point - call it before processing each request.
func (lk *LoopKiller) Evaluate(tenantID, prompt string) LoopAction {
	// Clean up old entries first
	lk.mu.RLock()
	state, exists := lk.tenants[tenantID]
	lk.mu.RUnlock()

	if !exists {
		state = &tenantLoopState{
			entries: make([]loopEntry, 0, 64),
		}
		lk.mu.Lock()
		lk.tenants[tenantID] = state
		lk.mu.Unlock()
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	now := time.Now()

	// If currently blocked, check if block has expired
	if !state.blockUntil.IsZero() && now.Before(state.blockUntil) {
		return LoopAction{
			Stage:        LoopStageBlock,
			Score:        10.0,
			Blocked:      true,
			Message:      fmt.Sprintf("agent loop detected: blocked until %s (retry after %ds)", state.blockUntil.Format("15:04:05"), int(state.blockUntil.Sub(now).Seconds())),
			RequestCount: len(state.entries),
		}
	}
	if !state.blockUntil.IsZero() && now.After(state.blockUntil) {
		state.blockUntil = time.Time{}
		state.lastStage = LoopStageOK
		state.entries = nil
	}

	// Prune old entries outside the window
	cutoff := now.Add(-lk.window)
	valid := make([]loopEntry, 0, len(state.entries))
	for _, e := range state.entries {
		if e.timestamp.After(cutoff) {
			valid = append(valid, e)
		}
	}
	state.entries = valid

	// Hash the prompt for comparison
	h := sha256.Sum256([]byte(prompt))
	promptHash := hex.EncodeToString(h[:])

	// Count how many of the recent entries have the same or very similar prompt
	sameCount := 0
	totalRecent := len(state.entries)
	for _, e := range state.entries {
		if e.promptHash == promptHash {
			sameCount++
		} else {
			// Check for similar prompts using word overlap
			if loopWordOverlap(prompt, e.prompt) >= 0.65 {
				sameCount++
			}
		}
	}

	// Calculate frequency score: how many requests vs. expected baseline
	// Baseline: 1 request per 5 seconds = 6 requests in a 30s window
	baseline := float64(lk.window.Seconds()) / 5.0
	if baseline < 1 {
		baseline = 1
	}
	freqScore := float64(totalRecent) / baseline

	// Calculate similarity score: proportion of requests that are identical/similar
	similarScore := 0.0
	if totalRecent > 0 {
		similarScore = float64(sameCount) / float64(totalRecent)
	}

	// Combined score: frequency * similarity weight
	// If freq is low but similarity is high -> moderate score (few similar requests)
	// If freq is high but similarity is low -> moderate score (many different requests)
	// If freq is high AND similarity is high -> high score (real loop!)
	combinedScore := (lk.freqWeight * freqScore) + (lk.similarWeight * similarScore * freqScore)

	// Add the current entry
	state.entries = append(state.entries, loopEntry{
		timestamp:  now,
		promptHash: promptHash,
		prompt:     truncatePrompt(prompt, 200),
	})

	// Cap entries
	if len(state.entries) > lk.maxEntries {
		state.entries = state.entries[len(state.entries)-lk.maxEntries:]
	}

	// Determine stage based on score
	action := LoopAction{
		Score:        math.Round(combinedScore*100) / 100,
		RequestCount: totalRecent + 1,
		SameCount:    sameCount,
	}

	switch {
	case combinedScore >= 8.0:
		action.Stage = LoopStageBlock
		action.Blocked = true
		action.Delay = 0
		action.Message = fmt.Sprintf("agent loop blocked: %.1f identical/similar requests in %.0fs (score: %.1f)", float64(sameCount), lk.window.Seconds(), combinedScore)
		state.blockUntil = now.Add(lk.blockDuration)
		log.Printf("🚫 LOOP BLOCKED: tenant=%s, score=%.1f, requests=%d, same=%d, block=%s",
			tenantID, combinedScore, totalRecent+1, sameCount, lk.blockDuration)

	case combinedScore >= 5.0:
		action.Stage = LoopStageThrottle
		action.Blocked = false
		// Escalating delay: 500ms base * (score - 4)
		delayMS := lk.stage2Delay.Milliseconds()
		escalatedDelay := time.Duration(delayMS*int64(combinedScore-4)) * time.Millisecond
		if escalatedDelay > lk.stage3Lockout {
			escalatedDelay = lk.stage3Lockout
		}
		action.Delay = escalatedDelay
		action.Message = fmt.Sprintf("agent loop throttle: slowing requests (score: %.1f, delay: %v)", combinedScore, escalatedDelay)
		log.Printf("⚠️ LOOP THROTTLE: tenant=%s, score=%.1f, delay=%v", tenantID, combinedScore, escalatedDelay)

	case combinedScore >= 3.0:
		action.Stage = LoopStageMonitor
		action.Blocked = false
		action.Delay = 0
		action.Message = fmt.Sprintf("agent loop monitor: suspicious pattern detected (score: %.1f)", combinedScore)
		if state.lastStage < LoopStageMonitor {
			log.Printf("👀 LOOP MONITOR: tenant=%s, score=%.1f, requests=%d, same=%d", tenantID, combinedScore, totalRecent+1, sameCount)
		}

	default:
		action.Stage = LoopStageOK
		action.Blocked = false
		action.Delay = 0
		action.Message = "ok"
	}

	state.lastStage = action.Stage

	return action
}

// RecordCleanup allows the loop killer to forget a tenant's state
// Used when a tenant completes a valid session
func (lk *LoopKiller) RecordCleanup(tenantID string) {
	lk.mu.Lock()
	delete(lk.tenants, tenantID)
	lk.mu.Unlock()
}

// GetStats returns loop killer stats for observability
func (lk *LoopKiller) GetStats() map[string]interface{} {
	lk.mu.RLock()
	defer lk.mu.RUnlock()

	totalTenants := len(lk.tenants)
	blockedCount := 0
	throttledCount := 0
	monitoredCount := 0

	for _, state := range lk.tenants {
		state.mu.Lock()
		switch state.lastStage {
		case LoopStageBlock:
			blockedCount++
		case LoopStageThrottle:
			throttledCount++
		case LoopStageMonitor:
			monitoredCount++
		}
		state.mu.Unlock()
	}

	return map[string]interface{}{
		"active_tenants": totalTenants,
		"blocked":       blockedCount,
		"throttled":     throttledCount,
		"monitored":     monitoredCount,
		"window_sec":    lk.window.Seconds(),
	}
}

// cleanupLoop periodically removes stale tenant state
func (lk *LoopKiller) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		lk.mu.Lock()
		now := time.Now()
		for id, state := range lk.tenants {
			state.mu.Lock()
			// Remove tenants with no recent activity
			if len(state.entries) > 0 {
				lastActivity := state.entries[len(state.entries)-1].timestamp
				if now.Sub(lastActivity) > lk.cooldownAfter {
					delete(lk.tenants, id)
				}
			} else {
				delete(lk.tenants, id)
			}
			state.mu.Unlock()
		}
		lk.mu.Unlock()
	}
}

// loopWordOverlap calculates the fraction of words shared between two prompts
// Uses a simpler implementation than fuzzy_cache.go's wordOverlapScore to avoid conflicts
func loopWordOverlap(a, b string) float64 {
	wordsA := strings.Fields(strings.ToLower(a))
	wordsB := strings.Fields(strings.ToLower(b))

	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0
	}

	setA := make(map[string]int, len(wordsA))
	for _, w := range wordsA {
		setA[w]++
	}

	intersection := 0
	for _, w := range wordsB {
		if setA[w] > 0 {
			intersection++
			setA[w]--
		}
	}

	union := len(wordsA) + len(wordsB) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

func truncatePrompt(prompt string, maxLen int) string {
	if len(prompt) <= maxLen {
		return prompt
	}
	return prompt[:maxLen]
}

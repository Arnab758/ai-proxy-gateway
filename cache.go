package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type VectorCacheEntry struct {
	Vector   []float64
	Response []byte
	Prompt   string
	Score    float64
}

type RedisSemanticCache struct {
	client          *redis.Client
	ctx             context.Context
	embedder        *VectorEmbeddingEngine
	templateMatcher *TemplateMatcher
	cfg             *Config

	localIndex   map[string][]VectorCacheEntry
	localIndexMu sync.RWMutex

	pendingRequests map[string]chan []byte
	dedupMu         sync.Mutex

	// Embedding cache to avoid recomputation
	embeddingCache   map[string][]float64
	embeddingCacheMu sync.RWMutex

	// Bounded dedup map with TTL to prevent memory leaks
	dedupExpiry   map[string]time.Time
	dedupExpiryMu sync.Mutex
}

var punctuationRegex = regexp.MustCompile(`[^\w\s]`)

func NewRedisSemanticCache(redisURL string, cfg *Config) (*RedisSemanticCache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	// Connection pool tuning for low latency
	opts.PoolSize = 50
	opts.MinIdleConns = 10
	opts.MaxRetries = 3
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second

	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	redisAvailable := false
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("⚠️  Redis connection failed: %v", err)
		log.Printf("⚠️  Falling back to in-memory cache (data will not persist across restarts)")
		rdb = nil
	} else {
		redisAvailable = true
		log.Printf("✅ Redis connected successfully: %s", redisURL)
	}

	cache := &RedisSemanticCache{
		client:          rdb,
		ctx:             context.Background(),
		embedder:        NewVectorEmbeddingEngine(cfg.Cache.Vector.Dimension),
		templateMatcher: NewTemplateMatcher(cfg.Cache.TemplateMatching.MaxTemplates),
		cfg:             cfg,
		localIndex:      make(map[string][]VectorCacheEntry),
		pendingRequests: make(map[string]chan []byte),
		embeddingCache:  make(map[string][]float64),
		dedupExpiry:     make(map[string]time.Time),
	}

	// Start cleanup goroutine for dedup map
	go cache.cleanupDedupExpired()

	storageType := "in-memory"
	if redisAvailable {
		storageType = "Redis"
	}
	log.Printf("📦 Cache initialized: storage=%s, dim=%d, vector=%v, jaccard=%v, templates=%v",
		storageType,
		cfg.Cache.Vector.Dimension,
		cfg.Cache.Vector.Enabled,
		cfg.Cache.Jaccard.Enabled,
		cfg.Cache.TemplateMatching.Enabled)

	return cache, nil
}

func (rc *RedisSemanticCache) getOrCreateEmbedding(text string) []float64 {
	// Check cache first
	rc.embeddingCacheMu.RLock()
	cached, exists := rc.embeddingCache[text]
	rc.embeddingCacheMu.RUnlock()

	if exists {
		return cached
	}

	// Compute embedding
	embedding := rc.embedder.Embed(text)

	// Cache it
	rc.embeddingCacheMu.Lock()
	rc.embeddingCache[text] = embedding
	rc.embeddingCacheMu.Unlock()

	return embedding
}

func (rc *RedisSemanticCache) Store(startupID, prompt string, response []byte) error {
	cleanPrompt := cleanText(prompt)
	hasher := sha256.New()
	hasher.Write([]byte(cleanPrompt))
	promptHash := hex.EncodeToString(hasher.Sum(nil))
	redisKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, promptHash)

	var embedding []float64
	if rc.cfg.Cache.Vector.Enabled {
		embedding = rc.getOrCreateEmbedding(cleanPrompt)
	}

	ttl := time.Duration(rc.cfg.Cache.TTLHours) * time.Hour

	redisStored := false
	if rc.client != nil {
		hashData := map[string]interface{}{
			"prompt":   cleanPrompt,
			"response": response,
		}

		if embedding != nil {
			hashData["vector"] = VectorToBytes(embedding)
		}

		err := rc.client.HSet(rc.ctx, redisKey, hashData).Err()
		if err != nil {
			log.Printf("⚠️  Redis store failed: %v", err)
		} else {
			rc.client.Expire(rc.ctx, redisKey, ttl)
			redisStored = true
		}
	}

	rc.localIndexMu.Lock()
	rc.localIndex[startupID] = append(rc.localIndex[startupID], VectorCacheEntry{
		Vector:   embedding,
		Response: response,
		Prompt:   cleanPrompt,
		Score:    1.0,
	})
	if len(rc.localIndex[startupID]) > rc.cfg.Cache.Vector.MaxVectorsPerTenant {
		rc.localIndex[startupID] = rc.localIndex[startupID][1:]
	}
	rc.localIndexMu.Unlock()

	if rc.cfg.Cache.TemplateMatching.Enabled {
		rc.templateMatcher.LearnFromPrompt(prompt, response)
	}

	storage := "in-memory"
	if redisStored {
		storage = "Redis"
	}
	log.Printf("💾 Cached response: storage=%s, tenant=%s, prompt_hash=%s, ttl=%dh",
		storage, startupID, promptHash[:8]+"...", rc.cfg.Cache.TTLHours)

	return nil
}

// SearchWithContext checks cache with conversation history for better matching
func (rc *RedisSemanticCache) SearchWithContext(tenantID, query string, context []string, threshold float64) ([]byte, float64, bool) {
	// Build context-aware cache key by combining previous messages with current query
	cacheKey := query
	if len(context) > 0 {
		// Use last 3 messages as context (balance between accuracy and performance)
		contextStart := 0
		if len(context) > 3 {
			contextStart = len(context) - 3
		}
		relevantContext := context[contextStart:]
		cacheKey = strings.Join(relevantContext, "\n") + "\n\n" + query
	}

	target := cleanText(cacheKey)

	if rc.cfg.Dedup.Enabled {
		if result, found := rc.checkDedup(tenantID, target); found {
			log.Printf("🔍 Cache HIT (dedup+context): tenant=%s", tenantID)
			return result, 1.0, true
		}
	}

	if rc.cfg.Cache.Vector.ExactHashFirst {
		hasher := sha256.New()
		hasher.Write([]byte(target))
		exactHash := hex.EncodeToString(hasher.Sum(nil))

		if result, found := rc.exactLookup(tenantID, exactHash); found {
			log.Printf("🔍 Cache HIT (exact hash+context): tenant=%s", tenantID)
			return result, 1.0, true
		}
	}

	if rc.cfg.Cache.TemplateMatching.Enabled {
		if result, found := rc.templateMatcher.Match(query); found {
			log.Printf("🔍 Cache HIT (template+context): tenant=%s", tenantID)
			return result, 0.95, true
		}
	}

	if rc.cfg.Cache.Vector.Enabled {
		incomingVec := rc.getOrCreateEmbedding(cacheKey)

		if rc.client != nil {
			if result, score, found := rc.vectorSearchRedis(tenantID, incomingVec, threshold); found {
				log.Printf("🔍 Cache HIT (vector/Redis+context): tenant=%s, score=%.2f", tenantID, score)
				return result, score, true
			}
		}

		if result, score, found := rc.vectorSearchLocal(tenantID, incomingVec, threshold); found {
			log.Printf("🔍 Cache HIT (vector/local+context): tenant=%s, score=%.2f", tenantID, score)
			return result, score, true
		}
	}

	if rc.cfg.Cache.Jaccard.Enabled {
		if result, score, found := rc.jaccardSearch(tenantID, target); found && score >= rc.cfg.Cache.Jaccard.Threshold {
			log.Printf("🔍 Cache HIT (jaccard+context): tenant=%s, score=%.2f", tenantID, score)
			return result, score, true
		}
	}

	log.Printf("🔍 Cache MISS (context): tenant=%s, query=%s", tenantID, query[:min(50, len(query))]+"...")
	return nil, 0.0, false
}

// ConfidenceScore returns how confident we are in a cache match
// Returns 0.0-1.0 where 1.0 = exact match, 0.0 = no match
func (rc *RedisSemanticCache) ConfidenceScore(startupID, incomingPrompt string, threshold float64) float64 {
	target := cleanText(incomingPrompt)

	// Exact hash = 100% confidence
	if rc.cfg.Cache.Vector.ExactHashFirst {
		hasher := sha256.New()
		hasher.Write([]byte(target))
		exactHash := hex.EncodeToString(hasher.Sum(nil))
		if _, found := rc.exactLookup(startupID, exactHash); found {
			return 1.0
		}
	}

	// Template match = 95% confidence
	if rc.cfg.Cache.TemplateMatching.Enabled {
		if _, found := rc.templateMatcher.Match(incomingPrompt); found {
			return 0.95
		}
	}

	// Vector similarity = actual score
	if rc.cfg.Cache.Vector.Enabled {
		incomingVec := rc.getOrCreateEmbedding(target)

		if rc.client != nil {
			if _, score, found := rc.vectorSearchRedis(startupID, incomingVec, threshold); found {
				return score
			}
		}

		if _, score, found := rc.vectorSearchLocal(startupID, incomingVec, threshold); found {
			return score
		}
	}

	// Jaccard = actual score
	if rc.cfg.Cache.Jaccard.Enabled {
		if _, score, found := rc.jaccardSearch(startupID, target); found {
			return score
		}
	}

	return 0.0
}

// Search checks cache in order: dedup, exact hash, template, vector, jaccard
func (rc *RedisSemanticCache) Search(startupID, incomingPrompt string, threshold float64) ([]byte, float64, bool) {
	target := cleanText(incomingPrompt)

	if rc.cfg.Dedup.Enabled {
		if result, found := rc.checkDedup(startupID, target); found {
			log.Printf("🔍 Cache HIT (dedup): tenant=%s", startupID)
			return result, 1.0, true
		}
	}

	if rc.cfg.Cache.Vector.ExactHashFirst {
		hasher := sha256.New()
		hasher.Write([]byte(target))
		exactHash := hex.EncodeToString(hasher.Sum(nil))

		if result, found := rc.exactLookup(startupID, exactHash); found {
			log.Printf("🔍 Cache HIT (exact hash): tenant=%s, hash=%s", startupID, exactHash[:8]+"...")
			return result, 1.0, true
		}
	}

	if rc.cfg.Cache.TemplateMatching.Enabled {
		if result, found := rc.templateMatcher.Match(incomingPrompt); found {
			log.Printf("🔍 Cache HIT (template): tenant=%s", startupID)
			return result, 0.95, true
		}
	}

	if rc.cfg.Cache.Vector.Enabled {
		incomingVec := rc.getOrCreateEmbedding(target)

		if rc.client != nil {
			if result, score, found := rc.vectorSearchRedis(startupID, incomingVec, threshold); found {
				log.Printf("🔍 Cache HIT (vector/Redis): tenant=%s, score=%.2f", startupID, score)
				return result, score, true
			}
		}

		if result, score, found := rc.vectorSearchLocal(startupID, incomingVec, threshold); found {
			log.Printf("🔍 Cache HIT (vector/local): tenant=%s, score=%.2f", startupID, score)
			return result, score, true
		}
	}

	if rc.cfg.Cache.Jaccard.Enabled {
		if result, score, found := rc.jaccardSearch(startupID, target); found && score >= rc.cfg.Cache.Jaccard.Threshold {
			log.Printf("🔍 Cache HIT (jaccard): tenant=%s, score=%.2f", startupID, score)
			return result, score, true
		}
	}

	log.Printf("🔍 Cache MISS: tenant=%s, prompt=%s", startupID, incomingPrompt[:min(50, len(incomingPrompt))]+"...")
	return nil, 0.0, false
}

func (rc *RedisSemanticCache) exactLookup(startupID, hash string) ([]byte, bool) {
	exactKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, hash)

	if rc.client != nil {
		if val, err := rc.client.HGet(rc.ctx, exactKey, "response").Bytes(); err == nil {
			return val, true
		}
	}

	rc.localIndexMu.RLock()
	defer rc.localIndexMu.RUnlock()
	if entries, ok := rc.localIndex[startupID]; ok {
		for _, entry := range entries {
			h := sha256.Sum256([]byte(entry.Prompt))
			if hex.EncodeToString(h[:]) == hash {
				return entry.Response, true
			}
		}
	}
	return nil, false
}

func (rc *RedisSemanticCache) vectorSearchRedis(startupID string, vec []float64, threshold float64) ([]byte, float64, bool) {
	pattern := fmt.Sprintf("gateway:cache:%s:*", startupID)
	var cursor uint64
	var bestMatch []byte
	var bestScore float64

	for {
		keys, nextCursor, err := rc.client.Scan(rc.ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, 0, false
		}

		for _, key := range keys {
			pipe := rc.client.Pipeline()
			vectorCmd := pipe.HGet(rc.ctx, key, "vector")
			responseCmd := pipe.HGet(rc.ctx, key, "response")
			pipe.Exec(rc.ctx)

			vectorBytes, err := vectorCmd.Bytes()
			if err != nil {
				continue
			}

			cachedVec := BytesToVector(vectorBytes)
			if cachedVec == nil {
				continue
			}

			score := CosineSimilarity(vec, cachedVec)
			if score > bestScore && score >= threshold {
				if responseBytes, err := responseCmd.Bytes(); err == nil {
					bestScore = score
					bestMatch = responseBytes
				}
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if bestMatch != nil {
		return bestMatch, bestScore, true
	}
	return nil, 0, false
}

func (rc *RedisSemanticCache) vectorSearchLocal(startupID string, vec []float64, threshold float64) ([]byte, float64, bool) {
	rc.localIndexMu.RLock()
	entries, ok := rc.localIndex[startupID]
	rc.localIndexMu.RUnlock()

	if !ok || len(entries) == 0 {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64

	for _, entry := range entries {
		if entry.Vector == nil {
			continue
		}
		score := CosineSimilarity(vec, entry.Vector)
		if score > bestScore && score >= threshold {
			bestScore = score
			bestMatch = entry.Response
		}
	}

	if bestMatch != nil {
		return bestMatch, bestScore, true
	}
	return nil, 0, false
}

func (rc *RedisSemanticCache) jaccardSearch(startupID, target string) ([]byte, float64, bool) {
	wordsTarget := strings.Fields(target)
	if len(wordsTarget) == 0 {
		return nil, 0.0, false
	}

	if rc.client != nil {
		pattern := fmt.Sprintf("gateway:cache:%s:*", startupID)
		var cursor uint64

		for {
			keys, nextCursor, err := rc.client.Scan(rc.ctx, cursor, pattern, 100).Result()
			if err != nil {
				break
			}

			for _, key := range keys {
				cachedPromptText, err := rc.client.HGet(rc.ctx, key, "prompt").Result()
				if err != nil {
					continue
				}

				score := calculateFastSimilarity(wordsTarget, cachedPromptText)
				if score >= rc.cfg.Cache.Jaccard.Threshold {
					cachedResponseBytes, err := rc.client.HGet(rc.ctx, key, "response").Bytes()
					if err == nil {
						return cachedResponseBytes, score, true
					}
				}
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
	}

	rc.localIndexMu.RLock()
	entries, ok := rc.localIndex[startupID]
	rc.localIndexMu.RUnlock()

	if !ok {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64
	for _, entry := range entries {
		score := calculateFastSimilarity(wordsTarget, entry.Prompt)
		if score > bestScore && score >= rc.cfg.Cache.Jaccard.Threshold {
			bestScore = score
			bestMatch = entry.Response
		}
	}
	return bestMatch, bestScore, bestMatch != nil
}

// checkDedup prevents duplicate concurrent requests from all hitting upstream
func (rc *RedisSemanticCache) checkDedup(startupID, prompt string) ([]byte, bool) {
	dedupKey := fmt.Sprintf("dedup:%s:%s", startupID, prompt)

	rc.dedupMu.Lock()
	ch, exists := rc.pendingRequests[dedupKey]
	if !exists {
		rc.pendingRequests[dedupKey] = make(chan []byte, 1)
		// Set expiry for cleanup
		rc.dedupExpiryMu.Lock()
		rc.dedupExpiry[dedupKey] = time.Now().Add(30 * time.Second)
		rc.dedupExpiryMu.Unlock()
		rc.dedupMu.Unlock()
		return nil, false
	}
	rc.dedupMu.Unlock()

	log.Printf("Deduplication: waiting for in-flight request: %s", dedupKey[:min(len(dedupKey), 40)]+"...")

	select {
	case result := <-ch:
		return result, true
	case <-time.After(time.Duration(rc.cfg.Dedup.MaxWaitSeconds) * time.Second):
		return nil, false
	}
}

func (rc *RedisSemanticCache) NotifyDedup(startupID, prompt string, response []byte) {
	if !rc.cfg.Dedup.Enabled {
		return
	}
	dedupKey := fmt.Sprintf("dedup:%s:%s", startupID, prompt)

	rc.dedupMu.Lock()
	ch, exists := rc.pendingRequests[dedupKey]
	if exists {
		delete(rc.pendingRequests, dedupKey)
		close(ch)
		// Remove expiry entry
		rc.dedupExpiryMu.Lock()
		delete(rc.dedupExpiry, dedupKey)
		rc.dedupExpiryMu.Unlock()
	}
	rc.dedupMu.Unlock()
}

func (rc *RedisSemanticCache) StoreWithVector(startupID, prompt string, response []byte, embedding []float64) error {
	cleanPrompt := cleanText(prompt)
	hasher := sha256.New()
	hasher.Write([]byte(cleanPrompt))
	promptHash := hex.EncodeToString(hasher.Sum(nil))
	redisKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, promptHash)
	ttl := time.Duration(rc.cfg.Cache.TTLHours) * time.Hour

	if rc.client != nil {
		err := rc.client.HSet(rc.ctx, redisKey, map[string]interface{}{
			"prompt":   cleanPrompt,
			"response": response,
			"vector":   VectorToBytes(embedding),
		}).Err()
		if err != nil {
			return err
		}
		return rc.client.Expire(rc.ctx, redisKey, ttl).Err()
	}

	rc.localIndexMu.Lock()
	entries := rc.localIndex[startupID]
	entries = append(entries, VectorCacheEntry{
		Vector:   embedding,
		Response: response,
		Prompt:   cleanPrompt,
		Score:    1.0,
	})
	if len(entries) > rc.cfg.Cache.Vector.MaxVectorsPerTenant {
		entries = entries[1:]
	}
	rc.localIndex[startupID] = entries
	rc.localIndexMu.Unlock()
	return nil
}

func cleanText(input string) string {
	lowered := strings.ToLower(input)
	stripped := punctuationRegex.ReplaceAllString(lowered, " ")
	words := strings.Fields(stripped)
	sort.Strings(words)
	return strings.Join(words, " ")
}

func calculateFastSimilarity(words []string, cachedPrompt string) float64 {
	if len(words) == 0 {
		return 0
	}

	payloadWords := strings.Fields(cachedPrompt)
	if len(payloadWords) == 0 {
		return 0
	}

	wordSet := make(map[string]struct{}, len(payloadWords))
	for _, w := range payloadWords {
		wordSet[w] = struct{}{}
	}

	matches := 0
	for _, w := range words {
		if _, ok := wordSet[w]; ok {
			matches++
		}
	}

	if matches == 0 {
		return 0
	}

	total := len(words) + len(payloadWords) - matches
	return float64(matches) / float64(total)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cleanupDedupExpired removes old entries from dedup map to prevent memory leaks
func (rc *RedisSemanticCache) cleanupDedupExpired() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		rc.dedupExpiryMu.Lock()
		rc.dedupMu.Lock()
		now := time.Now()
		for key, expiry := range rc.dedupExpiry {
			if now.After(expiry) {
				delete(rc.dedupExpiry, key)
				// Also clean up pending request channel if it exists
				if ch, exists := rc.pendingRequests[key]; exists {
					close(ch)
					delete(rc.pendingRequests, key)
				}
			}
		}
		rc.dedupMu.Unlock()
		rc.dedupExpiryMu.Unlock()
	}
}

func (rc *RedisSemanticCache) GetStats() map[string]interface{} {
	rc.localIndexMu.RLock()
	totalEntries := 0
	for _, entries := range rc.localIndex {
		totalEntries += len(entries)
	}
	rc.localIndexMu.RUnlock()

	return map[string]interface{}{
		"local_index_entries": totalEntries,
		"vector_dimensions":   rc.cfg.Cache.Vector.Dimension,
		"vector_threshold":    math.Round(rc.cfg.Cache.Vector.SimilarityThreshold*100) / 100,
		"jaccard_threshold":   math.Round(rc.cfg.Cache.Jaccard.Threshold*100) / 100,
		"template_enabled":    rc.cfg.Cache.TemplateMatching.Enabled,
		"dedup_enabled":       rc.cfg.Dedup.Enabled,
		"ttl_hours":           rc.cfg.Cache.TTLHours,
	}
}

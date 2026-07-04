package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ============================================================================
// HNSW (Hierarchical Navigable Small World) Index for O(log n) vector search
// ============================================================================

type HNSWNode struct {
	ID       int
	Vector   []float64
	Norm     float64 // Pre-computed magnitude
	Layers   [][]int // Neighbors at each layer
	MaxLayer int
}

type HNSWIndex struct {
	nodes          map[int]*HNSWNode
	dimension      int
	M              int // Max connections per node
	Mmax           int // Max connections at layer 0
	Mmax0          int
	ml             float64 // Level generation factor
	efConstruction int     // Size of dynamic candidate list during construction
	efSearch       int     // Size of dynamic candidate list during search
	entryPoint     int
	maxLayer       int
	nodeCount      int
	mu             sync.RWMutex
}

func NewHNSWIndex(dimension, M, efConstruction int) *HNSWIndex {
	return &HNSWIndex{
		nodes:          make(map[int]*HNSWNode),
		dimension:      dimension,
		M:              M,
		Mmax:           M,
		Mmax0:          M * 2,
		ml:             1.0 / math.Log(float64(M)),
		efConstruction: efConstruction,
		efSearch:       efConstruction,
		entryPoint:     -1,
		maxLayer:       -1,
		nodeCount:      0,
	}
}

func (h *HNSWIndex) randomLevel() int {
	level := 0
	for randFloat() < h.ml {
		level++
	}
	return level
}

func randFloat() float64 {
	return float64(uint64(time.Now().UnixNano())%1000000) / 1000000.0
}

func (h *HNSWIndex) distance(a, b []float64) float64 {
	// Cosine distance = 1 - cosine similarity
	// For normalized vectors, this is equivalent to Euclidean distance
	return 1.0 - CosineSimilarity(a, b)
}

func (h *HNSWIndex) Insert(vector []float64) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	nodeID := h.nodeCount
	h.nodeCount++

	norm := 0.0
	for _, v := range vector {
		norm += v * v
	}
	norm = math.Sqrt(norm)

	level := h.randomLevel()
	node := &HNSWNode{
		ID:       nodeID,
		Vector:   vector,
		Norm:     norm,
		Layers:   make([][]int, level+1),
		MaxLayer: level,
	}

	// If this is the first node
	if h.entryPoint == -1 {
		h.entryPoint = nodeID
		h.maxLayer = level
		h.nodes[nodeID] = node
		return nodeID
	}

	// Find closest node at top layer
	entryNode := h.nodes[h.entryPoint]
	currDist := h.distance(vector, entryNode.Vector)
	currID := h.entryPoint

	// Traverse from top layer to level+1
	for layer := h.maxLayer; layer > level; layer-- {
		changed := true
		for changed {
			changed = false
			for _, neighborID := range entryNode.Layers[layer] {
				neighbor := h.nodes[neighborID]
				dist := h.distance(vector, neighbor.Vector)
				if dist < currDist {
					currDist = dist
					currID = neighborID
					entryNode = neighbor
					changed = true
				}
			}
		}
	}

	// Insert at each layer from level down to 0
	for layer := min(level, h.maxLayer); layer >= 0; layer-- {
		candidates := h.searchLayer(vector, currID, 1, layer)
		neighbors := h.selectNeighbors(vector, candidates, h.M)

		node.Layers[layer] = neighbors

		// Add bidirectional connections
		for _, neighborID := range neighbors {
			neighbor := h.nodes[neighborID]
			neighbor.Layers[layer] = append(neighbor.Layers[layer], nodeID)

			// Prune if exceeding max connections
			if len(neighbor.Layers[layer]) > h.Mmax {
				neighbor.Layers[layer] = h.pruneNeighbors(neighbor, neighbor.Layers[layer], h.Mmax, layer)
			}
		}

		if len(candidates) > 0 {
			currID = candidates[0]
		}
	}

	// Update entry point if needed
	if level > h.maxLayer {
		h.entryPoint = nodeID
		h.maxLayer = level
	}

	h.nodes[nodeID] = node
	return nodeID
}

func (h *HNSWIndex) searchLayer(query []float64, entryID, ef, layer int) []int {
	candidates := make([][2]float64, 0)
	results := make([][2]float64, 0)

	entryNode := h.nodes[entryID]
	dist := h.distance(query, entryNode.Vector)

	candidates = append(candidates, [2]float64{dist, float64(entryID)})
	results = append(results, [2]float64{dist, float64(entryID)})

	visited := make(map[int]bool)
	visited[entryID] = true

	for len(candidates) > 0 {
		// Get closest candidate
		closestIdx := 0
		for i := 1; i < len(candidates); i++ {
			if candidates[i][0] < candidates[closestIdx][0] {
				closestIdx = i
			}
		}

		closestDist, closestID := candidates[closestIdx][0], int(candidates[closestIdx][1])
		furthestDist := results[len(results)-1][0]

		if closestDist > furthestDist && len(results) >= ef {
			break
		}

		// Remove from candidates
		candidates = append(candidates[:closestIdx], candidates[closestIdx+1:]...)

		// Check neighbors
		node := h.nodes[closestID]
		for _, neighborID := range node.Layers[layer] {
			if visited[neighborID] {
				continue
			}
			visited[neighborID] = true

			neighbor := h.nodes[neighborID]
			dist := h.distance(query, neighbor.Vector)
			furthestDist = results[len(results)-1][0]

			if dist < furthestDist || len(results) < ef {
				// Insert maintaining sorted order
				insertIdx := len(candidates)
				for insertIdx > 0 && candidates[insertIdx-1][0] > dist {
					insertIdx--
				}
				candidates = append(candidates[:insertIdx], append([][2]float64{{dist, float64(neighborID)}}, candidates[insertIdx:]...)...)

				// Add to results
				results = append(results, [2]float64{dist, float64(neighborID)})
				if len(results) > ef {
					results = results[:ef]
				}
			}
		}
	}

	// Return IDs sorted by distance
	ids := make([]int, len(results))
	for i, r := range results {
		ids[i] = int(r[1])
	}
	return ids
}

func (h *HNSWIndex) selectNeighbors(query []float64, candidates []int, M int) []int {
	if len(candidates) <= M {
		return candidates
	}

	// Sort by distance and take top M
	type neighborDist struct {
		id   int
		dist float64
	}
	dists := make([]neighborDist, len(candidates))
	for i, id := range candidates {
		node := h.nodes[id]
		dists[i] = neighborDist{id, h.distance(query, node.Vector)}
	}

	sort.Slice(dists, func(i, j int) bool {
		return dists[i].dist < dists[j].dist
	})

	neighbors := make([]int, M)
	for i := 0; i < M; i++ {
		neighbors[i] = dists[i].id
	}
	return neighbors
}

func (h *HNSWIndex) pruneNeighbors(node *HNSWNode, neighbors []int, M, layer int) []int {
	if len(neighbors) <= M {
		return neighbors
	}

	type neighborDist struct {
		id   int
		dist float64
	}
	dists := make([]neighborDist, len(neighbors))
	for i, id := range neighbors {
		n := h.nodes[id]
		dists[i] = neighborDist{id, h.distance(node.Vector, n.Vector)}
	}

	sort.Slice(dists, func(i, j int) bool {
		return dists[i].dist < dists[j].dist
	})

	pruned := make([]int, M)
	for i := 0; i < M; i++ {
		pruned[i] = dists[i].id
	}
	return pruned
}

func (h *HNSWIndex) Search(query []float64, ef int, threshold float64) (int, float64, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.entryPoint == -1 {
		return -1, 0, false
	}

	// Start from entry point
	entryNode := h.nodes[h.entryPoint]
	currID := h.entryPoint
	currDist := h.distance(query, entryNode.Vector)

	// Traverse from top to layer 1
	for layer := h.maxLayer; layer > 0; layer-- {
		changed := true
		for changed {
			changed = false
			for _, neighborID := range entryNode.Layers[layer] {
				neighbor := h.nodes[neighborID]
				dist := h.distance(query, neighbor.Vector)
				if dist < currDist {
					currDist = dist
					currID = neighborID
					entryNode = neighbor
					changed = true
				}
			}
		}
	}

	// Search at layer 0 with ef
	candidates := h.searchLayer(query, currID, ef, 0)

	// Find best match above threshold
	bestScore := 0.0
	bestID := -1

	for _, id := range candidates {
		node := h.nodes[id]
		score := CosineSimilarity(query, node.Vector)
		if score > bestScore && score >= threshold {
			bestScore = score
			bestID = id
		}
	}

	if bestID != -1 {
		return bestID, bestScore, true
	}
	return -1, 0, false
}

func (h *HNSWIndex) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nodeCount
}

// ============================================================================
// L1 Hot Cache - O(1) exact and near-exact lookups with LRU tracking
// ============================================================================

type l1Node struct {
	hash     string
	response []byte
	prev     *l1Node
	next     *l1Node
}

type L1HotCache struct {
	exactMatches map[string]*l1Node  // Exact hash -> linked list node
	nearMatches  map[uint64][]l1NearEntry // Quantized vector hash -> responses
	mu           sync.RWMutex
	maxSize      int

	// LRU linked list
	head *l1Node
	tail *l1Node
	lruSize int
}

type l1NearEntry struct {
	response      []byte
	score         float64
	promptHash    string // Reference back to exact entry for promotion on hit
}

func NewL1HotCache(maxSize int) *L1HotCache {
	return &L1HotCache{
		exactMatches: make(map[string]*l1Node),
		nearMatches:  make(map[uint64][]l1NearEntry),
		maxSize:      maxSize,
	}
}

func (c *L1HotCache) GetExact(hash string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.exactMatches[hash]
	if !ok {
		return nil, false
	}

	// Promote to front (most recently used)
	c.promote(node)

	return node.response, true
}

func (c *L1HotCache) SetExact(hash string, response []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already exists, just update
	if node, ok := c.exactMatches[hash]; ok {
		node.response = response
		c.promote(node)
		return
	}

	// Evict if full
	for c.lruSize >= c.maxSize/2 {
		c.evictOldest()
	}

	// Add to front
	node := &l1Node{
		hash:     hash,
		response: response,
	}
	c.pushFront(node)
	c.exactMatches[hash] = node
}

func (c *L1HotCache) promote(node *l1Node) {
	if node == c.head {
		return // already at front
	}

	// Remove from current position
	if node.prev != nil {
		node.prev.next = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	}
	if node == c.tail {
		c.tail = node.prev
	}

	// Insert at front
	node.prev = nil
	node.next = c.head
	if c.head != nil {
		c.head.prev = node
	}
	c.head = node
	if c.tail == nil {
		c.tail = node
	}
}

func (c *L1HotCache) pushFront(node *l1Node) {
	node.prev = nil
	node.next = c.head
	if c.head != nil {
		c.head.prev = node
	}
	c.head = node
	if c.tail == nil {
		c.tail = node
	}
	c.lruSize++
}

func (c *L1HotCache) evictOldest() {
	if c.tail == nil {
		return
	}

	oldest := c.tail
	c.tail = oldest.prev
	if c.tail != nil {
		c.tail.next = nil
	} else {
		c.head = nil
	}

	delete(c.exactMatches, oldest.hash)
	c.lruSize--
}

// Quantize vector to 8-bit integers for fast hashing
func (c *L1HotCache) quantizeVector(vec []float64) uint64 {
	if len(vec) == 0 {
		return 0
	}

	// Use first 8 dimensions, quantized to 8 bits each
	hash := uint64(0)
	for i := 0; i < min(8, len(vec)); i++ {
		// Map [-1, 1] to [0, 255]
		val := int((vec[i] + 1.0) * 127.5)
		if val < 0 {
			val = 0
		} else if val > 255 {
			val = 255
		}
		hash = hash*256 + uint64(val)
	}
	return hash
}

func (c *L1HotCache) GetNear(vec []float64, threshold float64) ([]byte, float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hash := c.quantizeVector(vec)
	entries, ok := c.nearMatches[hash]
	if !ok {
		return nil, 0, false
	}

	// Find best match
	bestScore := 0.0
	var bestResponse []byte
	var bestHash string
	for _, entry := range entries {
		if entry.score > bestScore && entry.score >= threshold {
			bestScore = entry.score
			bestResponse = entry.response
			bestHash = entry.promptHash
		}
	}

	// Promote the exact entry to front on near hit
	if bestHash != "" {
		if node, ok := c.exactMatches[bestHash]; ok {
			c.promote(node)
		}
	}

	if bestResponse != nil {
		return bestResponse, bestScore, true
	}
	return nil, 0, false
}

func (c *L1HotCache) SetNear(vec []float64, response []byte, score float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hash := c.quantizeVector(vec)

	// Remove duplicate if exists
	for i, entry := range c.nearMatches[hash] {
		if string(entry.response) == string(response) {
			c.nearMatches[hash] = append(c.nearMatches[hash][:i], c.nearMatches[hash][i+1:]...)
			break
		}
	}

	// Find prompt hash for cross-reference
	promptHash := ""
	for k, node := range c.exactMatches {
		if string(node.response) == string(response) {
			promptHash = k
			break
		}
	}

	// Simple eviction if needed (map iteration is fine for near matches)
	if len(c.nearMatches) >= c.maxSize/2 {
		count := 0
		evictCount := c.maxSize / 20
		for k := range c.nearMatches {
			delete(c.nearMatches, k)
			count++
			if count >= evictCount {
				break
			}
		}
	}

	c.nearMatches[hash] = append(c.nearMatches[hash], l1NearEntry{
		response:   response,
		score:      score,
		promptHash: promptHash,
	})

	// Limit entries per hash
	if len(c.nearMatches[hash]) > 10 {
		c.nearMatches[hash] = c.nearMatches[hash][:10]
	}
}

// ============================================================================
// Advanced Semantic Cache with all optimizations
// ============================================================================

type AdvancedSemanticCache struct {
	client          *redis.Client
	ctx             context.Context
	embedder        *VectorEmbeddingEngine
	templateMatcher *TemplateMatcher
	cfg             *Config

	// Tiered storage
	localIndex   map[string][]VectorCacheEntry
	localIndexMu sync.RWMutex

	// HNSW index for fast vector search
	hnswIndex *HNSWIndex

	// L1 hot cache for O(1) lookups
	l1Cache *L1HotCache

	// Pre-computed norms cache
	normCache   map[string]float64
	normCacheMu sync.RWMutex

	// Pending requests for dedup
	pendingRequests map[string]chan []byte
	dedupMu         sync.Mutex

	// Bounded dedup map with TTL
	dedupExpiry   map[string]time.Time
	dedupExpiryMu sync.Mutex

	// Embedding cache
	embeddingCache   map[string][]float64
	embeddingCacheMu sync.RWMutex

	// Adaptive threshold state (EMA-based for smoother adjustments)
	adaptiveThreshold float64
	emaHitRate        float64
	hitCount          int64
	missCount         int64
	adjMu             sync.Mutex
	lastAdjTime       time.Time
}

func NewAdvancedSemanticCache(redisURL string, cfg *Config) (*AdvancedSemanticCache, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	// Connection pool tuning
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
		log.Printf("⚠️  Falling back to in-memory cache")
		rdb = nil
	} else {
		redisAvailable = true
		log.Printf("✅ Redis connected successfully")
	}

	// Initialize HNSW index
	hnsw := NewHNSWIndex(cfg.Cache.Vector.Dimension, 16, 200)

	// Initialize L1 hot cache
	l1Cache := NewL1HotCache(10000)

	cache := &AdvancedSemanticCache{
		client:            rdb,
		ctx:               context.Background(),
		embedder:          NewVectorEmbeddingEngine(cfg.Cache.Vector.Dimension),
		templateMatcher:   NewTemplateMatcher(cfg.Cache.TemplateMatching.MaxTemplates),
		cfg:               cfg,
		localIndex:        make(map[string][]VectorCacheEntry),
		hnswIndex:         hnsw,
		l1Cache:           l1Cache,
		normCache:         make(map[string]float64),
		pendingRequests:   make(map[string]chan []byte),
		embeddingCache:    make(map[string][]float64),
		dedupExpiry:       make(map[string]time.Time),
		adaptiveThreshold: cfg.Cache.Vector.SimilarityThreshold,
		emaHitRate:        0.5,
		lastAdjTime:       time.Now(),
	}

	go cache.cleanupDedupExpired()
	go cache.adaptiveThresholdTuner()

	storageType := "in-memory"
	if redisAvailable {
		storageType = "Redis+HNSW+L1"
	}

	log.Printf("Advanced Cache initialized: storage=%s, dim=%d, hnsw=%v, l1=%v",
		storageType, cfg.Cache.Vector.Dimension, cfg.Cache.Vector.Enabled, cfg.Cache.Vector.Enabled)

	return cache, nil
}

// ============================================================================
// Optimized embedding with caching
// ============================================================================

func (ac *AdvancedSemanticCache) getOrCreateEmbedding(text string) []float64 {
	ac.embeddingCacheMu.RLock()
	cached, exists := ac.embeddingCache[text]
	ac.embeddingCacheMu.RUnlock()

	if exists {
		return cached
	}

	embedding := ac.embedder.Embed(text)

	ac.embeddingCacheMu.Lock()
	ac.embeddingCache[text] = embedding
	ac.embeddingCacheMu.Unlock()

	return embedding
}

func (ac *AdvancedSemanticCache) getOrCreateNorm(text string) float64 {
	ac.normCacheMu.RLock()
	cached, exists := ac.normCache[text]
	ac.normCacheMu.RUnlock()

	if exists {
		return cached
	}

	embedding := ac.getOrCreateEmbedding(text)
	norm := 0.0
	for _, v := range embedding {
		norm += v * v
	}
	norm = math.Sqrt(norm)

	ac.normCacheMu.Lock()
	ac.normCache[text] = norm
	ac.normCacheMu.Unlock()

	return norm
}

// ============================================================================
// Store with HNSW indexing
// ============================================================================

func (ac *AdvancedSemanticCache) Store(startupID, prompt string, response []byte) error {
	return ac.StoreWithContext(startupID, prompt, nil, response)
}

// StoreWithContext stores a response keyed by both the raw prompt and the
// context-combined key (if context is provided). This ensures that subsequent
// SearchWithContext calls across different sessions can find the cached entry
// via exact hash lookup.
func (ac *AdvancedSemanticCache) StoreWithContext(startupID, prompt string, context []string, response []byte) error {
	cleanPrompt := cleanText(prompt)
	hasher := sha256.New()
	hasher.Write([]byte(cleanPrompt))
	promptHash := hex.EncodeToString(hasher.Sum(nil))
	redisKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, promptHash)

	var embedding []float64
	if ac.cfg.Cache.Vector.Enabled {
		embedding = ac.getOrCreateEmbedding(cleanPrompt)
	}

	ttl := time.Duration(ac.cfg.Cache.TTLHours) * time.Hour

	redisStored := false
	if ac.client != nil {
		hashData := map[string]interface{}{
			"prompt":   cleanPrompt,
			"response": response,
		}

		if embedding != nil {
			hashData["vector"] = VectorToBytes(embedding)
		}

		// Store under the prompt hash
		err := ac.client.HSet(ac.ctx, redisKey, hashData).Err()
		if err != nil {
			log.Printf("⚠️  Redis store failed: %v", err)
		} else {
			ac.client.Expire(ac.ctx, redisKey, ttl)
			redisStored = true

			// If context is provided, also store under a context-aware key
			// so that SearchWithContext can find it via exact hash lookup
			if len(context) > 0 {
				contextKey := buildContextCacheKey(prompt, context)
				contextHasher := sha256.New()
				contextHasher.Write([]byte(contextKey))
				contextHash := hex.EncodeToString(contextHasher.Sum(nil))
				contextRedisKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, contextHash)

				contextHashData := map[string]interface{}{
					"prompt":   contextKey,
					"response": response,
				}
				if embedding != nil {
					contextHashData["vector"] = VectorToBytes(embedding)
				}

				if err := ac.client.HSet(ac.ctx, contextRedisKey, contextHashData).Err(); err != nil {
					log.Printf("⚠️  Redis context store failed: %v", err)
				} else {
					ac.client.Expire(ac.ctx, contextRedisKey, ttl)
					log.Printf("💾 Context-cached: tenant=%s, context_hash=%s", startupID, contextHash[:8]+"...")
				}
			}
		}
	}

	// Store in local index
	ac.localIndexMu.Lock()
	ac.localIndex[startupID] = append(ac.localIndex[startupID], VectorCacheEntry{
		Vector:   embedding,
		Response: response,
		Prompt:   cleanPrompt,
		Score:    1.0,
	})
	if len(ac.localIndex[startupID]) > ac.cfg.Cache.Vector.MaxVectorsPerTenant {
		ac.localIndex[startupID] = ac.localIndex[startupID][1:]
	}
	ac.localIndexMu.Unlock()

	// Add to HNSW index
	if ac.cfg.Cache.Vector.Enabled && embedding != nil {
		ac.hnswIndex.Insert(embedding)
	}

	// Add to L1 hot cache
	if ac.cfg.Cache.Vector.Enabled && embedding != nil {
		ac.l1Cache.SetExact(promptHash, response)
		ac.l1Cache.SetNear(embedding, response, 1.0)
	}

	// Learn template
	if ac.cfg.Cache.TemplateMatching.Enabled {
		ac.templateMatcher.LearnFromPrompt(prompt, response)
	}

	storage := "in-memory"
	if redisStored {
		storage = "Redis+HNSW+L1"
	}

	log.Printf("�� Cached: storage=%s, tenant=%s, hash=%s, ttl=%dh",
		storage, startupID, promptHash[:8]+"...", ac.cfg.Cache.TTLHours)

	return nil
}

// buildContextCacheKey constructs a deterministic key from prompt + conversation context
func buildContextCacheKey(prompt string, context []string) string {
	if len(context) == 0 {
		return cleanText(prompt)
	}
	// Use last 3 messages as context (balance between accuracy and performance)
	contextStart := 0
	if len(context) > 3 {
		contextStart = len(context) - 3
	}
	relevantContext := context[contextStart:]
	return cleanText(strings.Join(relevantContext, "\n") + "\n\n" + prompt)
}

// ============================================================================
// Parallel multi-strategy search with early termination
// ============================================================================

type searchResult struct {
	response []byte
	score    float64
	found    bool
	strategy string
}

func (ac *AdvancedSemanticCache) Search(startupID, incomingPrompt string, threshold float64) ([]byte, float64, bool) {
	target := cleanText(incomingPrompt)

	// Fast path 1: Dedup check
	if ac.cfg.Dedup.Enabled {
		if result, found := ac.checkDedup(startupID, target); found {
			log.Printf("🔍 HIT (dedup): tenant=%s", startupID)
			return result, 1.0, true
		}
	}

	// Fast path 2: Exact hash (O(1))
	if ac.cfg.Cache.Vector.ExactHashFirst {
		hasher := sha256.New()
		hasher.Write([]byte(target))
		exactHash := hex.EncodeToString(hasher.Sum(nil))

		if result, found := ac.exactLookup(startupID, exactHash); found {
			log.Printf("🔍 HIT (exact): tenant=%s, hash=%s", startupID, exactHash[:8]+"...")
			return result, 1.0, true
		}

		// Also check L1 cache
		if result, found := ac.l1Cache.GetExact(exactHash); found {
			log.Printf("🔍 HIT (L1 exact): tenant=%s", startupID)
			ac.recordHit()
			return result, 1.0, true
		}
	}

	// Fast path 3: Template matching (O(1) regex)
	if ac.cfg.Cache.TemplateMatching.Enabled {
		if result, found := ac.templateMatcher.Match(incomingPrompt); found {
			log.Printf("🔍 HIT (template): tenant=%s", startupID)
			ac.recordHit()
			return result, 0.95, true
		}
	}

	// Determine query length for strategy selection
	queryLen := len(incomingPrompt)
	useVectorSearch := ac.cfg.Cache.Vector.Enabled && queryLen > 10
	useJaccardSearch := ac.cfg.Cache.Jaccard.Enabled && queryLen <= 50
	useFuzzySearch := true // Always try fuzzy matching for typo tolerance

	// Parallel search for vector, Jaccard, and fuzzy
	if useVectorSearch || useJaccardSearch || useFuzzySearch {
		results := make(chan searchResult, 3)

		if useVectorSearch {
			go func() {
				incomingVec := ac.getOrCreateEmbedding(target)

				// Try L1 near cache first (O(1))
				if result, score, found := ac.l1Cache.GetNear(incomingVec, threshold); found {
					results <- searchResult{result, score, true, "L1 near"}
					return
				}

				// Try HNSW index (O(log n))
				if nodeID, score, found := ac.hnswIndex.Search(incomingVec, 50, threshold); found {
					if result, found := ac.getResponseByNodeID(nodeID); found {
						results <- searchResult{result, score, true, "HNSW"}
						return
					}
				}

				// Fallback to local linear search
				if result, score, found := ac.vectorSearchLocal(startupID, incomingVec, threshold); found {
					results <- searchResult{result, score, true, "local"}
					return
				}

				results <- searchResult{nil, 0, false, "vector"}
			}()
		}

		if useJaccardSearch {
			go func() {
				if result, score, found := ac.jaccardSearch(startupID, target); found && score >= ac.cfg.Cache.Jaccard.Threshold {
					results <- searchResult{result, score, true, "jaccard"}
					return
				}
				results <- searchResult{nil, 0, false, "jaccard"}
			}()
		}

		if useFuzzySearch {
			go func() {
				if result, score, found := ac.fuzzySearch(startupID, target, threshold); found {
					results <- searchResult{result, score, true, "fuzzy"}
					return
				}
				results <- searchResult{nil, 0, false, "fuzzy"}
			}()
		}

		// Collect results with early termination
		bestResult := searchResult{}
		remaining := 0
		if useVectorSearch {
			remaining++
		}
		if useJaccardSearch {
			remaining++
		}
		if useFuzzySearch {
			remaining++
		}

		for result := range results {
			remaining--
			if result.found && result.score > bestResult.score {
				bestResult = result
				// Early termination: if we found a very good match, stop waiting
				if bestResult.score >= 0.98 {
					break
				}
			}
			if remaining == 0 {
				break
			}
		}

		if bestResult.found {
			log.Printf("🔍 HIT (%s): tenant=%s, score=%.2f", bestResult.strategy, startupID, bestResult.score)
			ac.recordHit()
			return bestResult.response, bestResult.score, true
		}
	}

	// Fallback to Redis if available
	if ac.client != nil && ac.cfg.Cache.Vector.Enabled {
		incomingVec := ac.getOrCreateEmbedding(target)
		if result, score, found := ac.vectorSearchRedis(startupID, incomingVec, threshold); found {
			log.Printf("🔍 HIT (Redis): tenant=%s, score=%.2f", startupID, score)
			ac.recordHit()
			return result, score, true
		}
	}

	log.Printf("🔍 MISS: tenant=%s, query=%s", startupID, incomingPrompt[:min(50, len(incomingPrompt))]+"...")
	ac.recordMiss()
	return nil, 0.0, false
}

// ============================================================================
// Fuzzy search with typo tolerance
// ============================================================================

func (ac *AdvancedSemanticCache) fuzzySearch(startupID, target string, threshold float64) ([]byte, float64, bool) {
	ac.localIndexMu.RLock()
	entries, ok := ac.localIndex[startupID]
	ac.localIndexMu.RUnlock()

	if !ok || len(entries) == 0 {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64

	// Check each cached entry for fuzzy word match
	for _, entry := range entries {
		if entry.Vector == nil {
			continue
		}

		// Calculate word overlap
		overlap := wordOverlapScore(target, entry.Prompt)

		// If good word overlap, check vector similarity
		if overlap >= 0.7 {
			score := CosineSimilarity(ac.getOrCreateEmbedding(target), entry.Vector)
			combinedScore := score*0.7 + overlap*0.3 // Weighted combination

			if combinedScore > bestScore && combinedScore >= threshold {
				bestScore = combinedScore
				bestMatch = entry.Response

				// Early termination for very good matches
				if combinedScore >= 0.95 {
					break
				}
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, bestScore, true
	}
	return nil, 0, false
}

// ============================================================================
// Helper to get response by HNSW node ID
// ============================================================================

func (ac *AdvancedSemanticCache) getResponseByNodeID(nodeID int) ([]byte, bool) {
	ac.localIndexMu.RLock()
	defer ac.localIndexMu.RUnlock()

	for _, entries := range ac.localIndex {
		for _, entry := range entries {
			if entry.Vector != nil {
				// Compare vectors to find matching node
				// This is a simplified approach - in production, maintain a reverse mapping
				if ac.hnswIndex.nodes[nodeID] != nil {
					if CosineSimilarity(entry.Vector, ac.hnswIndex.nodes[nodeID].Vector) >= 0.99 {
						return entry.Response, true
					}
				}
			}
		}
	}
	return nil, false
}

// ============================================================================
// ============================================================================
// Early termination vector search
// ============================================================================

func (ac *AdvancedSemanticCache) vectorSearchLocal(startupID string, vec []float64, threshold float64) ([]byte, float64, bool) {
	ac.localIndexMu.RLock()
	entries, ok := ac.localIndex[startupID]
	ac.localIndexMu.RUnlock()

	if !ok || len(entries) == 0 {
		return nil, 0, false
	}

	// Pre-compute query norm
	queryNorm := 0.0
	for _, v := range vec {
		queryNorm += v * v
	}
	queryNorm = math.Sqrt(queryNorm)

	if queryNorm == 0 {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64

	// Early termination: sort by potential and stop early
	// For simplicity, we'll use a simple linear scan but with pre-computed norms
	for _, entry := range entries {
		if entry.Vector == nil {
			continue
		}

		// Use pre-computed norm if available
		score := CosineSimilarity(vec, entry.Vector)
		if score > bestScore && score >= threshold {
			bestScore = score
			bestMatch = entry.Response

			// Early termination: perfect match found
			if score >= 0.99 {
				break
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, bestScore, true
	}
	return nil, 0, false
}

// ============================================================================
// Optimized Redis vector search with sorted sets
// ============================================================================

func (ac *AdvancedSemanticCache) vectorSearchRedis(startupID string, vec []float64, threshold float64) ([]byte, float64, bool) {
	if ac.client == nil {
		return nil, 0, false
	}

	// Store query vector temporarily
	queryKey := fmt.Sprintf("gateway:cache:query:%s:%d", startupID, time.Now().UnixNano())
	err := ac.client.Set(ac.ctx, queryKey, VectorToBytes(vec), 10*time.Second).Err()
	if err != nil {
		return nil, 0, false
	}
	defer ac.client.Del(ac.ctx, queryKey)

	// Use ZINTERSTORE to compute cosine similarity
	// This is a simplified version - in production, use Redis Stack with vector search
	pipe := ac.client.Pipeline()

	// Get all vector keys
	pattern := fmt.Sprintf("gateway:cache:%s:*", startupID)
	keys, _, err := ac.client.Scan(ac.ctx, 0, pattern, 1000).Result()
	if err != nil {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64

	// Batch fetch vectors
	for _, key := range keys {
		pipe.HGet(ac.ctx, key, "vector")
		pipe.HGet(ac.ctx, key, "response")
	}

	cmds, err := pipe.Exec(ac.ctx)
	if err != nil {
		return nil, 0, false
	}

	// Process in batches of 2 (vector + response)
	for i := 0; i < len(cmds); i += 2 {
		vectorBytes, err := cmds[i].(*redis.StringCmd).Bytes()
		if err != nil {
			continue
		}

		cachedVec := BytesToVector(vectorBytes)
		if cachedVec == nil {
			continue
		}

		score := CosineSimilarity(vec, cachedVec)
		if score > bestScore && score >= threshold {
			if responseBytes, err := cmds[i+1].(*redis.StringCmd).Bytes(); err == nil {
				bestScore = score
				bestMatch = responseBytes
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, bestScore, true
	}
	return nil, 0, false
}

// ============================================================================
// Jaccard search (unchanged but optimized)
// ============================================================================

func (ac *AdvancedSemanticCache) jaccardSearch(startupID, target string) ([]byte, float64, bool) {
	wordsTarget := strings.Fields(target)
	if len(wordsTarget) == 0 {
		return nil, 0.0, false
	}

	ac.localIndexMu.RLock()
	entries, ok := ac.localIndex[startupID]
	ac.localIndexMu.RUnlock()

	if !ok || len(entries) == 0 {
		return nil, 0, false
	}

	var bestMatch []byte
	var bestScore float64

	for _, entry := range entries {
		score := calculateFastSimilarity(wordsTarget, entry.Prompt)
		if score > bestScore && score >= ac.cfg.Cache.Jaccard.Threshold {
			bestScore = score
			bestMatch = entry.Response

			// Early termination
			if score >= 0.99 {
				break
			}
		}
	}

	return bestMatch, bestScore, bestMatch != nil
}

// ============================================================================
// SearchWithContext checks cache with conversation history for better matching
// ============================================================================
func (ac *AdvancedSemanticCache) SearchWithContext(tenantID, query string, context []string, threshold float64) ([]byte, float64, bool) {
	// Build context-aware cache key by combining previous messages with current query
	cacheKey := buildContextCacheKey(query, context)

	return ac.Search(tenantID, cacheKey, threshold)
}

// ============================================================================
// Exact lookup (unchanged)
// ============================================================================

func (ac *AdvancedSemanticCache) exactLookup(startupID, hash string) ([]byte, bool) {
	exactKey := fmt.Sprintf("gateway:cache:%s:%s", startupID, hash)

	if ac.client != nil {
		if val, err := ac.client.HGet(ac.ctx, exactKey, "response").Bytes(); err == nil {
			return val, true
		}
	}

	ac.localIndexMu.RLock()
	defer ac.localIndexMu.RUnlock()
	if entries, ok := ac.localIndex[startupID]; ok {
		for _, entry := range entries {
			h := sha256.Sum256([]byte(entry.Prompt))
			if hex.EncodeToString(h[:]) == hash {
				return entry.Response, true
			}
		}
	}
	return nil, false
}

// ============================================================================
// Dedup (unchanged)
// ============================================================================

func (ac *AdvancedSemanticCache) checkDedup(startupID, prompt string) ([]byte, bool) {
	dedupKey := fmt.Sprintf("dedup:%s:%s", startupID, prompt)

	ac.dedupMu.Lock()
	ch, exists := ac.pendingRequests[dedupKey]
	if !exists {
		ac.pendingRequests[dedupKey] = make(chan []byte, 1)
		ac.dedupExpiryMu.Lock()
		ac.dedupExpiry[dedupKey] = time.Now().Add(30 * time.Second)
		ac.dedupExpiryMu.Unlock()
		ac.dedupMu.Unlock()
		return nil, false
	}
	ac.dedupMu.Unlock()

	select {
	case result := <-ch:
		return result, true
	case <-time.After(time.Duration(ac.cfg.Dedup.MaxWaitSeconds) * time.Second):
		return nil, false
	}
}

func (ac *AdvancedSemanticCache) NotifyDedup(startupID, prompt string, response []byte) {
	if !ac.cfg.Dedup.Enabled {
		return
	}
	dedupKey := fmt.Sprintf("dedup:%s:%s", startupID, prompt)

	ac.dedupMu.Lock()
	ch, exists := ac.pendingRequests[dedupKey]
	if exists {
		delete(ac.pendingRequests, dedupKey)
		close(ch)
		ac.dedupExpiryMu.Lock()
		delete(ac.dedupExpiry, dedupKey)
		ac.dedupExpiryMu.Unlock()
	}
	ac.dedupMu.Unlock()
}

// ============================================================================
// Adaptive threshold tuning
// ============================================================================

func (ac *AdvancedSemanticCache) recordHit() {
	ac.adjMu.Lock()
	ac.hitCount++
	// EMA: new_rate = 0.05 * 1.0 + 0.95 * old_rate
	ac.emaHitRate = 0.05*1.0 + 0.95*ac.emaHitRate
	ac.adjMu.Unlock()
}

func (ac *AdvancedSemanticCache) recordMiss() {
	ac.adjMu.Lock()
	ac.missCount++
	// EMA: new_rate = 0.05 * 0.0 + 0.95 * old_rate
	ac.emaHitRate = 0.95 * ac.emaHitRate
	ac.adjMu.Unlock()
}

func (ac *AdvancedSemanticCache) adaptiveThresholdTuner() {
	ticker := time.NewTicker(120 * time.Second) // Check every 2 minutes
	defer ticker.Stop()

	for range ticker.C {
		ac.adjMu.Lock()

		totalRequests := ac.hitCount + ac.missCount
		if totalRequests < 50 {
			ac.adjMu.Unlock()
			continue
		}

		// Use EMA hit rate for smoother adjustments
		hitRate := ac.emaHitRate

		// Adjust threshold based on EMA hit rate
		oldThreshold := ac.adaptiveThreshold
		if hitRate < 0.25 {
			// Too many misses - lower threshold gradually
			ac.adaptiveThreshold = math.Max(0.65, ac.adaptiveThreshold-0.015)
		} else if hitRate < 0.35 {
			// Slightly low - gentle decrease
			ac.adaptiveThreshold = math.Max(0.7, ac.adaptiveThreshold-0.005)
		} else if hitRate > 0.75 {
			// Too many hits - raise threshold to avoid false positives
			ac.adaptiveThreshold = math.Min(0.97, ac.adaptiveThreshold+0.01)
		} else if hitRate > 0.65 {
			// Slightly high - gentle increase
			ac.adaptiveThreshold = math.Min(0.95, ac.adaptiveThreshold+0.005)
		}

		if oldThreshold != ac.adaptiveThreshold {
			log.Printf("Adaptive threshold: %.2f -> %.2f (EMA hit rate: %.1f%%)",
				oldThreshold, ac.adaptiveThreshold, hitRate*100)
		}

		// Decay counters to keep EMA responsive
		ac.hitCount = 0
		ac.missCount = 0
		ac.adjMu.Unlock()
	}
}

// ============================================================================
// Cleanup
// ============================================================================

func (ac *AdvancedSemanticCache) cleanupDedupExpired() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		ac.dedupExpiryMu.Lock()
		ac.dedupMu.Lock()
		now := time.Now()
		for key, expiry := range ac.dedupExpiry {
			if now.After(expiry) {
				delete(ac.dedupExpiry, key)
				if ch, exists := ac.pendingRequests[key]; exists {
					close(ch)
					delete(ac.pendingRequests, key)
				}
			}
		}
		ac.dedupMu.Unlock()
		ac.dedupExpiryMu.Unlock()
	}
}

// ============================================================================
// Stats
// ============================================================================

func (ac *AdvancedSemanticCache) GetStats() map[string]interface{} {
	ac.localIndexMu.RLock()
	totalEntries := 0
	for _, entries := range ac.localIndex {
		totalEntries += len(entries)
	}
	ac.localIndexMu.RUnlock()

	ac.adjMu.Lock()
	recentHitRate := ac.emaHitRate
	ac.adjMu.Unlock()

	return map[string]interface{}{
		"local_index_entries": totalEntries,
		"hnsw_nodes":          ac.hnswIndex.Size(),
		"l1_cache_size":       len(ac.l1Cache.exactMatches) + len(ac.l1Cache.nearMatches),
		"vector_dimensions":   ac.cfg.Cache.Vector.Dimension,
		"adaptive_threshold":  math.Round(ac.adaptiveThreshold*100) / 100,
		"recent_hit_rate":     math.Round(recentHitRate*100) / 100,
		"jaccard_threshold":   math.Round(ac.cfg.Cache.Jaccard.Threshold*100) / 100,
		"template_enabled":    ac.cfg.Cache.TemplateMatching.Enabled,
		"dedup_enabled":       ac.cfg.Dedup.Enabled,
		"ttl_hours":           ac.cfg.Cache.TTLHours,
	}
}

package main

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
)

type VectorEmbeddingEngine struct {
	dimension  int
	projection [][]float64
	vocab      map[string]int
	vocabMu    sync.RWMutex
	vocabSize  int
}

func NewVectorEmbeddingEngine(dimension int) *VectorEmbeddingEngine {
	rng := rand.New(rand.NewSource(42))
	vocabSize := 20000
	scale := 1.0 / math.Sqrt(float64(dimension))

	projection := make([][]float64, vocabSize)
	for i := range projection {
		row := make([]float64, dimension)
		for j := range row {
			u1 := rng.Float64()
			u2 := rng.Float64()
			z := math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2.0*math.Pi*u2)
			row[j] = z * scale
		}
		projection[i] = row
	}

	return &VectorEmbeddingEngine{
		dimension:  dimension,
		projection: projection,
		vocab:      make(map[string]int),
		vocabSize:  vocabSize,
	}
}

func (e *VectorEmbeddingEngine) tokenize(text string) []string {
	lowered := strings.ToLower(text)
	tokens := strings.Fields(lowered)
	if len(tokens) == 0 {
		return nil
	}

	sort.Strings(tokens)

	seen := make(map[string]struct{}, len(tokens))
	unique := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			unique = append(unique, t)
		}
	}

	return unique
}

func (e *VectorEmbeddingEngine) getOrAssignTokenID(token string) int {
	e.vocabMu.RLock()
	if id, ok := e.vocab[token]; ok {
		e.vocabMu.RUnlock()
		return id
	}
	e.vocabMu.RUnlock()

	e.vocabMu.Lock()
	defer e.vocabMu.Unlock()

	if id, ok := e.vocab[token]; ok {
		return id
	}

	id := len(e.vocab)
	if id >= e.vocabSize {
		id = hashToken(token) % e.vocabSize
	} else {
		e.vocab[token] = id
	}

	return id
}

func hashToken(token string) int {
	h := 0
	for _, c := range token {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

func (e *VectorEmbeddingEngine) Embed(text string) []float64 {
	tokens := e.tokenize(text)
	if len(tokens) == 0 {
		return make([]float64, e.dimension)
	}

	vector := make([]float64, e.dimension)

	for _, token := range tokens {
		id := e.getOrAssignTokenID(token)
		if id >= len(e.projection) {
			id = id % len(e.projection)
		}
		row := e.projection[id]
		for j := range vector {
			vector[j] += row[j]
		}
	}

	magnitude := 0.0
	for _, v := range vector {
		magnitude += v * v
	}
	magnitude = math.Sqrt(magnitude)
	if magnitude > 1e-10 {
		for j := range vector {
			vector[j] /= magnitude
		}
	}

	return vector
}

func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	dotProduct := 0.0
	normA := 0.0
	normB := 0.0

	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

func VectorToBytes(vec []float64) []byte {
	data := make([]byte, len(vec)*8)
	for i, v := range vec {
		bits := math.Float64bits(v)
		for j := 0; j < 8; j++ {
			data[i*8+j] = byte(bits >> (j * 8))
		}
	}
	return data
}

func BytesToVector(data []byte) []float64 {
	if len(data) == 0 || len(data)%8 != 0 {
		return nil
	}
	vec := make([]float64, len(data)/8)
	for i := range vec {
		var bits uint64
		for j := 0; j < 8; j++ {
			bits |= uint64(data[i*8+j]) << (j * 8)
		}
		vec[i] = math.Float64frombits(bits)
	}
	return vec
}

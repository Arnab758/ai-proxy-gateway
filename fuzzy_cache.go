package main

import (
	"strings"
)

// ============================================================================
// Typo tolerance and fuzzy matching for better cache hits
// ============================================================================

// levenshteinDistance calculates edit distance between two strings
func levenshteinDistance(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
		matrix[i][0] = i
	}
	for j := range matrix[0] {
		matrix[0][j] = j
	}

	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			if s1[i-1] == s2[j-1] {
				matrix[i][j] = matrix[i-1][j-1]
			} else {
				matrix[i][j] = min3(
					matrix[i-1][j]+1,
					matrix[i][j-1]+1,
					matrix[i-1][j-1]+1,
				)
			}
		}
	}

	return matrix[len(s1)][len(s2)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// isTypo checks if two words are similar (typo tolerance)
func isTypo(w1, w2 string, maxDistance int) bool {
	if w1 == w2 {
		return true
	}
	if len(w1) < 3 || len(w2) < 3 {
		return false // Don't check typos for short words
	}
	if abs(len(w1)-len(w2)) > maxDistance {
		return false
	}
	return levenshteinDistance(w1, w2) <= maxDistance
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// stemWord applies simple stemming (remove common suffixes)
func stemWord(word string) string {
	if len(word) < 4 {
		return word
	}

	// Common English suffixes
	suffixes := []string{"ing", "tion", "ment", "ness", "able", "ible", "less", "ous", "ive", "ed", "er", "ly"}

	for _, suffix := range suffixes {
		if strings.HasSuffix(word, suffix) && len(word)-len(suffix) >= 3 {
			return word[:len(word)-len(suffix)]
		}
	}

	return word
}

// ngramSimilarity calculates similarity using character n-grams
func ngramSimilarity(s1, s2 string, n int) float64 {
	if len(s1) < n || len(s2) < n {
		return 0.0
	}

	// Generate n-grams
	ngrams1 := make(map[string]int)
	ngrams2 := make(map[string]int)

	for i := 0; i <= len(s1)-n; i++ {
		gram := s1[i : i+n]
		ngrams1[gram]++
	}

	for i := 0; i <= len(s2)-n; i++ {
		gram := s2[i : i+n]
		ngrams2[gram]++
	}

	// Calculate intersection
	intersection := 0
	for gram, count1 := range ngrams1 {
		if count2, ok := ngrams2[gram]; ok {
			intersection += min(count1, count2)
		}
	}

	// Calculate union
	union := len(ngrams1) + len(ngrams2) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}

// wordOverlapScore calculates how many words match between two strings
func wordOverlapScore(query, target string) float64 {
	queryWords := strings.Fields(query)
	targetWords := strings.Fields(target)

	if len(queryWords) == 0 || len(targetWords) == 0 {
		return 0.0
	}

	// Create lookup map for target words (with stemming)
	targetMap := make(map[string]int)
	for _, w := range targetWords {
		stemmed := stemWord(strings.ToLower(w))
		targetMap[stemmed]++
	}

	matches := 0
	for _, qw := range queryWords {
		qwLower := stemWord(strings.ToLower(qw))
		if count, ok := targetMap[qwLower]; ok && count > 0 {
			matches++
			targetMap[qwLower]--
		}
	}

	// Calculate overlap as percentage of smaller set
	total := len(queryWords)
	if len(targetWords) < total {
		total = len(targetWords)
	}
	if total == 0 {
		return 0.0
	}

	return float64(matches) / float64(total)
}

// fuzzyMatch checks if query matches target with typo tolerance
func fuzzyMatch(query, target string, typoThreshold int) (bool, float64) {
	queryWords := strings.Fields(query)
	targetWords := strings.Fields(target)

	if len(queryWords) == 0 || len(targetWords) == 0 {
		return false, 0.0
	}

	// Create lookup for target words (with stemming)
	targetLower := make([]string, len(targetWords))
	for i, w := range targetWords {
		targetLower[i] = stemWord(strings.ToLower(w))
	}

	matched := 0
	total := len(queryWords)

	for _, qw := range queryWords {
		qwLower := stemWord(strings.ToLower(qw))
		found := false

		// Exact match first
		for _, tw := range targetLower {
			if qwLower == tw {
				found = true
				break
			}
		}

		// Try typo match if no exact match
		if !found && typoThreshold > 0 {
			for _, tw := range targetLower {
				if isTypo(qwLower, tw, typoThreshold) {
					found = true
					break
				}
			}
		}

		// Try n-gram match as last resort
		if !found && len(qwLower) >= 3 {
			for _, tw := range targetLower {
				if len(tw) >= 3 {
					ngramScore := ngramSimilarity(qwLower, tw, 2)
					if ngramScore >= 0.6 {
						found = true
						break
					}
				}
			}
		}

		if found {
			matched++
		}
	}

	score := float64(matched) / float64(total)
	return score >= 0.6, score // Lowered from 0.7 to 0.6 for better recall
}

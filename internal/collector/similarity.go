package collector

import (
	"strings"
	"unicode"
)

// keywordScorer computes relevance scores based on keyword matching.
// It uses a combination of exact match, partial match, and frequency weighting.
type keywordScorer struct{}

// NewKeywordScorer creates a new keyword-based similarity scorer.
func NewKeywordScorer() SimilarityScorer {
	return &keywordScorer{}
}

// Score computes a relevance score between keywords and the combined title+body text.
// The scoring algorithm:
//   - Exact keyword match: 1.0 weight
//   - Partial/substring match: 0.5 weight
//   - Multiple occurrences boost the score (capped at 2x)
//   - Final score is normalized to [0.0, 1.0]
func (s *keywordScorer) Score(keywords []string, title, body string) float64 {
	if len(keywords) == 0 {
		return 0
	}

	text := strings.ToLower(title + " " + body)
	text = normalizeText(text)

	var totalScore float64
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		normalizedKW := normalizeText(kw)

		// Exact match (whole word)
		exactCount := countOccurrences(text, normalizedKW)

		// Partial match
		partialScore := 0.0
		if exactCount == 0 {
			if strings.Contains(text, normalizedKW) {
				partialScore = 0.5
			} else {
				// Check individual words in the keyword
				kwWords := strings.Fields(normalizedKW)
				matched := 0
				for _, w := range kwWords {
					if len(w) > 1 && strings.Contains(text, w) {
						matched++
					}
				}
				if len(kwWords) > 0 {
					partialScore = 0.3 * float64(matched) / float64(len(kwWords))
				}
			}
		}

		// Combine exact and partial scores
		keywordScore := float64(exactCount)*1.0 + partialScore
		if keywordScore > 2.0 {
			keywordScore = 2.0 // Cap per-keyword contribution
		}
		totalScore += keywordScore
	}

	// Normalize: divide by keyword count and cap at 1.0
	result := totalScore / float64(len(keywords))
	if result > 1.0 {
		result = 1.0
	}
	return result
}

// countOccurrences counts how many times a word appears as a whole-word match.
func countOccurrences(text, word string) int {
	count := 0
	for _, w := range strings.Fields(text) {
		if w == word {
			count++
		}
	}
	return count
}

// normalizeText removes punctuation and extra whitespace for better matching.
func normalizeText(s string) string {
	var builder strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			builder.WriteRune(r)
		} else {
			builder.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// ExtractKeywords extracts meaningful keywords from topic and key points text.
// It filters out common Chinese and English stop words and returns unique keywords.
func ExtractKeywords(topic, keyPoints string) []string {
	// Common Chinese and English stop words
	stopWords := map[string]bool{
		"的": true, "是": true, "在": true, "了": true, "和": true,
		"与": true, "或": true, "对": true, "等": true, "及": true,
		"其": true, "为": true, "以": true, "而": true, "但": true,
		"从": true, "到": true, "被": true, "把": true, "让": true,
		"这": true, "那": true, "该": true, "都": true, "也": true,
		"就": true, "要": true, "会": true, "能": true,
		"一个": true, "一种": true, "一些": true,
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "to": true,
		"of": true, "in": true, "for": true, "on": true, "with": true,
		"at": true, "by": true, "from": true, "as": true, "it": true,
		"its": true, "and": true, "or": true, "not": true, "but": true,
	}

	text := topic + " " + keyPoints
	words := strings.Fields(text)

	seen := make(map[string]bool)
	var keywords []string
	// Define punctuation chars to trim
	punctuation := "，。！？、；：「」『』（）《》…—·,#.!?;:()" +
		"“”‘’" // curly quotes

	for _, w := range words {
		w = strings.TrimSpace(w)
		// Skip stop words, short words, pure numbers, and pure punctuation
		if len(w) < 2 || stopWords[strings.ToLower(w)] || isAllDigits(w) {
			continue
		}
		cleaned := strings.Trim(w, punctuation)
		if len(cleaned) < 2 || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		keywords = append(keywords, cleaned)
	}

	// Limit to max 10 keywords
	if len(keywords) > 10 {
		keywords = keywords[:10]
	}
	return keywords
}

// isAllDigits checks if a string consists entirely of digit characters.
func isAllDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(s) > 0
}

// Package collector provides content collection capabilities from external platforms.
// It searches for similar/relevant content on Zhihu and Xiaohongshu based on keywords
// extracted from AI-generated drafts, enabling competitive research and trend analysis.
package collector

import (
	"context"

	"auto-publisher/internal/model"
)

// Collector defines the interface for collecting similar content from a platform.
type Collector interface {
	// Name returns the collector identifier.
	Name() string

	// Platform returns the target platform for this collector.
	Platform() model.Platform

	// Search finds content similar to the given keywords on the target platform.
	// maxResults limits the number of results returned (0 means default, typically 10).
	Search(ctx context.Context, keywords []string, maxResults int) ([]model.CollectedContent, error)

	// IsAvailable checks whether the collector can connect to the platform.
	IsAvailable(ctx context.Context) bool
}

// SimilarityScorer computes relevance scores between search keywords and content.
type SimilarityScorer interface {
	// Score computes a relevance score (0.0 to 1.0) between keywords and content text.
	Score(keywords []string, title, body string) float64
}

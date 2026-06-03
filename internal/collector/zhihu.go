package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"auto-publisher/internal/model"
)

// ZhihuCollector searches for similar content on Zhihu (知乎) via HTTP API.
type ZhihuCollector struct {
	client    *http.Client
	cookieStr string
	userAgent string
	scorer    SimilarityScorer
}

// NewZhihuCollector creates a new Zhihu content collector.
// cookieStr is a valid Zhihu login cookie string (can be empty for limited search).
func NewZhihuCollector(cookieStr string) *ZhihuCollector {
	return &ZhihuCollector{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		cookieStr: cookieStr,
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		scorer:    NewKeywordScorer(),
	}
}

// Name returns the collector name.
func (c *ZhihuCollector) Name() string {
	return "zhihu-collector"
}

// Platform returns the target platform.
func (c *ZhihuCollector) Platform() model.Platform {
	return model.PlatformZhihu
}

// IsAvailable checks if the Zhihu API is reachable.
func (c *ZhihuCollector) IsAvailable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.zhihu.com/api/v4/search/preset_words", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("zhihu collector not available", "error", err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// Search finds Zhihu content matching the given keywords.
func (c *ZhihuCollector) Search(ctx context.Context, keywords []string, maxResults int) ([]model.CollectedContent, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	query := strings.Join(keywords, " ")
	slog.Info("searching zhihu", "query", query, "max_results", maxResults)

	searchURL := fmt.Sprintf(
		"https://www.zhihu.com/api/v4/search_v3?t=general&q=%s&limit=%d&offset=0",
		url.QueryEscape(query), maxResults,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.cookieStr != "" {
		req.Header.Set("Cookie", c.cookieStr)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zhihu search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("zhihu search API error (HTTP %d): %s",
			resp.StatusCode, truncateStr(string(body), 200))
	}

	var searchResult zhihuSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}

	// Convert search results to collected content with relevance scoring
	var results []model.CollectedContent
	for _, item := range searchResult.Data {
		obj := item.Object
		if obj.Type != "answer" && obj.Type != "article" {
			continue
		}

		title := obj.Title
		if title == "" {
			title = obj.Question.Name
		}

		// Extract body text
		body := ""
		if obj.Type == "answer" {
			body = obj.Excerpt
		} else {
			body = obj.Excerpt
		}

		score := c.scorer.Score(keywords, title, body)
		if score < 0.1 {
			continue // Skip irrelevant results
		}

		results = append(results, model.CollectedContent{
			Platform:       model.PlatformZhihu,
			SourceURL:      obj.URL,
			Title:          title,
			Author:         obj.Author.Name,
			Summary:        body,
			Keywords:       strings.Join(keywords, ","),
			RelevanceScore: score,
			Likes:          obj.VoteCount,
			Comments:       obj.CommentCount,
			SyncedAt:       time.Now(),
		})
	}

	slog.Info("zhihu search complete", "found", len(results), "query", query)
	return results, nil
}

// zhihuSearchResponse represents the Zhihu search API response.
type zhihuSearchResponse struct {
	Data []zhihuSearchItem `json:"data"`
}

type zhihuSearchItem struct {
	Object zhihuSearchObject `json:"object"`
}

type zhihuSearchObject struct {
	Type         string          `json:"type"`
	Title        string          `json:"title"`
	URL          string          `json:"url"`
	Excerpt      string          `json:"excerpt"`
	VoteCount    int             `json:"voteup_count"`
	CommentCount int             `json:"comment_count"`
	Author       zhihuAuthor     `json:"author"`
	Question     zhihuQuestion   `json:"question"`
}

type zhihuAuthor struct {
	Name string `json:"name"`
}

type zhihuQuestion struct {
	Name string `json:"name"`
}

// truncateStr truncates a string to maxLen characters, appending "..." if needed.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

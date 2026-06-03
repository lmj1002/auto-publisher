package handler

import (
	"fmt"
	"log/slog"
	"net/http"

	"auto-publisher/internal/collector"
	"auto-publisher/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CollectionHandler handles content collection (scraping similar content) API requests.
type CollectionHandler struct {
	db         *gorm.DB
	collectors map[string]collector.Collector
}

// NewCollectionHandler creates a new CollectionHandler.
func NewCollectionHandler(db *gorm.DB, collectors map[string]collector.Collector) *CollectionHandler {
	return &CollectionHandler{
		db:         db,
		collectors: collectors,
	}
}

// Collect triggers content collection for a given content ID.
// It extracts keywords from the content, searches both platforms, saves results,
// and returns the collected items.
//
// POST /api/collect
//
//	{
//	  "content_id": 1,
//	  "platform": "zhihu",     // optional: limit to one platform
//	  "max_results": 10        // optional: default 10
//	}
func (h *CollectionHandler) Collect(c *gin.Context) {
	var req model.CollectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid request: provide content_id"})
		return
	}

	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}

	// Load source content to extract keywords
	var content model.Content
	if err := h.db.First(&content, req.ContentID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "msg": "content not found"})
		return
	}

	// Extract keywords from the content
	keywords := collector.ExtractKeywords(content.Topic, content.KeyPoints)
	if len(keywords) == 0 {
		// Fallback: use topic words
		keywords = []string{content.Topic}
	}
	slog.Info("collection keywords extracted",
		"content_id", req.ContentID,
		"keywords", keywords,
	)

	// Determine which platforms to search
	platforms := h.platformsToSearch(req.Platform)

	// Search each platform
	var allResults []model.CollectedContent
	for _, platform := range platforms {
		col, ok := h.collectors[platform]
		if !ok {
			slog.Warn("collector not available", "platform", platform)
			continue
		}

		results, err := col.Search(c.Request.Context(), keywords, req.MaxResults)
		if err != nil {
			slog.Warn("collection search failed",
				"platform", platform,
				"error", err,
			)
			continue
		}

		// Set source content ID and save to DB
		for _, r := range results {
			r.SourceContentID = req.ContentID
			if err := h.db.Create(&r).Error; err != nil {
				slog.Warn("save collected content failed", "error", err)
				continue
			}
			allResults = append(allResults, r)
		}
	}

	if len(allResults) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"msg":  "no similar content found",
			"data": model.CollectResponse{
				ContentID: req.ContentID,
				Results:   []model.CollectedContent{},
				Total:     0,
				Keywords:  keywords,
			},
		})
		return
	}

	// Sort by relevance score descending
	sortByRelevance(allResults)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "collection complete",
		"data": model.CollectResponse{
			ContentID: req.ContentID,
			Results:   allResults,
			Total:     len(allResults),
			Keywords:  keywords,
		},
	})
}

// List returns previously collected content, optionally filtered by source content ID.
//
// GET /api/collected?content_id=1&platform=zhihu&page=1&page_size=20
func (h *CollectionHandler) List(c *gin.Context) {
	contentID := c.Query("content_id")
	platform := c.Query("platform")
	page := 1
	pageSize := 20

	if p, err := parseIntParam(c.Query("page")); err == nil && p > 0 {
		page = p
	}
	if ps, err := parseIntParam(c.Query("page_size")); err == nil && ps > 0 && ps <= 100 {
		pageSize = ps
	}

	tx := h.db.Model(&model.CollectedContent{})
	if contentID != "" {
		tx = tx.Where("source_content_id = ?", contentID)
	}
	if platform != "" {
		tx = tx.Where("platform = ?", platform)
	}

	var total int64
	tx.Count(&total)

	var results []model.CollectedContent
	tx.Offset((page - 1) * pageSize).
		Limit(pageSize).
		Order("relevance_score DESC, synced_at DESC").
		Find(&results)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"list":      results,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// platformsToSearch returns the list of platforms to search based on the request filter.
func (h *CollectionHandler) platformsToSearch(filter model.Platform) []string {
	if filter == "" || filter == "both" {
		// Search all available platforms
		var platforms []string
		for name := range h.collectors {
			platforms = append(platforms, name)
		}
		return platforms
	}
	// Search specific platform if available
	if _, ok := h.collectors[string(filter)]; ok {
		return []string{string(filter)}
	}
	return nil
}

// sortByRelevance sorts collected content by relevance score in descending order.
func sortByRelevance(items []model.CollectedContent) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].RelevanceScore > items[i].RelevanceScore {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

// parseIntParam parses a string to an integer, returning an error if invalid.
func parseIntParam(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid integer: %s", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

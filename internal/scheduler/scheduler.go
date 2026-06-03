// Package scheduler provides a lightweight, timer-based publication scheduler.
// It scans the database for scheduled content and dispatches it to the appropriate
// platform publisher with configurable retry logic.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"auto-publisher/internal/model"
	"auto-publisher/internal/publisher"

	"gorm.io/gorm"
)

// Scheduler periodically scans for scheduled content and triggers publishing.
type Scheduler struct {
	db         *gorm.DB
	publishers map[string]publisher.Publisher
	interval   time.Duration
	maxRetries int
	retryDelay time.Duration
	timezone   *time.Location

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New creates a new Scheduler with the given configuration.
// timezone should be an IANA timezone name (e.g., "Asia/Shanghai").
func New(db *gorm.DB, interval time.Duration, maxRetries int, retryDelay time.Duration, timezone string) (*Scheduler, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.Local
	}

	return &Scheduler{
		db:         db,
		publishers: make(map[string]publisher.Publisher),
		interval:   interval,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		timezone:   loc,
		stopCh:     make(chan struct{}),
	}, nil
}

// Register adds a publisher to the scheduler by name.
func (s *Scheduler) Register(name string, p publisher.Publisher) {
	s.publishers[name] = p
	slog.Info("publisher registered with scheduler", "name", name, "platform", p.Platform())
}

// Start begins the scheduling loop in a background goroutine.
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.loop()
	slog.Info("scheduler started", "interval", s.interval, "timezone", s.timezone)
}

// Stop gracefully stops the scheduling loop.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	slog.Info("scheduler stopped")
}

// loop is the main scheduling loop.
func (s *Scheduler) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run immediately on start
	s.scanAndPublish()

	for {
		select {
		case <-ticker.C:
			s.scanAndPublish()
		case <-s.stopCh:
			return
		}
	}
}

// scanAndPublish finds all due content and publishes it.
func (s *Scheduler) scanAndPublish() {
	now := time.Now().In(s.timezone)

	var contents []model.Content
	err := s.db.Where(
		"status = ? AND scheduled_at <= ?",
		model.StatusScheduled, now,
	).Order("scheduled_at ASC").Find(&contents).Error

	if err != nil {
		slog.Error("scan scheduled content failed", "error", err)
		return
	}

	if len(contents) == 0 {
		return
	}

	slog.Info("publishable content found", "count", len(contents))

	for _, content := range contents {
		s.publishContent(&content)
	}
}

// publishContent publishes a single content item with retry logic.
func (s *Scheduler) publishContent(content *model.Content) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	slog.Info("publishing content",
		"id", content.ID,
		"topic", content.Topic,
		"platform", content.Platform,
	)

	// Select the appropriate publisher
	pub := s.selectPublisher(content.Platform)
	if pub == nil {
		s.markFailed(content, "no available publisher")
		return
	}

	// Retry loop
	var lastErr error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if attempt > 0 {
			slog.Info("retrying publish",
				"content_id", content.ID,
				"attempt", attempt,
				"max_retries", s.maxRetries,
			)
			time.Sleep(s.retryDelay)
		}

		result, err := pub.Publish(ctx, content)
		if err == nil && result.Success {
			s.markPublished(content, result)
			return
		}
		lastErr = err
		if result != nil {
			lastErr = fmt.Errorf("%s", result.Message)
		}
	}

	s.markFailed(content, fmt.Sprintf("publish failed after %d retries: %v", s.maxRetries, lastErr))
}

// selectPublisher selects the appropriate publisher for the given platform.
func (s *Scheduler) selectPublisher(platform model.Platform) publisher.Publisher {
	switch platform {
	case model.PlatformZhihu, model.PlatformBoth:
		if p, ok := s.publishers["zhihu"]; ok {
			return p
		}
	case model.PlatformXiaohongshu:
		if p, ok := s.publishers["xiaohongshu"]; ok {
			return p
		}
	}
	return nil
}

// markPublished marks content as successfully published.
func (s *Scheduler) markPublished(content *model.Content, result *publisher.PublishResult) {
	now := time.Now()
	updates := map[string]any{
		"status":        model.StatusPublished,
		"published_at":  now,
		"publish_error": nil,
		"retry_count":   0,
	}

	if err := s.db.Model(content).Updates(updates).Error; err != nil {
		slog.Error("mark published failed", "content_id", content.ID, "error", err)
		return
	}
	slog.Info("publish succeeded", "content_id", content.ID, "url", result.URL)
}

// markFailed marks content as failed or keeps it scheduled for retry.
func (s *Scheduler) markFailed(content *model.Content, errMsg string) {
	retryCount := content.RetryCount + 1
	status := model.StatusFailed
	if retryCount <= s.maxRetries {
		status = model.StatusScheduled // keep scheduled for next scan
	}

	updates := map[string]any{
		"status":        status,
		"publish_error": errMsg,
		"retry_count":   retryCount,
	}

	if err := s.db.Model(content).Updates(updates).Error; err != nil {
		slog.Error("mark failed update error", "content_id", content.ID, "error", err)
	}
	slog.Error("publish failed",
		"content_id", content.ID,
		"error", errMsg,
		"retry", fmt.Sprintf("%d/%d", retryCount, s.maxRetries),
	)
}

// PublishNow immediately triggers publishing for a content item (async).
func (s *Scheduler) PublishNow(contentID int64) error {
	var content model.Content
	if err := s.db.First(&content, contentID).Error; err != nil {
		return fmt.Errorf("content not found: %w", err)
	}

	if content.Status != model.StatusApproved && content.Status != model.StatusScheduled {
		return fmt.Errorf("content status %q cannot be published", content.Status)
	}

	go s.publishContent(&content)
	return nil
}

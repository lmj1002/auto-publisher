// Package scheduler provides a lightweight, timer-based publication scheduler.
//
// Architecture:
//
//	Content (1) → (N) Schedule → (N) PublishTask → (N) PublishLog
//
// Each Schedule creates one PublishTask per target platform. PublishTasks are
// independently retried — a failed zhihu task does not trigger a re-publish of
// an already-successful xiaohongshu task in "both" mode.
//
// The scheduler scans for Schedules with actionable PublishTasks, processes them
// with optimistic locking, and aggregates task results back into Schedule/Content status.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"auto-publisher/internal/model"
	"auto-publisher/internal/publisher"

	"gorm.io/gorm"
)

// Scheduler periodically scans for actionable PublishTasks and triggers publishing.
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

// scanAndPublish finds all schedules with actionable PublishTasks and processes them.
func (s *Scheduler) scanAndPublish() {
	now := time.Now().In(s.timezone)

	// Find schedules that may need processing.
	// We preload all PublishTasks and filter in Go — data volumes are small for a
	// personal publishing tool, and the Go-level filter is easier to get right than
	// a complex multi-condition SQL join.
	var schedules []model.Schedule
	err := s.db.Where("status IN ?", []model.ScheduleStatus{
		model.SchedulePending, model.ScheduleProcessing,
	}).Preload("PublishTasks").Order("scheduled_at ASC").Find(&schedules).Error

	if err != nil {
		slog.Error("scan schedules failed", "error", err)
		return
	}

	var actionable []model.Schedule
	for _, sch := range schedules {
		if hasActionableTasks(sch.PublishTasks, now) {
			actionable = append(actionable, sch)
		}
	}

	if len(actionable) == 0 {
		return
	}

	slog.Info("actionable schedules found", "count", len(actionable))

	for _, sch := range actionable {
		s.processSchedule(&sch)
	}
}

// hasActionableTasks returns true if any PublishTask in the schedule needs processing now.
func hasActionableTasks(tasks []model.PublishTask, now time.Time) bool {
	for _, t := range tasks {
		if isTaskActionable(&t, now) {
			return true
		}
	}
	return false
}

// isTaskActionable returns true when a PublishTask should be picked up for processing.
func isTaskActionable(t *model.PublishTask, now time.Time) bool {
	switch t.Status {
	case model.TaskPending:
		return true
	case model.TaskFailed:
		// Retry due?
		return t.NextRetryAt != nil && !t.NextRetryAt.After(now)
	case model.TaskPublishing:
		// Stuck from a previous crash — attempt recovery
		return true
	default:
		return false
	}
}

// processSchedule claims the schedule and processes each actionable PublishTask.
func (s *Scheduler) processSchedule(schedule *model.Schedule) {
	// Optimistic lock: claim the schedule so no other goroutine processes it
	result := s.db.Model(schedule).Where("status IN ?", []model.ScheduleStatus{
		model.SchedulePending, model.ScheduleProcessing,
	}).Updates(map[string]any{"status": model.ScheduleProcessing})
	if result.RowsAffected == 0 {
		return // already claimed by another goroutine
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("scheduler panic recovered during schedule processing",
				"schedule_id", schedule.ID, "panic", r)
		}
	}()

	now := time.Now().In(s.timezone)

	// Reload PublishTasks for this schedule
	var tasks []model.PublishTask
	s.db.Where("schedule_id = ?", schedule.ID).Find(&tasks)

	// Process each actionable task
	for i := range tasks {
		task := &tasks[i]
		if !isTaskActionable(task, now) {
			continue
		}
		s.processTask(task, schedule)
	}

	// Reload tasks to get the latest state and aggregate
	var updated []model.PublishTask
	s.db.Where("schedule_id = ?", schedule.ID).Find(&updated)

	s.aggregateAndUpdate(schedule, updated)
}

// processTask processes a single PublishTask: claims it with optimistic lock,
// executes the publish, and updates the task state. Does NOT update Schedule/Content.
func (s *Scheduler) processTask(task *model.PublishTask, schedule *model.Schedule) {
	// Optimistic lock: only claim if task is in a pre-actionable state
	result := s.db.Model(task).Where("status IN ?", []model.PublishTaskStatus{
		model.TaskPending, model.TaskFailed, model.TaskPublishing,
	}).Updates(map[string]any{
		"status": model.TaskPublishing,
	})
	if result.RowsAffected == 0 {
		return // already claimed
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("publisher panicked during task processing",
				"task_id", task.ID, "platform", task.Platform, "panic", r)
			s.markTaskRetry(task, fmt.Sprintf("publisher panic: %v", r))
		}
	}()

	// Load content
	var content model.Content
	if err := s.db.First(&content, schedule.ContentID).Error; err != nil {
		s.markTaskTerminal(task, "content not found: "+err.Error())
		return
	}

	// Get publisher
	pub := s.selectPublisher(model.Platform(task.Platform))
	if pub == nil {
		s.markTaskTerminal(task,
			fmt.Sprintf("no publisher for %s — set username/password in config.yaml or %s env var",
				task.Platform, platformToEnvVar(model.Platform(task.Platform))))
		return
	}

	// Execute publish
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	startTime := time.Now()
	pubResult, pubErr := s.safePublish(ctx, pub, &content)
	durationMs := time.Since(startTime).Milliseconds()

	attempt := task.RetryCount + 1

	if pubErr == nil && pubResult != nil && pubResult.Success {
		// SUCCESS
		slog.Info("publish task succeeded",
			"task_id", task.ID,
			"platform", task.Platform,
			"content_id", content.ID,
			"duration_ms", durationMs,
		)
		now := time.Now()
		s.db.Model(task).Updates(map[string]any{
			"status":        model.TaskPublished,
			"published_at":  now,
			"publish_error": gorm.Expr("NULL"),
			"external_id":   pubResult.ContentID,
			"external_url":  pubResult.URL,
		})
		s.writePublishLog(task, schedule, string(task.Platform), "success",
			attempt, durationMs, pubResult.Message, pubResult.URL, pubResult.Screenshot)
	} else {
		// FAILURE
		errMsg := buildErrorDetail(pubErr, pubResult)
		slog.Error("publish task failed",
			"task_id", task.ID,
			"platform", task.Platform,
			"content_id", content.ID,
			"error", errMsg,
		)
		s.markTaskRetry(task, errMsg)
		s.writePublishLog(task, schedule, string(task.Platform), "failed",
			attempt, durationMs, errMsg, "", "")
	}
}

// markTaskRetry increments retry_count and either schedules a retry (next_retry_at)
// or marks as terminal (next_retry_at = NULL) when max retries are exhausted.
func (s *Scheduler) markTaskRetry(task *model.PublishTask, errMsg string) {
	retryCount := task.RetryCount + 1

	updates := map[string]any{
		"status":        model.TaskFailed,
		"retry_count":   retryCount,
		"publish_error": errMsg,
	}

	if retryCount < s.maxRetries {
		nextRetry := time.Now().In(s.timezone).Add(s.retryDelay)
		updates["next_retry_at"] = nextRetry
		slog.Warn("task will retry",
			"task_id", task.ID,
			"platform", task.Platform,
			"retry", fmt.Sprintf("%d/%d", retryCount, s.maxRetries),
			"next_retry", nextRetry.Format(time.RFC3339),
		)
	} else {
		updates["next_retry_at"] = nil // terminal
		slog.Error("task retries exhausted",
			"task_id", task.ID,
			"platform", task.Platform,
			"retry", fmt.Sprintf("%d/%d", retryCount, s.maxRetries),
		)
	}

	if err := s.db.Model(task).Updates(updates).Error; err != nil {
		slog.Error("update task retry state failed", "task_id", task.ID, "error", err)
	}
}

// markTaskTerminal permanently marks a task as failed with no retry.
// Used for unrecoverable errors (content not found, no publisher, etc.).
func (s *Scheduler) markTaskTerminal(task *model.PublishTask, errMsg string) {
	slog.Error("task marked terminal",
		"task_id", task.ID,
		"platform", task.Platform,
		"error", errMsg,
	)
	s.db.Model(task).Updates(map[string]any{
		"status":        model.TaskFailed,
		"publish_error": errMsg,
		"next_retry_at": nil,
	})
	// Also write a log entry
	var schedule model.Schedule
	s.db.First(&schedule, task.ScheduleID)
	s.writePublishLog(task, &schedule, task.Platform, "failed",
		task.RetryCount+1, 0, errMsg, "", "")
}

// aggregateAndUpdate inspects all PublishTasks for a schedule, computes the
// aggregate Schedule/Content status, and persists the results.
func (s *Scheduler) aggregateAndUpdate(schedule *model.Schedule, tasks []model.PublishTask) {
	var (
		total    = len(tasks)
		success  = 0
		terminal = 0
		pending  = 0
	)

	for _, t := range tasks {
		switch {
		case t.Status == model.TaskPublished:
			success++
		case t.IsTerminalFailed():
			terminal++
		default:
			// TaskPending, TaskPublishing, or TaskFailed with retries remaining
			pending++
		}
	}

	if total == 0 {
		return
	}

	var scheduleStatus model.ScheduleStatus
	var contentStatus model.ContentStatus

	switch {
	case success == total:
		scheduleStatus = model.ScheduleCompleted
		contentStatus = model.StatusPublished

	case terminal == total:
		scheduleStatus = model.ScheduleFailed
		contentStatus = model.StatusFailed

	case success > 0 && terminal > 0 && pending == 0:
		scheduleStatus = model.SchedulePartiallyCompleted
		contentStatus = model.StatusPartiallyPublished

	default:
		// Still have pending/retrying tasks
		scheduleStatus = model.ScheduleProcessing
		contentStatus = model.StatusScheduled
	}

	// Update Schedule status
	if err := s.db.Model(schedule).Update("status", scheduleStatus).Error; err != nil {
		slog.Error("update schedule status failed", "schedule_id", schedule.ID, "error", err)
	}

	// Update Content status (and published_at when fully published)
	contentUpdates := map[string]any{"status": contentStatus}
	if contentStatus == model.StatusPublished {
		contentUpdates["published_at"] = time.Now()
	}
	if err := s.db.Model(&model.Content{}).Where("id = ?", schedule.ContentID).
		Updates(contentUpdates).Error; err != nil {
		slog.Error("update content status failed", "content_id", schedule.ContentID, "error", err)
	}

	slog.Info("schedule aggregation complete",
		"schedule_id", schedule.ID,
		"schedule_status", scheduleStatus,
		"content_status", contentStatus,
		"tasks_total", total,
		"tasks_success", success,
		"tasks_terminal", terminal,
		"tasks_pending", pending,
	)
}

// ==================== PublishNow (立即发布) ====================

// PublishNow immediately publishes content by creating a Schedule + PublishTasks
// and dispatching asynchronously.
func (s *Scheduler) PublishNow(contentID int64) error {
	var content model.Content
	if err := s.db.First(&content, contentID).Error; err != nil {
		return fmt.Errorf("content not found: %w", err)
	}

	if !canPublish(content.Status) {
		return fmt.Errorf("content status %q cannot be published (need approved/scheduled/failed/partially_published)",
			content.Status)
	}

	if missing := s.MissingPublishers(content.Platform); len(missing) > 0 {
		return fmt.Errorf("platform [%s] has no publisher configured", strings.Join(missing, ", "))
	}

	// Create Schedule + PublishTasks in a transaction
	now := time.Now()
	schedule := &model.Schedule{
		ContentID:   content.ID,
		ScheduledAt: &now,
		Status:      model.SchedulePending,
	}

	tx := s.db.Begin()
	if err := tx.Create(schedule).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("create schedule: %w", err)
	}

	tasks := makePublishTasks(content.ID, schedule.ID, content.Platform)
	for _, task := range tasks {
		if err := tx.Create(task).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("create publish task: %w", err)
		}
	}

	if err := tx.Model(&content).Update("status", model.StatusScheduled).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("update content status: %w", err)
	}
	tx.Commit()

	// Dispatch asynchronously
	schedule.Content = &content
	schedule.PublishTasks = make([]model.PublishTask, len(tasks))
	for i, t := range tasks {
		schedule.PublishTasks[i] = *t
	}
	go s.processSchedule(schedule)

	return nil
}

// makePublishTasks creates the appropriate PublishTasks for a content item.
// For platform=both, creates two tasks (zhihu + xiaohongshu).
func makePublishTasks(contentID, scheduleID int64, platform model.Platform) []*model.PublishTask {
	platforms := expandPlatforms(platform)
	tasks := make([]*model.PublishTask, 0, len(platforms))
	for _, p := range platforms {
		tasks = append(tasks, &model.PublishTask{
			ContentID:  contentID,
			ScheduleID: scheduleID,
			Platform:   string(p),
			Status:     model.TaskPending,
		})
	}
	return tasks
}

// expandPlatforms returns the individual platforms for a content target.
// PlatformBoth expands to [zhihu, xiaohongshu].
func expandPlatforms(platform model.Platform) []model.Platform {
	switch platform {
	case model.PlatformBoth:
		return []model.Platform{model.PlatformZhihu, model.PlatformXiaohongshu}
	default:
		return []model.Platform{platform}
	}
}

// canPublish returns true if the content is in a publishable state.
func canPublish(status model.ContentStatus) bool {
	switch status {
	case model.StatusApproved, model.StatusScheduled, model.StatusFailed,
		model.StatusPartiallyPublished:
		return true
	default:
		return false
	}
}

// ==================== Publisher helpers ====================

// safePublish wraps pub.Publish with panic recovery.
func (s *Scheduler) safePublish(ctx context.Context, pub publisher.Publisher, content *model.Content) (res *publisher.PublishResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("publisher crashed: %v", r)
			res = &publisher.PublishResult{
				Success:  false,
				Platform: string(pub.Platform()),
				Message:  fmt.Sprintf("browser automation error: %v", r),
			}
			slog.Error("publisher panicked (recovered)",
				"platform", pub.Platform(), "content_id", content.ID, "panic", r)
		}
	}()
	return pub.Publish(ctx, content)
}

// buildErrorDetail merges error and PublishResult.Message, avoiding duplicates.
func buildErrorDetail(err error, result *publisher.PublishResult) string {
	if err == nil && (result == nil || result.Message == "") {
		return "unknown error"
	}
	var parts []string
	if err != nil {
		parts = append(parts, err.Error())
	}
	if result != nil && result.Message != "" {
		msg := result.Message
		if err == nil || err.Error() != msg {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, " | ")
}

// ==================== Publisher selection ====================

// HasPublisherFor checks whether a publisher is registered for the given platform(s).
func (s *Scheduler) HasPublisherFor(platform model.Platform) bool {
	if platform == model.PlatformBoth {
		return s.selectPublisher(model.PlatformZhihu) != nil &&
			s.selectPublisher(model.PlatformXiaohongshu) != nil
	}
	return s.selectPublisher(platform) != nil
}

// MissingPublishers returns platform names that have no publisher configured.
func (s *Scheduler) MissingPublishers(platform model.Platform) []string {
	var missing []string
	if platform == model.PlatformBoth {
		if s.selectPublisher(model.PlatformZhihu) == nil {
			missing = append(missing, "zhihu")
		}
		if s.selectPublisher(model.PlatformXiaohongshu) == nil {
			missing = append(missing, "xiaohongshu")
		}
	} else {
		if s.selectPublisher(platform) == nil {
			missing = append(missing, string(platform))
		}
	}
	return missing
}

func (s *Scheduler) selectPublisher(platform model.Platform) publisher.Publisher {
	switch platform {
	case model.PlatformZhihu:
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

func platformToEnvVar(platform model.Platform) string {
	switch platform {
	case model.PlatformZhihu:
		return "ZHIHU_COOKIE"
	case model.PlatformXiaohongshu:
		return "XHS_COOKIE"
	default:
		return "COOKIE"
	}
}

// ==================== Logging ====================

// writePublishLog creates a PublishLog record linked to a PublishTask.
func (s *Scheduler) writePublishLog(task *model.PublishTask, schedule *model.Schedule,
	platform, status string, attempt int, durationMs int64,
	message, url, screenshot string) {

	log := model.PublishLog{
		ContentID:     schedule.ContentID,
		ScheduleID:    schedule.ID,
		PublishTaskID: task.ID,
		Platform:      platform,
		Status:        status,
		Attempt:       attempt,
		Message:       message,
		URL:           url,
		ErrorDetail:   message,
		DurationMs:    durationMs,
		Screenshot:    screenshot,
	}
	if status == "success" {
		log.ErrorDetail = ""
	}

	if err := s.db.Create(&log).Error; err != nil {
		slog.Error("write publish log failed",
			"content_id", schedule.ContentID,
			"schedule_id", schedule.ID,
			"task_id", task.ID,
			"error", err,
		)
	}
}

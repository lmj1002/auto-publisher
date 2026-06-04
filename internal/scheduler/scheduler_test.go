package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"auto-publisher/internal/model"
	"auto-publisher/internal/publisher"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// ==================== Test Helpers ====================

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Content{}, &model.Schedule{}, &model.PublishTask{}, &model.PublishLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func testContent(t *testing.T, db *gorm.DB, platform model.Platform, status model.ContentStatus) *model.Content {
	t.Helper()
	c := &model.Content{
		Topic:    "test topic",
		Platform: platform,
		Type:     model.TypeArticle,
		Status:   status,
		XHSTitle: "test xhs title",
		XHSBody:  "test xhs body",
		ZHTitle:  "test zh title",
		ZHBody:   "test zh body",
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("create test content: %v", err)
	}
	return c
}

func testScheduleWithTasks(t *testing.T, db *gorm.DB, content *model.Content, schStatus model.ScheduleStatus, taskStatus model.PublishTaskStatus) *model.Schedule {
	t.Helper()
	now := time.Now()
	s := &model.Schedule{
		ContentID:   content.ID,
		ScheduledAt: &now,
		Status:      schStatus,
	}
	if err := db.Create(s).Error; err != nil {
		t.Fatalf("create test schedule: %v", err)
	}

	platforms := expandPlatforms(content.Platform)
	for _, p := range platforms {
		task := &model.PublishTask{
			ContentID:  content.ID,
			ScheduleID: s.ID,
			Platform:   string(p),
			Status:     taskStatus,
		}
		if err := db.Create(task).Error; err != nil {
			t.Fatalf("create test publish task: %v", err)
		}
	}

	var reloaded model.Schedule
	db.Where("id = ?", s.ID).Preload("PublishTasks").First(&reloaded)
	var reloadedContent model.Content
	db.First(&reloadedContent, content.ID)
	reloaded.Content = &reloadedContent
	return &reloaded
}

type controllableMockPub struct {
	name     string
	platform publisher.Platform
	results  []*publisher.PublishResult
	errors   []error
	callIdx  int
}

func (m *controllableMockPub) Name() string                         { return m.name }
func (m *controllableMockPub) Platform() publisher.Platform         { return m.platform }
func (m *controllableMockPub) Validate(ctx context.Context) error   { return nil }
func (m *controllableMockPub) IsAvailable(ctx context.Context) bool { return true }
func (m *controllableMockPub) Publish(ctx context.Context, content *model.Content) (*publisher.PublishResult, error) {
	if m.callIdx >= len(m.results) {
		return &publisher.PublishResult{Success: true, Platform: string(m.platform), URL: "http://example.com/default", Message: "default success"}, nil
	}
	res := m.results[m.callIdx]
	var err error
	if m.callIdx < len(m.errors) {
		err = m.errors[m.callIdx]
	}
	m.callIdx++
	return res, err
}

func successResult() *publisher.PublishResult {
	return &publisher.PublishResult{Success: true, Platform: "test", URL: "http://example.com/success", Message: "published ok", ContentID: "test-123"}
}

func failResult() *publisher.PublishResult {
	return &publisher.PublishResult{Success: false, Platform: "test", Message: "network error"}
}

func failErr() error {
	return fmt.Errorf("network error")
}

// ==================== processTask ====================

func TestProcessTask_Success(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu, results: []*publisher.PublishResult{successResult()}})

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched.processTask(&task, schedule)

	var reloaded model.PublishTask
	db.First(&reloaded, task.ID)
	if reloaded.Status != model.TaskPublished {
		t.Errorf("expected task status 'published', got %q", reloaded.Status)
	}
	if reloaded.PublishedAt == nil {
		t.Error("expected published_at to be set")
	}

	var logs []model.PublishLog
	db.Where("publish_task_id = ?", task.ID).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Status != "success" {
		t.Errorf("expected log status 'success', got %q", logs[0].Status)
	}
}

func TestProcessTask_Failure_WithRetry(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: 10 * time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu,
		results: []*publisher.PublishResult{failResult()},
		errors:  []error{failErr()},
	})

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched.processTask(&task, schedule)

	var reloaded model.PublishTask
	db.First(&reloaded, task.ID)
	if reloaded.Status != model.TaskFailed {
		t.Errorf("expected task status 'failed', got %q", reloaded.Status)
	}
	if reloaded.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", reloaded.RetryCount)
	}
	if reloaded.NextRetryAt == nil {
		t.Error("expected next_retry_at to be set (not terminal)")
	}
}

func TestProcessTask_Failure_ExhaustedRetries(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	db.Model(&model.PublishTask{}).Where("schedule_id = ?", schedule.ID).Update("retry_count", 2)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu,
		results: []*publisher.PublishResult{failResult()},
		errors:  []error{failErr()},
	})

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched.processTask(&task, schedule)

	var reloaded model.PublishTask
	db.First(&reloaded, task.ID)
	if reloaded.Status != model.TaskFailed {
		t.Errorf("expected task status 'failed', got %q", reloaded.Status)
	}
	if reloaded.RetryCount != 3 {
		t.Errorf("expected retry_count 3, got %d", reloaded.RetryCount)
	}
	if reloaded.NextRetryAt != nil {
		t.Error("expected next_retry_at to be nil (terminal)")
	}
}

func TestProcessTask_NoPublisher(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: time.Minute, timezone: time.UTC}

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched.processTask(&task, schedule)

	var reloaded model.PublishTask
	db.First(&reloaded, task.ID)
	if reloaded.Status != model.TaskFailed {
		t.Errorf("expected task status 'failed', got %q", reloaded.Status)
	}
	if reloaded.NextRetryAt != nil {
		t.Error("expected terminal failure (no retry) when publisher missing")
	}
}

// ==================== PublishTask status helpers ====================

func TestPublishTask_IsTerminalFailed(t *testing.T) {
	task := &model.PublishTask{Status: model.TaskFailed, NextRetryAt: nil}
	if !task.IsTerminalFailed() {
		t.Error("expected terminal failed when status=failed and next_retry_at=nil")
	}

	next := time.Now().Add(time.Hour)
	task.NextRetryAt = &next
	if task.IsTerminalFailed() {
		t.Error("expected NOT terminal when next_retry_at is set")
	}

	task.Status = model.TaskPublished
	if task.IsTerminalFailed() {
		t.Error("expected NOT terminal for published task")
	}
}

func TestPublishTask_HasRetryPending(t *testing.T) {
	next := time.Now().Add(time.Hour)
	task := &model.PublishTask{Status: model.TaskFailed, NextRetryAt: &next}
	if !task.HasRetryPending() {
		t.Error("expected retry pending when status=failed and next_retry_at is set")
	}

	task.NextRetryAt = nil
	if task.HasRetryPending() {
		t.Error("expected NO retry pending when next_retry_at is nil")
	}
}

// ==================== isTaskActionable ====================

func TestIsTaskActionable(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	tests := []struct {
		name      string
		status    model.PublishTaskStatus
		nextRetry *time.Time
		want      bool
	}{
		{"pending", model.TaskPending, nil, true},
		{"publishing (stuck)", model.TaskPublishing, nil, true},
		{"failed retry due", model.TaskFailed, &past, true},
		{"failed retry not due", model.TaskFailed, &future, false},
		{"failed terminal", model.TaskFailed, nil, false},
		{"published", model.TaskPublished, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.PublishTask{Status: tt.status, NextRetryAt: tt.nextRetry}
			got := isTaskActionable(task, now)
			if got != tt.want {
				t.Errorf("isTaskActionable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ==================== aggregateAndUpdate ====================

func TestAggregateAndUpdate_AllSuccess(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.ScheduleProcessing, model.TaskPending)

	db.Model(&model.PublishTask{}).Where("schedule_id = ?", schedule.ID).
		Updates(map[string]any{"status": model.TaskPublished, "published_at": time.Now()})

	var updated []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Find(&updated)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.aggregateAndUpdate(schedule, updated)

	var reloadedSch model.Schedule
	db.First(&reloadedSch, schedule.ID)
	if reloadedSch.Status != model.ScheduleCompleted {
		t.Errorf("expected schedule 'completed', got %q", reloadedSch.Status)
	}

	var reloadedContent model.Content
	db.First(&reloadedContent, content.ID)
	if reloadedContent.Status != model.StatusPublished {
		t.Errorf("expected content 'published', got %q", reloadedContent.Status)
	}
}

func TestAggregateAndUpdate_AllTerminalFailed(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.ScheduleProcessing, model.TaskPending)

	db.Model(&model.PublishTask{}).Where("schedule_id = ?", schedule.ID).
		Updates(map[string]any{"status": model.TaskFailed, "next_retry_at": nil})

	var updated []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Find(&updated)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.aggregateAndUpdate(schedule, updated)

	var reloadedSch model.Schedule
	db.First(&reloadedSch, schedule.ID)
	if reloadedSch.Status != model.ScheduleFailed {
		t.Errorf("expected schedule 'failed', got %q", reloadedSch.Status)
	}

	var reloadedContent model.Content
	db.First(&reloadedContent, content.ID)
	if reloadedContent.Status != model.StatusFailed {
		t.Errorf("expected content 'failed', got %q", reloadedContent.Status)
	}
}

func TestAggregateAndUpdate_PartiallyCompleted(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.ScheduleProcessing, model.TaskPending)

	var tasks []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Order("id ASC").Find(&tasks)

	db.Model(&tasks[0]).Updates(map[string]any{
		"status": model.TaskPublished, "published_at": time.Now(), "next_retry_at": nil,
	})
	db.Model(&tasks[1]).Updates(map[string]any{
		"status": model.TaskFailed, "next_retry_at": nil,
	})

	var updated []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Find(&updated)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.aggregateAndUpdate(schedule, updated)

	var reloadedSch model.Schedule
	db.First(&reloadedSch, schedule.ID)
	if reloadedSch.Status != model.SchedulePartiallyCompleted {
		t.Errorf("expected schedule 'partially_completed', got %q", reloadedSch.Status)
	}

	var reloadedContent model.Content
	db.First(&reloadedContent, content.ID)
	if reloadedContent.Status != model.StatusPartiallyPublished {
		t.Errorf("expected content 'partially_published', got %q", reloadedContent.Status)
	}
}

func TestAggregateAndUpdate_StillRetrying(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.ScheduleProcessing, model.TaskPending)

	var tasks []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Order("id ASC").Find(&tasks)

	db.Model(&tasks[0]).Updates(map[string]any{
		"status": model.TaskPublished, "published_at": time.Now(), "next_retry_at": nil,
	})
	nextRetry := time.Now().Add(time.Hour)
	db.Model(&tasks[1]).Updates(map[string]any{
		"status": model.TaskFailed, "next_retry_at": &nextRetry,
	})

	var updated []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Find(&updated)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.aggregateAndUpdate(schedule, updated)

	var reloadedSch model.Schedule
	db.First(&reloadedSch, schedule.ID)
	if reloadedSch.Status != model.ScheduleProcessing {
		t.Errorf("expected schedule 'processing' (still retrying), got %q", reloadedSch.Status)
	}

	var reloadedContent model.Content
	db.First(&reloadedContent, content.ID)
	if reloadedContent.Status != model.StatusScheduled {
		t.Errorf("expected content 'scheduled' (still retrying), got %q", reloadedContent.Status)
	}
}

// ==================== PublishNow ====================

func TestPublishNow_SinglePlatform(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusApproved)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu, results: []*publisher.PublishResult{successResult()}})

	err := sched.PublishNow(content.ID)
	if err != nil {
		t.Fatalf("PublishNow error: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	var schedules []model.Schedule
	db.Where("content_id = ?", content.ID).Find(&schedules)
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}

	var tasks []model.PublishTask
	db.Where("schedule_id = ?", schedules[0].ID).Find(&tasks)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task for single platform, got %d", len(tasks))
	}
	if tasks[0].Platform != "zhihu" {
		t.Errorf("expected platform 'zhihu', got %q", tasks[0].Platform)
	}
}

func TestPublishNow_BothPlatform(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusApproved)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu, results: []*publisher.PublishResult{successResult()}})
	sched.Register("xiaohongshu", &controllableMockPub{name: "xiaohongshu", platform: publisher.PlatformXiaohongshu, results: []*publisher.PublishResult{successResult()}})

	err := sched.PublishNow(content.ID)
	if err != nil {
		t.Fatalf("PublishNow error: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	var tasks []model.PublishTask
	db.Where("content_id = ?", content.ID).Find(&tasks)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks for both platform, got %d", len(tasks))
	}
}

func TestPublishNow_InvalidStatus(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusDraft)

	sched := &Scheduler{db: db, maxRetries: 3, timezone: time.UTC}
	err := sched.PublishNow(content.ID)
	if err == nil {
		t.Error("expected error for draft status")
	}
}

func TestPublishNow_NotFound(t *testing.T) {
	db := testDB(t)
	sched := &Scheduler{db: db, maxRetries: 3, timezone: time.UTC}
	err := sched.PublishNow(99999)
	if err == nil {
		t.Error("expected error for non-existent content")
	}
}

func TestPublishNow_NoPublisher(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusApproved)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, timezone: time.UTC}
	err := sched.PublishNow(content.ID)
	if err == nil {
		t.Error("expected error when no publisher registered")
	}
}

func TestPublishNow_Both_MissingOnePublisher(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusApproved)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu})

	err := sched.PublishNow(content.ID)
	if err == nil {
		t.Error("expected error when xiaohongshu publisher is missing for both mode")
	}
}

// ==================== Per-platform retry isolation ====================

func TestPerPlatformRetry_OnlyFailedPlatformRetried(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformBoth, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	sched := &Scheduler{db: db, publishers: make(map[string]publisher.Publisher), maxRetries: 3, retryDelay: 10 * time.Minute, timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu, results: []*publisher.PublishResult{successResult()}})
	sched.Register("xiaohongshu", &controllableMockPub{name: "xiaohongshu", platform: publisher.PlatformXiaohongshu,
		results: []*publisher.PublishResult{failResult()},
		errors:  []error{failErr()},
	})

	var tasks []model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).Order("id ASC").Find(&tasks)

	for i := range tasks {
		sched.processTask(&tasks[i], schedule)
	}

	db.Where("schedule_id = ?", schedule.ID).Order("id ASC").Find(&tasks)

	var zhTask, xhsTask *model.PublishTask
	for i := range tasks {
		switch tasks[i].Platform {
		case "zhihu":
			zhTask = &tasks[i]
		case "xiaohongshu":
			xhsTask = &tasks[i]
		}
	}

	if zhTask.Status != model.TaskPublished {
		t.Errorf("expected zhihu task 'published', got %q", zhTask.Status)
	}
	if xhsTask.Status != model.TaskFailed {
		t.Errorf("expected xhs task 'failed', got %q", xhsTask.Status)
	}
	if xhsTask.RetryCount != 1 {
		t.Errorf("expected xhs retry_count 1, got %d", xhsTask.RetryCount)
	}
	if xhsTask.NextRetryAt == nil {
		t.Error("expected xhs next_retry_at to be set")
	}
}

// ==================== Publisher helpers ====================

func TestSelectPublisher(t *testing.T) {
	sched := &Scheduler{publishers: make(map[string]publisher.Publisher), timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu})
	sched.Register("xiaohongshu", &controllableMockPub{name: "xiaohongshu", platform: publisher.PlatformXiaohongshu})

	if p := sched.selectPublisher(model.PlatformZhihu); p == nil {
		t.Error("expected zhihu publisher")
	}
	if p := sched.selectPublisher(model.PlatformXiaohongshu); p == nil {
		t.Error("expected xiaohongshu publisher")
	}
	if p := sched.selectPublisher(model.PlatformBoth); p != nil {
		t.Error("expected nil for 'both' platform")
	}
}

func TestHasPublisherFor(t *testing.T) {
	sched := &Scheduler{publishers: make(map[string]publisher.Publisher), timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu})

	if !sched.HasPublisherFor(model.PlatformZhihu) {
		t.Error("expected true for zhihu")
	}
	if sched.HasPublisherFor(model.PlatformBoth) {
		t.Error("expected false for both (missing xhs)")
	}
}

func TestMissingPublishers(t *testing.T) {
	sched := &Scheduler{publishers: make(map[string]publisher.Publisher), timezone: time.UTC}
	sched.Register("zhihu", &controllableMockPub{name: "zhihu", platform: publisher.PlatformZhihu})

	missing := sched.MissingPublishers(model.PlatformBoth)
	if len(missing) != 1 || missing[0] != "xiaohongshu" {
		t.Errorf("expected missing [xiaohongshu], got %v", missing)
	}
}

func TestExpandPlatforms(t *testing.T) {
	tests := []struct {
		name     string
		input    model.Platform
		expected []model.Platform
	}{
		{"zhihu", model.PlatformZhihu, []model.Platform{model.PlatformZhihu}},
		{"xiaohongshu", model.PlatformXiaohongshu, []model.Platform{model.PlatformXiaohongshu}},
		{"both", model.PlatformBoth, []model.Platform{model.PlatformZhihu, model.PlatformXiaohongshu}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandPlatforms(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d platforms, got %d", len(tt.expected), len(got))
			}
			for i, p := range got {
				if p != tt.expected[i] {
					t.Errorf("platform[%d]: expected %q, got %q", i, tt.expected[i], p)
				}
			}
		})
	}
}

func TestCanPublish(t *testing.T) {
	tests := []struct {
		status model.ContentStatus
		want   bool
	}{
		{model.StatusDraft, false},
		{model.StatusAIGenerated, false},
		{model.StatusReviewing, false},
		{model.StatusApproved, true},
		{model.StatusScheduled, true},
		{model.StatusFailed, true},
		{model.StatusPartiallyPublished, true},
		{model.StatusPublished, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := canPublish(tt.status)
			if got != tt.want {
				t.Errorf("canPublish(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// ==================== buildErrorDetail ====================

func TestBuildErrorDetail(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		result *publisher.PublishResult
		want   string
	}{
		{"both nil", nil, nil, "unknown error"},
		{"err only", fmt.Errorf("foo"), nil, "foo"},
		{"result only", nil, &publisher.PublishResult{Message: "bar"}, "bar"},
		{"both different", fmt.Errorf("foo"), &publisher.PublishResult{Message: "bar"}, "foo | bar"},
		{"both same (no dup)", fmt.Errorf("foo"), &publisher.PublishResult{Message: "foo"}, "foo"},
		{"empty result message", fmt.Errorf("err"), &publisher.PublishResult{Message: ""}, "err"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildErrorDetail(tt.err, tt.result)
			if got != tt.want {
				t.Errorf("buildErrorDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ==================== writePublishLog ====================

func TestWritePublishLog_Success(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.writePublishLog(&task, schedule, "zhihu", "success", 1, 1500, "published ok", "http://example.com", "/tmp/ss.png")

	var logs []model.PublishLog
	db.Where("publish_task_id = ?", task.ID).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	l := logs[0]
	if l.ScheduleID != schedule.ID {
		t.Errorf("schedule_id: got %d, want %d", l.ScheduleID, schedule.ID)
	}
	if l.PublishTaskID != task.ID {
		t.Errorf("publish_task_id: got %d, want %d", l.PublishTaskID, task.ID)
	}
	if l.DurationMs != 1500 {
		t.Errorf("duration_ms: got %d", l.DurationMs)
	}
	if l.ErrorDetail != "" {
		t.Errorf("expected empty error_detail for success, got %q", l.ErrorDetail)
	}
}

func TestWritePublishLog_Failed(t *testing.T) {
	db := testDB(t)
	content := testContent(t, db, model.PlatformZhihu, model.StatusScheduled)
	schedule := testScheduleWithTasks(t, db, content, model.SchedulePending, model.TaskPending)

	var task model.PublishTask
	db.Where("schedule_id = ?", schedule.ID).First(&task)

	sched := &Scheduler{db: db, timezone: time.UTC}
	sched.writePublishLog(&task, schedule, "zhihu", "failed", 1, 500, "connection refused", "", "")

	var logs []model.PublishLog
	db.Where("publish_task_id = ?", task.ID).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].ErrorDetail != "connection refused" {
		t.Errorf("error_detail: got %q", logs[0].ErrorDetail)
	}
}

// ==================== Scheduler constructor ====================

func TestScheduler_New(t *testing.T) {
	db := testDB(t)
	sched, err := New(db, 5*time.Minute, 3, 10*time.Minute, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if sched.interval != 5*time.Minute {
		t.Errorf("interval: got %v", sched.interval)
	}
	if sched.maxRetries != 3 {
		t.Errorf("maxRetries: got %d", sched.maxRetries)
	}
	if sched.retryDelay != 10*time.Minute {
		t.Errorf("retryDelay: got %v", sched.retryDelay)
	}
}

func TestScheduler_Register(t *testing.T) {
	sched := &Scheduler{publishers: make(map[string]publisher.Publisher), timezone: time.UTC}
	pub := &controllableMockPub{name: "test-pub", platform: publisher.PlatformZhihu}
	sched.Register("test-pub", pub)

	if p, ok := sched.publishers["test-pub"]; !ok || p != pub {
		t.Error("publisher not registered correctly")
	}
}

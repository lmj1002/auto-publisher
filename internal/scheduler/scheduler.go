package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"auto-publisher/internal/model"
	"auto-publisher/internal/publisher"

	"gorm.io/gorm"
)

// Scheduler 定时调度器
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

// New 创建调度器
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

// Register 注册发布器
func (s *Scheduler) Register(name string, p publisher.Publisher) {
	s.publishers[name] = p
	log.Printf("[Scheduler] 注册发布器: %s (%s)", name, p.Platform())
}

// Start 启动调度器
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.loop()
	log.Printf("[Scheduler] 调度器已启动，扫描间隔: %v，时区: %s", s.interval, s.timezone)
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	log.Println("[Scheduler] 调度器已停止")
}

// loop 主循环
func (s *Scheduler) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 启动时立即执行一次
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

// scanAndPublish 扫描并发布到期的内容
func (s *Scheduler) scanAndPublish() {
	now := time.Now().In(s.timezone)

	// 查找所有已排期且到时间的内容
	var contents []model.Content
	err := s.db.Where(
		"status = ? AND scheduled_at <= ?",
		model.StatusScheduled, now,
	).Order("scheduled_at ASC").Find(&contents).Error

	if err != nil {
		log.Printf("[Scheduler] 扫描排期失败: %v", err)
		return
	}

	if len(contents) == 0 {
		return
	}

	log.Printf("[Scheduler] 发现 %d 条待发布内容", len(contents))

	// 逐条发布
	for _, content := range contents {
		s.publishContent(&content)
	}
}

// publishContent 发布单条内容
func (s *Scheduler) publishContent(content *model.Content) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Printf("[Scheduler] 开始发布 #%d: %s", content.ID, content.Topic)

	// 选择发布器
	pub := s.selectPublisher(content.Platform)
	if pub == nil {
		s.markFailed(content, "没有可用的发布器")
		return
	}

	// 带重试的发布
	var lastErr error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("[Scheduler] 重试 #%d (attempt %d/%d)", content.ID, attempt, s.maxRetries)
			time.Sleep(s.retryDelay)
		}

		result, err := pub.Publish(ctx, content)
		if err == nil && result.Success {
			s.markPublished(content, result)
			return
		}
		lastErr = err
		if result != nil {
			lastErr = fmtErr(result.Message)
		}
	}

	s.markFailed(content, fmt.Sprintf("发布失败（已重试%d次）: %v", s.maxRetries, lastErr))
}

// selectPublisher 根据平台选择发布器
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

// markPublished 标记为已发布
func (s *Scheduler) markPublished(content *model.Content, result *publisher.PublishResult) {
	now := time.Now()
	updates := map[string]interface{}{
		"status":       model.StatusPublished,
		"published_at": now,
		"publish_error": nil,
		"retry_count":  0,
	}

	s.db.Model(content).Updates(updates)
	log.Printf("[Scheduler] ✅ 发布成功 #%d: %s", content.ID, result.URL)
}

// markFailed 标记为发布失败
func (s *Scheduler) markFailed(content *model.Content, errMsg string) {
	retryCount := content.RetryCount + 1
	status := model.StatusFailed
	if retryCount <= s.maxRetries {
		status = model.StatusScheduled // 保持排期状态等待下次重试
	}

	updates := map[string]interface{}{
		"status":        status,
		"publish_error": errMsg,
		"retry_count":   retryCount,
	}

	s.db.Model(content).Updates(updates)
	log.Printf("[Scheduler] ❌ 发布失败 #%d: %s (retry: %d/%d)", content.ID, errMsg, retryCount, s.maxRetries)
}

// PublishNow 立即发布指定内容（供手动触发）
func (s *Scheduler) PublishNow(contentID int64) error {
	var content model.Content
	if err := s.db.First(&content, contentID).Error; err != nil {
		return fmtErr("内容不存在: " + err.Error())
	}

	if content.Status != model.StatusApproved && content.Status != model.StatusScheduled {
		return fmtErr("内容状态不允许发布: " + string(content.Status))
	}

	go s.publishContent(&content)
	return nil
}

// fmtErr 辅助函数
func fmtErr(msg string) error {
	return &schedError{msg}
}

type schedError struct {
	msg string
}

func (e *schedError) Error() string {
	return e.msg
}

package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"auto-publisher/internal/config"
	"auto-publisher/internal/model"
	"auto-publisher/internal/provider"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ContentHandler 内容管理 Handler
type ContentHandler struct {
	db     *gorm.DB
	router *provider.Router
	cfg    *config.Config
	pm     *provider.PromptManager
}

// NewContentHandler 创建 Handler
func NewContentHandler(db *gorm.DB, router *provider.Router, cfg *config.Config, pm *provider.PromptManager) *ContentHandler {
	return &ContentHandler{db: db, router: router, cfg: cfg, pm: pm}
}

// ==================== 内容 CRUD ====================

// List 内容列表
func (h *ContentHandler) List(c *gin.Context) {
	var query model.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		query.Page = 1
		query.PageSize = 20
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		query.PageSize = 20
	}

	var contents []model.Content
	var total int64

	tx := h.db.Model(&model.Content{})
	if query.Status != "" {
		statuses := strings.Split(string(query.Status), ",")
		tx = tx.Where("status IN ?", statuses)
	}
	if query.Platform != "" {
		tx = tx.Where("platform = ? OR platform = 'both'", query.Platform)
	}
	tx.Count(&total)
	tx.Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Order("created_at DESC").
		Find(&contents)

	// Enrich with latest schedule info
	type ContentWithSchedule struct {
		model.Content
		ScheduleScheduledAt  *time.Time `json:"schedule_scheduled_at,omitempty"`
		ScheduleRetryCount   int        `json:"schedule_retry_count"`
		SchedulePublishError string     `json:"schedule_publish_error,omitempty"`
		ScheduleStatus       string     `json:"schedule_status,omitempty"`
	}

	result := make([]ContentWithSchedule, len(contents))
	for i, c := range contents {
		cws := ContentWithSchedule{Content: c}
		var latest model.Schedule
		if err := h.db.Where("content_id = ?", c.ID).Order("created_at DESC").First(&latest).Error; err == nil {
			cws.ScheduleScheduledAt = latest.ScheduledAt
			cws.ScheduleStatus = string(latest.Status)

			// Get retry count and error from the latest PublishTask
			var latestTask model.PublishTask
			if err := h.db.Where("schedule_id = ?", latest.ID).Order("created_at DESC").First(&latestTask).Error; err == nil {
				cws.ScheduleRetryCount = latestTask.RetryCount
				if latestTask.PublishError.Valid {
					cws.SchedulePublishError = latestTask.PublishError.String
				}
			}
		}
		result[i] = cws
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"list":      result,
			"total":     total,
			"page":      query.Page,
			"page_size": query.PageSize,
		},
	})
}

// Get 获取单条内容（含最新排期信息）
func (h *ContentHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
		return
	}

	var content model.Content
	if err := h.db.First(&content, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "msg": "内容不存在"})
		return
	}

	// Load latest schedule with PublishTasks
	var latestSchedule model.Schedule
	h.db.Where("content_id = ?", content.ID).
		Preload("PublishTasks").
		Order("created_at DESC").First(&latestSchedule)

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"content":         content,
		"latest_schedule": latestSchedule,
	}})
}

// Update 更新内容（人工修改定稿）
func (h *ContentHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
		return
	}

	var content model.Content
	if err := h.db.First(&content, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "msg": "内容不存在"})
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "请求参数错误"})
		return
	}

	// 只允许更新的字段白名单
	allowedFields := map[string]bool{
		"topic": true, "xhs_title": true, "xhs_body": true, "xhs_images": true,
		"zh_title": true, "zh_body": true, "zh_topics": true,
		"status": true, "key_points": true,
	}
	filtered := make(map[string]interface{})
	for k, v := range updates {
		if allowedFields[k] {
			filtered[k] = v
		}
	}

	if err := h.db.Model(&content).Updates(filtered).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "更新失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "更新成功"})
}

// Delete 删除内容
func (h *ContentHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
		return
	}

	if err := h.db.Delete(&model.Content{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "删除失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "删除成功"})
}

// ==================== 执行日志 ====================

// LogEntry 执行日志条目（JOIN content.topic）
type LogEntry struct {
	model.PublishLog
	Topic string `json:"topic"`
}

// ListLogs 查询发布执行日志
func (h *ContentHandler) ListLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	// Build base query with filters
	baseQuery := func(tx *gorm.DB) *gorm.DB {
		if contentID := c.Query("content_id"); contentID != "" {
			tx = tx.Where("publish_logs.content_id = ?", contentID)
		}
		if scheduleID := c.Query("schedule_id"); scheduleID != "" {
			tx = tx.Where("publish_logs.schedule_id = ?", scheduleID)
		}
		if platform := c.Query("platform"); platform != "" {
			tx = tx.Where("publish_logs.platform = ?", platform)
		}
		if status := c.Query("status"); status != "" {
			tx = tx.Where("publish_logs.status = ?", status)
		}
		return tx
	}

	var total int64
	baseQuery(h.db.Model(&model.PublishLog{})).Count(&total)

	var logs []LogEntry
	dataQuery := h.db.Table("publish_logs").
		Select("publish_logs.*, contents.topic").
		Joins("LEFT JOIN contents ON contents.id = publish_logs.content_id")
	dataQuery = baseQuery(dataQuery)
	dataQuery.Order("publish_logs.created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&logs)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"list":      logs,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ==================== AI 生成 ====================

// GenerateDraft 触发 AI 生成初稿
func (h *ContentHandler) GenerateDraft(c *gin.Context) {
	var req model.GenerateDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "请填写选题和核心观点"})
		return
	}

	slog.Info("ai generation start", "platform", req.Platform, "type", req.Type, "topic", req.Topic)

	// 1. 获取 System Prompt（优先从文件读取）
	sysPrompt := h.pm.GetSystemPrompt(string(req.Platform), getSystemPrompt(h.cfg, req.Platform))

	// 2. 构建用户 Prompt
	userPrompt := fmt.Sprintf("%s\n\n核心观点：\n%s", req.Topic, req.KeyPoints)

	// 3. 调用模型路由 → 生成文字
	textResp, err := h.router.Generate(c.Request.Context(), &provider.GenerateRequest{
		TaskType:     provider.ProviderText,
		Platform:     req.Platform,
		ContentType:  req.Type,
		SystemPrompt: sysPrompt,
		UserPrompt:   userPrompt,
	})
	if err != nil {
		slog.Error("ai generation failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  fmt.Sprintf("AI 生成失败: %v", err),
		})
		return
	}

	slog.Info("ai generation complete", "duration", textResp.Duration, "content_len", len(textResp.Text))

	// 4. 解析输出
	parsed := provider.ParseMarkedOutput(textResp.Text, req.Platform)

	// 5. 构建 Content 对象
	content := &model.Content{
		Topic:       req.Topic,
		Platform:    req.Platform,
		Type:        req.Type,
		KeyPoints:   req.KeyPoints,
		Status:      model.StatusAIGenerated,
		AIModel:     textResp.Model,
		AIRawOutput: textResp.RawOutput,
	}

	// 根据平台填充不同字段
	fillContentFields(content, parsed, req.Platform)

	// 6. 保存到数据库
	if err := h.db.Create(content).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "保存内容失败"})
		return
	}

	// 7. 返回结果（包含解析后的结构化数据）
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"content":     content,
			"raw_output":  textResp.RawOutput,
			"titles":      parsed.Titles,
			"body":        parsed.Body,
			"tags":        parsed.Tags,
			"duration_ms": textResp.Duration.Milliseconds(),
		},
	})
}

// GenerateDraftWithImage 生成文字 + 封面图
func (h *ContentHandler) GenerateDraftWithImage(c *gin.Context) {
	var req struct {
		model.GenerateDraftRequest
		NeedCover        bool   `json:"need_cover"`
		ImageAspectRatio string `json:"image_aspect_ratio"`
		ImageStyle       string `json:"image_style"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "请填写选题和核心观点"})
		return
	}
	if req.ImageAspectRatio == "" {
		req.ImageAspectRatio = getDefaultAspectRatio(req.Platform)
	}

	slog.Info("generation with image start", "platform", req.Platform, "topic", req.Topic, "need_cover", req.NeedCover)

	// 1. 生成文字
	sysPrompt := h.pm.GetSystemPrompt(string(req.Platform), getSystemPrompt(h.cfg, req.Platform))
	userPrompt := fmt.Sprintf("%s\n\n核心观点：\n%s", req.Topic, req.KeyPoints)

	textResp, err := h.router.Generate(c.Request.Context(), &provider.GenerateRequest{
		TaskType:     provider.ProviderText,
		Platform:     req.Platform,
		SystemPrompt: sysPrompt,
		UserPrompt:   userPrompt,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "文字生成失败: " + err.Error()})
		return
	}

	// 2. 生成封面图（如果需要）
	var images []provider.ImageResult
	if req.NeedCover {
		imgResp, imgErr := h.router.Generate(c.Request.Context(), &provider.GenerateRequest{
			TaskType:         provider.ProviderImage,
			Platform:         req.Platform,
			UserPrompt:       req.Topic,
			ImageAspectRatio: req.ImageAspectRatio,
			ImageStyle:       req.ImageStyle,
		})
		if imgErr != nil {
			slog.Warn("image generation failed but text generated", "error", imgErr)
		} else {
			images = imgResp.Images
		}
	}

	// 3. 解析 + 保存
	parsed := provider.ParseMarkedOutput(textResp.Text, req.Platform)
	content := &model.Content{
		Topic:       req.Topic,
		Platform:    req.Platform,
		Type:        req.Type,
		KeyPoints:   req.KeyPoints,
		Status:      model.StatusAIGenerated,
		AIModel:     textResp.Model,
		AIRawOutput: textResp.RawOutput,
	}
	fillContentFields(content, parsed, req.Platform)

	// 图片 URL 存入 content
	if len(images) > 0 {
		var imgURLs []string
		for _, img := range images {
			imgURLs = append(imgURLs, img.URL)
		}
		content.XHSImages = stringsJoin(imgURLs, ",")
	}

	if err := h.db.Create(content).Error; err != nil {
		slog.Error("save generated content failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "保存内容失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"content":    content,
			"raw_output": textResp.RawOutput,
			"titles":     parsed.Titles,
			"body":       parsed.Body,
			"tags":       parsed.Tags,
			"images":     images,
			"text_ms":    textResp.Duration.Milliseconds(),
		},
	})
}

// ==================== 排期发布 ====================

// Schedule 设置排期（创建 Schedule 记录）
func (h *ContentHandler) Schedule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
		return
	}

	var req model.ScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "请提供发布时间"})
		return
	}

	scheduledAt, err := time.ParseInLocation("2006-01-02 15:04:05", req.ScheduledAt, time.Local)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "时间格式错误，应为: 2006-01-02 15:04:05"})
		return
	}

	if scheduledAt.Before(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "发布时间不能是过去"})
		return
	}

	// Verify content exists and is in a schedulable state
	var content model.Content
	if err := h.db.First(&content, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "msg": "内容不存在"})
		return
	}
	if !isSchedulable(content.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "当前状态不能排期，请先定稿"})
		return
	}

	tx := h.db.Begin()

	// Cancel any existing active schedules for this content (reschedule scenario)
	if err := tx.Model(&model.Schedule{}).
		Where("content_id = ? AND status IN ?", id,
			[]model.ScheduleStatus{model.SchedulePending, model.ScheduleProcessing}).
		Update("status", model.ScheduleCancelled).Error; err != nil {
		tx.Rollback()
		slog.Error("cancel existing schedules failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "取消旧排期失败"})
		return
	}
	// Cancel non-terminal PublishTasks for those schedules
	if err := tx.Model(&model.PublishTask{}).
		Where("content_id = ? AND status NOT IN ?", id,
			[]model.PublishTaskStatus{model.TaskPublished, model.TaskFailed}).
		Update("status", model.TaskFailed).
		Update("next_retry_at", nil).
		Update("publish_error", "superseded by new schedule").Error; err != nil {
		tx.Rollback()
		slog.Error("cancel existing publish tasks failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "取消旧发布任务失败"})
		return
	}

	// Create Schedule + PublishTasks in a transaction
	schedule := &model.Schedule{
		ContentID:   id,
		ScheduledAt: &scheduledAt,
		Status:      model.SchedulePending,
	}
	tasks := buildPublishTasks(content.ID, schedule.ID, content.Platform)

	if err := tx.Create(schedule).Error; err != nil {
		tx.Rollback()
		slog.Error("create schedule failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "排期创建失败"})
		return
	}
	for _, t := range tasks {
		t.ScheduleID = schedule.ID
		if err := tx.Create(t).Error; err != nil {
			tx.Rollback()
			slog.Error("create publish task failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "创建发布任务失败"})
			return
		}
	}
	if err := tx.Model(&content).Update("status", model.StatusScheduled).Error; err != nil {
		tx.Rollback()
		slog.Error("update content status failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "状态更新失败"})
		return
	}
	tx.Commit()

	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "排期设置成功", "data": gin.H{
		"schedule_id":   schedule.ID,
		"scheduled_at":  scheduledAt.Format("2006-01-02 15:04:05"),
		"publish_tasks": len(tasks),
	}})
}

// Unschedule 取消排期（取消最新的 pending/failed Schedule）
func (h *ContentHandler) Unschedule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
		return
	}

	var content model.Content
	if err := h.db.First(&content, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "msg": "内容不存在"})
		return
	}

	if content.Status != model.StatusScheduled && content.Status != model.StatusFailed &&
		content.Status != model.StatusPartiallyPublished {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "只有已排期、部分发布或失败的内容可以取消排期"})
		return
	}

	// Find the latest cancellable schedule (pending, processing, or failed)
	var schedule model.Schedule
	if err := h.db.Where("content_id = ? AND status IN ?", id,
		[]model.ScheduleStatus{model.SchedulePending, model.ScheduleProcessing, model.ScheduleFailed}).
		Order("created_at DESC").First(&schedule).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "没有可取消的排期"})
		return
	}

	tx := h.db.Begin()
	// Cancel schedule
	if err := tx.Model(&schedule).Update("status", model.ScheduleCancelled).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "取消排期失败"})
		return
	}
	// Cancel any non-terminal PublishTasks
	if err := tx.Model(&model.PublishTask{}).
		Where("schedule_id = ? AND status NOT IN ?", schedule.ID,
			[]model.PublishTaskStatus{model.TaskPublished, model.TaskFailed}).
		Update("status", model.TaskFailed).
		Update("next_retry_at", nil).
		Update("publish_error", "cancelled by user").Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "取消发布任务失败"})
		return
	}
	// Update content status back to approved
	if err := tx.Model(&content).Update("status", model.StatusApproved).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "状态更新失败"})
		return
	}
	tx.Commit()

	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "已取消排期"})
}

// ListSchedules 查看某条内容的所有排期记录（含 PublishTasks）
func (h *ContentHandler) ListSchedules(c *gin.Context) {
	contentID := c.Param("id")
	var schedules []model.Schedule
	h.db.Where("content_id = ?", contentID).
		Preload("PublishTasks").
		Order("created_at DESC").Find(&schedules)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{"list": schedules},
	})
}

// GetScheduleLogs 查看某次排期的所有发布日志
func (h *ContentHandler) GetScheduleLogs(c *gin.Context) {
	scheduleID := c.Param("sid")
	var logs []model.PublishLog
	h.db.Where("schedule_id = ?", scheduleID).Order("created_at ASC").Find(&logs)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{"list": logs},
	})
}

// ==================== 辅助函数 ====================

// getSystemPrompt 获取平台对应的 System Prompt
func getSystemPrompt(cfg *config.Config, platform model.Platform) string {
	switch platform {
	case model.PlatformXiaohongshu:
		return cfg.Prompts.Xiaohongshu.System
	case model.PlatformZhihu:
		return cfg.Prompts.Zhihu.System
	case model.PlatformBoth:
		return cfg.Prompts.Xiaohongshu.System + "\n\n同时请为知乎平台生成内容。"
	default:
		return cfg.Prompts.Xiaohongshu.System
	}
}

// fillContentFields 根据平台填充 Content 字段
func fillContentFields(content *model.Content, parsed *provider.ParsedTextContent, platform model.Platform) {
	switch platform {
	case model.PlatformXiaohongshu:
		if len(parsed.Titles) > 0 {
			content.XHSTitle = parsed.Titles[0]
		}
		content.XHSBody = parsed.Body

	case model.PlatformZhihu:
		if len(parsed.Titles) > 0 {
			content.ZHTitle = parsed.Titles[0]
		}
		content.ZHBody = parsed.Body
		if len(parsed.Tags) > 0 {
			content.ZHTopics = strings.Join(parsed.Tags, ",")
		}

	case model.PlatformBoth:
		// both 模式需要进一步解析（后续优化）
		content.XHSBody = parsed.Body
		content.ZHBody = parsed.Body
	}
}

// getDefaultAspectRatio 获取平台默认图片比例
func getDefaultAspectRatio(platform model.Platform) string {
	switch platform {
	case model.PlatformXiaohongshu:
		return "3:4"
	case model.PlatformZhihu:
		return "16:9"
	default:
		return "1:1"
	}
}

func stringsJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

// isSchedulable returns true if the content status allows scheduling.
func isSchedulable(status model.ContentStatus) bool {
	switch status {
	case model.StatusApproved, model.StatusFailed, model.StatusPartiallyPublished:
		return true
	default:
		return false
	}
}

// buildPublishTasks creates PublishTask records for the given content platform.
// For platform=both, creates two tasks (zhihu + xiaohongshu).
func buildPublishTasks(contentID, scheduleID int64, platform model.Platform) []*model.PublishTask {
	platforms := expandTargetPlatforms(platform)
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

// expandTargetPlatforms returns individual platforms from a content target.
func expandTargetPlatforms(platform model.Platform) []model.Platform {
	switch platform {
	case model.PlatformBoth:
		return []model.Platform{model.PlatformZhihu, model.PlatformXiaohongshu}
	default:
		return []model.Platform{platform}
	}
}

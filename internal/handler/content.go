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
	db *gorm.DB
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
		tx = tx.Where("status = ?", query.Status)
	}
	if query.Platform != "" {
		tx = tx.Where("platform = ? OR platform = 'both'", query.Platform)
	}
	tx.Count(&total)
	tx.Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Order("created_at DESC").
		Find(&contents)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"list":      contents,
			"total":     total,
			"page":      query.Page,
			"page_size": query.PageSize,
		},
	})
}

// Get 获取单条内容
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

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": content})
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
			"content":      content,
			"titles":       parsed.Titles,
			"body":         parsed.Body,
			"tags":         parsed.Tags,
			"duration_ms":  textResp.Duration.Milliseconds(),
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

	h.db.Create(content)

	if err := h.db.Create(content).Error; err != nil {
		slog.Error("save generated content failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "保存内容失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"content":    content,
			"titles":     parsed.Titles,
			"body":       parsed.Body,
			"tags":       parsed.Tags,
			"images":     images,
			"text_ms":    textResp.Duration.Milliseconds(),
		},
	})
}

// ==================== 排期发布 ====================

// Schedule 设置排期
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

	if err := h.db.Model(&model.Content{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":       model.StatusScheduled,
		"scheduled_at": scheduledAt,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1, "msg": "排期设置失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "排期设置成功", "data": gin.H{
		"scheduled_at": scheduledAt.Format("2006-01-02 15:04:05"),
	}})
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

package model

import (
	"database/sql"
	"time"
)

// ContentStatus 内容状态
type ContentStatus string

const (
	StatusDraft       ContentStatus = "draft"        // 草稿
	StatusAIGenerated ContentStatus = "ai_generated" // AI已生成初稿
	StatusReview      ContentStatus = "review"       // 审核中
	StatusApproved    ContentStatus = "approved"     // 已定稿
	StatusScheduled   ContentStatus = "scheduled"    // 已排期
	StatusPublished   ContentStatus = "published"    // 已发布
	StatusFailed      ContentStatus = "failed"       // 发布失败
)

// Platform 目标平台
type Platform string

const (
	PlatformXiaohongshu Platform = "xiaohongshu"
	PlatformZhihu       Platform = "zhihu"
	PlatformBoth        Platform = "both"
)

// ContentType 内容类型
type ContentType string

const (
	TypeArticle ContentType = "article" // 知乎文章
	TypeNote    ContentType = "note"    // 小红书笔记
	TypeIdea    ContentType = "idea"    // 知乎想法
)

// Content 内容主表
type Content struct {
	ID        int64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Topic     string        `gorm:"type:varchar(500);not null;comment:选题/标题" json:"topic"`
	Platform  Platform      `gorm:"type:varchar(20);not null;comment:目标平台" json:"platform"`
	Type      ContentType   `gorm:"column:content_type;type:varchar(20);not null;comment:内容类型" json:"content_type"`

	// 分平台内容
	XHSTitle  string        `gorm:"type:varchar(500);comment:小红书标题" json:"xhs_title,omitempty"`
	XHSBody   string        `gorm:"type:text;comment:小红书正文" json:"xhs_body,omitempty"`
	XHSImages string        `gorm:"type:json;comment:小红书图片URL列表" json:"xhs_images,omitempty"`
	ZHTitle   string        `gorm:"type:varchar(500);comment:知乎标题" json:"zh_title,omitempty"`
	ZHBody    string        `gorm:"type:text;comment:知乎正文" json:"zh_body,omitempty"`
	ZHTopics  string        `gorm:"type:json;comment:知乎话题标签" json:"zh_topics,omitempty"`

	// 核心观点（用于AI生成）
	KeyPoints string `gorm:"type:text;comment:核心观点" json:"key_points,omitempty"`

	// 状态流转
	Status      ContentStatus `gorm:"type:varchar(20);default:draft;index;comment:内容状态" json:"status"`
	ScheduledAt *time.Time    `gorm:"comment:计划发布时间" json:"scheduled_at,omitempty"`
	PublishedAt *time.Time    `gorm:"comment:实际发布时间" json:"published_at,omitempty"`

	// AI 相关
	AIModel     string `gorm:"type:varchar(50);comment:生成模型" json:"ai_model,omitempty"`
	AIPrompt    string `gorm:"type:text;comment:生成prompt" json:"ai_prompt,omitempty"`
	AIRawOutput string `gorm:"type:text;comment:AI原始输出" json:"ai_raw_output,omitempty"`

	// 数据追踪
	XHSLikes    int `gorm:"default:0" json:"xhs_likes"`
	XHSCollects int `gorm:"default:0" json:"xhs_collects"`
	XHSComments int `gorm:"default:0" json:"xhs_comments"`
	ZHLikes     int `gorm:"default:0" json:"zh_likes"`
	ZHComments  int `gorm:"default:0" json:"zh_comments"`
	ZHViews     int `gorm:"default:0" json:"zh_views"`

	// 发布错误信息
	PublishError sql.NullString `gorm:"type:text;comment:发布错误信息" json:"publish_error,omitempty"`
	RetryCount   int            `gorm:"default:0;comment:重试次数" json:"retry_count"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名
func (Content) TableName() string {
	return "contents"
}

// GenerateDraftRequest AI生成初稿请求
type GenerateDraftRequest struct {
	Topic     string      `json:"topic" binding:"required"`
	KeyPoints string      `json:"key_points" binding:"required"`
	Platform  Platform    `json:"platform" binding:"required"`
	Type      ContentType `json:"content_type" binding:"required"`
}

// GenerateDraftResponse AI生成初稿响应
type GenerateDraftResponse struct {
	Content    *Content `json:"content"`
	XHSTitle   string   `json:"xhs_title,omitempty"`
	XHSBody    string   `json:"xhs_body,omitempty"`
	XHSTags    string   `json:"xhs_tags,omitempty"`
	ZHTitle    string   `json:"zh_title,omitempty"`
	ZHBody     string   `json:"zh_body,omitempty"`
	ZHTopics   string   `json:"zh_topics,omitempty"`
	RawOutput  string   `json:"raw_output"`
}

// ScheduleRequest 排期请求
type ScheduleRequest struct {
	ScheduledAt string `json:"scheduled_at" binding:"required"` // 格式: 2006-01-02 15:04:05
}

// ListQuery 内容列表查询参数
type ListQuery struct {
	Status   ContentStatus `form:"status"`
	Platform Platform      `form:"platform"`
	Page     int           `form:"page"`
	PageSize int           `form:"page_size"`
}

// AIOptions AI 生成选项
type AIOptions struct {
	Platform    Platform    `json:"platform"`
	ContentType ContentType `json:"content_type"`
	Topic       string      `json:"topic"`
	KeyPoints   string      `json:"key_points"`
}

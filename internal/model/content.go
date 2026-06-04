package model

import (
	"database/sql"
	"time"
)

// ContentStatus 内容状态 — 内容生命周期的人类可见阶段
type ContentStatus string

const (
	StatusDraft              ContentStatus = "draft"               // 草稿 — 仅有选题和核心观点
	StatusAIGenerated        ContentStatus = "ai_generated"        // AI已生成初稿
	StatusReviewing          ContentStatus = "reviewing"           // 审核中 — 人类正在审核/修改 AI 输出
	StatusApproved           ContentStatus = "approved"            // 已定稿 — 内容就绪，等待排期
	StatusScheduled          ContentStatus = "scheduled"           // 已排期 — 存在活跃的 Schedule
	StatusPublished          ContentStatus = "published"           // 已发布 — 所有平台发布成功
	StatusPartiallyPublished ContentStatus = "partially_published" // 部分发布 — 部分平台成功、部分终结失败（仅 both）
	StatusFailed             ContentStatus = "failed"              // 发布失败 — 所有平台终结失败
)

// StatusReview 旧状态值，保留兼容；新代码应使用 StatusReviewing
// Deprecated: use StatusReviewing
const StatusReview ContentStatus = "review"

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
	ID       int64       `gorm:"primaryKey;autoIncrement" json:"id"`
	Topic    string      `gorm:"type:varchar(500);not null;comment:选题/标题" json:"topic"`
	Platform Platform    `gorm:"type:varchar(20);not null;comment:目标平台" json:"platform"`
	Type     ContentType `gorm:"column:content_type;type:varchar(20);not null;comment:内容类型" json:"content_type"`

	// 分平台内容
	XHSTitle  string `gorm:"type:varchar(500);comment:小红书标题" json:"xhs_title,omitempty"`
	XHSBody   string `gorm:"type:text;comment:小红书正文" json:"xhs_body,omitempty"`
	XHSImages string `gorm:"type:text;comment:小红书图片URL列表" json:"xhs_images,omitempty"`
	ZHTitle   string `gorm:"type:varchar(500);comment:知乎标题" json:"zh_title,omitempty"`
	ZHBody    string `gorm:"type:text;comment:知乎正文" json:"zh_body,omitempty"`
	ZHTopics  string `gorm:"type:text;comment:知乎话题标签" json:"zh_topics,omitempty"`

	// 核心观点（用于AI生成）
	KeyPoints string `gorm:"type:text;comment:核心观点" json:"key_points,omitempty"`

	// 状态流转
	Status      ContentStatus `gorm:"type:varchar(20);default:draft;index;comment:内容状态" json:"status"`
	ScheduledAt *time.Time    `gorm:"comment:计划发布时间" json:"scheduled_at,omitempty"`
	PublishedAt *time.Time    `gorm:"comment:最终发布时间(所有平台完成)" json:"published_at,omitempty"`

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

	// Deprecated: 已迁移到 PublishTask，保留字段兼容旧数据
	PublishError sql.NullString `gorm:"type:text;comment:[DEPRECATED]发布错误信息-已迁移到publish_tasks" json:"publish_error,omitempty"`
	RetryCount   int            `gorm:"default:0;comment:[DEPRECATED]重试次数-已迁移到publish_tasks" json:"retry_count"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName specifies the table name for Content.
func (Content) TableName() string {
	return "contents"
}

// ==================== 排期管理 ====================

// ScheduleStatus 排期状态 — 反映关联 PublishTask 的聚合状态
type ScheduleStatus string

const (
	SchedulePending            ScheduleStatus = "pending"             // 等待发布时间到达
	ScheduleProcessing         ScheduleStatus = "processing"          // 正在处理（至少一个 PublishTask 执行中）
	SchedulePublishing         ScheduleStatus = "publishing"          // [Deprecated] alias for processing
	ScheduleCompleted          ScheduleStatus = "completed"           // 全部平台成功
	SchedulePublished          ScheduleStatus = "published"           // [Deprecated] alias for completed
	SchedulePartiallyCompleted ScheduleStatus = "partially_completed" // 部分平台成功、部分终结（仅 both）
	ScheduleFailed             ScheduleStatus = "failed"              // 全部平台终结失败
	ScheduleCancelled          ScheduleStatus = "cancelled"           // 用户取消
)

// Schedule 排期记录（Content 1:N Schedule）
// Schedule 本身不执行发布，而是创建/编排 PublishTask。状态由 PublishTask 聚合而来。
type Schedule struct {
	ID           int64          `gorm:"primaryKey;autoIncrement" json:"id"`
	ContentID    int64          `gorm:"index;not null;comment:关联内容ID" json:"content_id"`
	ScheduledAt  *time.Time     `gorm:"index;not null;comment:计划发布时间" json:"scheduled_at"`
	Status       ScheduleStatus `gorm:"type:varchar(20);default:pending;index;comment:排期状态" json:"status"`
	RetryCount   int            `gorm:"default:0;comment:聚合重试次数" json:"retry_count"`
	NextRetryAt  *time.Time     `gorm:"index;comment:下次重试时间(NULL=终结)" json:"next_retry_at,omitempty"`
	PublishError sql.NullString `gorm:"type:text;comment:最近一次错误信息" json:"publish_error,omitempty"`
	PublishedAt  *time.Time     `gorm:"comment:实际发布时间" json:"published_at,omitempty"`
	CreatedAt    time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"autoUpdateTime" json:"updated_at"`

	// Preload 用
	Content      *Content      `gorm:"foreignKey:ContentID" json:"content,omitempty"`
	PublishTasks []PublishTask `gorm:"foreignKey:ScheduleID" json:"publish_tasks,omitempty"`
}

// TableName specifies the table name for Schedule.
func (Schedule) TableName() string {
	return "schedules"
}

// ==================== 发布任务（NEW） ====================

// PublishTaskStatus 发布任务状态 — 单次发往单个平台的执行状态
type PublishTaskStatus string

const (
	TaskPending    PublishTaskStatus = "pending"    // 等待执行
	TaskPublishing PublishTaskStatus = "publishing" // 正在发布
	TaskPublished  PublishTaskStatus = "published"  // 发布成功
	TaskFailed     PublishTaskStatus = "failed"     // 发布失败（含临时失败-等待重试 和 终结失败）
)

// PublishTask 发布任务 — 代表一次"将内容发布到单个平台"的执行单元。
// 每个 Schedule 创建 1~2 个 PublishTask（取决于 content.platform）。
// PublishTask 独立重试：一个任务失败不影响同 Schedule 的其他任务。
type PublishTask struct {
	ID           int64             `gorm:"primaryKey;autoIncrement" json:"id"`
	ContentID    int64             `gorm:"index;not null;comment:关联内容ID" json:"content_id"`
	ScheduleID   int64             `gorm:"index;not null;comment:关联排期ID" json:"schedule_id"`
	Platform     string            `gorm:"type:varchar(20);not null;comment:目标平台(zhihu/xiaohongshu)" json:"platform"`
	Status       PublishTaskStatus `gorm:"type:varchar(20);default:pending;index;comment:任务状态" json:"status"`
	RetryCount   int               `gorm:"default:0;comment:已重试次数" json:"retry_count"`
	NextRetryAt  *time.Time        `gorm:"index;comment:下次重试时间(NULL=终结)" json:"next_retry_at,omitempty"`
	PublishError sql.NullString    `gorm:"type:text;comment:最近一次错误信息" json:"publish_error,omitempty"`
	PublishedAt  *time.Time        `gorm:"comment:实际发布时间" json:"published_at,omitempty"`
	ExternalID   string            `gorm:"type:varchar(100);comment:平台侧内容ID" json:"external_id,omitempty"`
	ExternalURL  string            `gorm:"type:varchar(500);comment:发布后公开链接" json:"external_url,omitempty"`
	CreatedAt    time.Time         `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time         `gorm:"autoUpdateTime" json:"updated_at"`

	// Preload 用
	Schedule *Schedule `gorm:"foreignKey:ScheduleID" json:"-"`
}

// TableName specifies the table name for PublishTask.
func (PublishTask) TableName() string {
	return "publish_tasks"
}

// IsTerminalFailed returns true when the task has exhausted retries.
// A task with status=failed and next_retry_at=NULL is terminal.
func (t *PublishTask) IsTerminalFailed() bool {
	return t.Status == TaskFailed && t.NextRetryAt == nil
}

// HasRetryPending returns true when the task failed but still has retries left.
func (t *PublishTask) HasRetryPending() bool {
	return t.Status == TaskFailed && t.NextRetryAt != nil
}

// ==================== 发布执行日志 ====================

// PublishLog 发布执行日志（每次发布尝试一条记录，PublishTask 1:N PublishLog）
type PublishLog struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ContentID     int64     `gorm:"index;not null;comment:关联内容ID" json:"content_id"`
	ScheduleID    int64     `gorm:"index;not null;default:0;comment:关联排期ID" json:"schedule_id"`
	PublishTaskID int64     `gorm:"index;not null;default:0;comment:关联发布任务ID" json:"publish_task_id"`
	Platform      string    `gorm:"varchar(20);not null;comment:发布平台" json:"platform"`
	Status        string    `gorm:"varchar(20);not null;index;comment:发布结果(success/failed)" json:"status"`
	Attempt       int       `gorm:"default:1;comment:第几次尝试" json:"attempt"`
	Message       string    `gorm:"type:text;comment:结果消息" json:"message"`
	URL           string    `gorm:"varchar(500);comment:发布后的链接" json:"url"`
	ErrorDetail   string    `gorm:"type:text;comment:错误详情" json:"error_detail"`
	DurationMs    int64     `gorm:"comment:执行耗时(毫秒)" json:"duration_ms"`
	Screenshot    string    `gorm:"varchar(500);comment:截图路径" json:"screenshot"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName specifies the table name for PublishLog.
func (PublishLog) TableName() string {
	return "publish_logs"
}

// ==================== DTOs (unchanged) ====================

// GenerateDraftRequest AI生成初稿请求
type GenerateDraftRequest struct {
	Topic     string      `json:"topic" binding:"required"`
	KeyPoints string      `json:"key_points" binding:"required"`
	Platform  Platform    `json:"platform" binding:"required"`
	Type      ContentType `json:"content_type" binding:"required"`
}

// GenerateDraftResponse AI生成初稿响应
type GenerateDraftResponse struct {
	Content   *Content `json:"content"`
	XHSTitle  string   `json:"xhs_title,omitempty"`
	XHSBody   string   `json:"xhs_body,omitempty"`
	XHSTags   string   `json:"xhs_tags,omitempty"`
	ZHTitle   string   `json:"zh_title,omitempty"`
	ZHBody    string   `json:"zh_body,omitempty"`
	ZHTopics  string   `json:"zh_topics,omitempty"`
	RawOutput string   `json:"raw_output"`
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

// ==================== 内容采集 ====================

// CollectedContent 采集的外部内容参考
type CollectedContent struct {
	ID              int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Platform        Platform  `gorm:"type:varchar(20);not null;index;comment:来源平台" json:"platform"`
	SourceURL       string    `gorm:"type:varchar(1000);not null;comment:原文链接" json:"source_url"`
	Title           string    `gorm:"type:varchar(500);comment:原文标题" json:"title"`
	Author          string    `gorm:"type:varchar(100);comment:作者" json:"author"`
	Summary         string    `gorm:"type:text;comment:内容摘要" json:"summary"`
	Keywords        string    `gorm:"type:varchar(500);comment:匹配关键字" json:"keywords"`
	RelevanceScore  float64   `gorm:"default:0;comment:相关度评分" json:"relevance_score"`
	Likes           int       `gorm:"default:0;comment:点赞数" json:"likes"`
	Comments        int       `gorm:"default:0;comment:评论数" json:"comments"`
	SourceContentID int64     `gorm:"index;comment:关联的原始内容ID" json:"source_content_id"`
	SyncedAt        time.Time `gorm:"autoCreateTime;comment:采集时间" json:"synced_at"`
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName specifies the table name for CollectedContent.
func (CollectedContent) TableName() string {
	return "collected_contents"
}

// CollectRequest 内容采集请求
type CollectRequest struct {
	ContentID  int64    `json:"content_id" binding:"required"` // 关联的原始内容ID
	Platform   Platform `json:"platform"`                      // 限定平台，空=全部
	MaxResults int      `json:"max_results"`                   // 最大结果数，默认10
}

// CollectResponse 内容采集响应
type CollectResponse struct {
	ContentID int64              `json:"content_id"`
	Results   []CollectedContent `json:"results"`
	Total     int                `json:"total"`
	Keywords  []string           `json:"keywords"` // 用于搜索的关键字
}

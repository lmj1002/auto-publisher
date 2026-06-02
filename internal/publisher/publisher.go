package publisher

import (
	"context"
	"time"

	"auto-publisher/internal/model"
)

// Platform 发布平台枚举
type Platform string

const (
	PlatformZhihu       Platform = "zhihu"
	PlatformXiaohongshu Platform = "xiaohongshu"
)

// Publisher 发布器接口
type Publisher interface {
	// Name 返回发布器名称
	Name() string
	// Platform 返回目标平台
	Platform() Platform
	// Validate 发布前校验（登录态、内容格式等）
	Validate(ctx context.Context) error
	// Publish 执行发布
	Publish(ctx context.Context, content *model.Content) (*PublishResult, error)
	// IsAvailable 检查发布器是否可用
	IsAvailable(ctx context.Context) bool
}

// PublishResult 发布结果
type PublishResult struct {
	Success    bool          `json:"success"`
	Platform   string        `json:"platform"`
	ContentID  string        `json:"content_id"`  // 平台上的内容 ID
	URL        string        `json:"url"`          // 发布后的公开链接
	Message    string        `json:"message"`      // 结果描述
	PublishedAt time.Time    `json:"published_at"`
	Duration   time.Duration `json:"duration"`
	Screenshot string        `json:"screenshot,omitempty"` // 发布截图路径
}

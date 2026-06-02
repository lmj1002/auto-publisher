package provider

import (
	"context"
	"time"

	"auto-publisher/internal/model"
)

// ProviderType AI 能力类型
type ProviderType string

const (
	ProviderText  ProviderType = "text"
	ProviderImage ProviderType = "image"
	ProviderVideo ProviderType = "video"
)

// AIProvider 统一 AI 能力接口
type AIProvider interface {
	// Name 返回 Provider 名称
	Name() string
	// Type 返回 AI 能力类型
	Type() ProviderType
	// Generate 执行生成任务
	Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
	// IsAvailable 检查 Provider 是否可用
	IsAvailable(ctx context.Context) bool
}

// GenerateRequest 统一生成请求
type GenerateRequest struct {
	TaskType     ProviderType   `json:"task_type"`     // text / image / video
	Platform     model.Platform `json:"platform"`      // xiaohongshu / zhihu / both
	ContentType  model.ContentType `json:"content_type"` // article / note / idea
	SystemPrompt string         `json:"system_prompt"` // 系统 Prompt
	UserPrompt   string         `json:"user_prompt"`   // 用户 Prompt（选题+观点）
	Options      map[string]any `json:"options"`       // 额外参数

	// 图片生成特定参数
	ImageAspectRatio string `json:"image_aspect_ratio,omitempty"` // 1:1 / 3:4 / 16:9
	ImageStyle       string `json:"image_style,omitempty"`        // 风格描述
}

// GenerateResponse 统一生成响应
type GenerateResponse struct {
	Text     string        `json:"text"`     // 文字内容
	Images   []ImageResult `json:"images"`   // 图片列表
	Model    string        `json:"model"`    // 实际使用的模型名
	Duration time.Duration `json:"duration"` // 耗时
	RawOutput string       `json:"raw_output,omitempty"` // 原始输出
}

// ImageResult 图片生成结果
type ImageResult struct {
	Data     []byte `json:"-"`         // 图片二进制（不序列化）
	Format   string `json:"format"`    // png / jpg / webp
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FilePath string `json:"file_path"` // 本地保存路径
	URL      string `json:"url"`       // 可访问 URL
}

// ParsedTextContent 解析后的文字内容
type ParsedTextContent struct {
	Titles []string // 候选标题
	Body   string   // 正文
	Tags   []string // 标签/话题
}

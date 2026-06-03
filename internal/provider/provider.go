// Package provider defines the AI generation interface and supporting types.
// It provides a unified abstraction over different AI backends (Claude Code CLI, Gemini API, etc.)
// with model routing and automatic fallback.
package provider

import (
	"context"
	"time"

	"auto-publisher/internal/model"
)

// ProviderType classifies an AI provider by its generation capability.
type ProviderType string

const (
	// ProviderText generates text content.
	ProviderText ProviderType = "text"
	// ProviderImage generates images.
	ProviderImage ProviderType = "image"
	// ProviderVideo generates videos (reserved for future use).
	ProviderVideo ProviderType = "video"
)

// AIProvider defines the interface for all AI generation backends.
type AIProvider interface {
	// Name returns a human-readable provider identifier.
	Name() string
	// Type returns the provider capability type.
	Type() ProviderType
	// Generate produces content from the given request.
	Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
	// IsAvailable checks whether the provider is ready to serve requests.
	IsAvailable(ctx context.Context) bool
}

// GenerateRequest contains all parameters for an AI generation call.
type GenerateRequest struct {
	TaskType     ProviderType      `json:"task_type"`
	Platform     model.Platform    `json:"platform"`
	ContentType  model.ContentType `json:"content_type"`
	SystemPrompt string            `json:"system_prompt"` // AI role/style guidelines
	UserPrompt   string            `json:"user_prompt"`   // Topic + key points
	Options      map[string]any    `json:"options"`       // Provider-specific params

	// Image generation parameters
	ImageAspectRatio string `json:"image_aspect_ratio,omitempty"` // e.g. "3:4", "16:9", "1:1"
	ImageStyle       string `json:"image_style,omitempty"`        // Style description
}

// GenerateResponse contains the result of an AI generation call.
type GenerateResponse struct {
	Text      string        `json:"text"`                // Generated text
	Images    []ImageResult `json:"images"`              // Generated images
	Model     string        `json:"model"`               // Model identifier
	Duration  time.Duration `json:"duration"`            // Generation time
	RawOutput string        `json:"raw_output,omitempty"` // Unprocessed output
}

// ImageResult holds a generated image and its metadata.
type ImageResult struct {
	Data     []byte `json:"-"`         // Raw image bytes (not serialized)
	Format   string `json:"format"`    // Image format: png, jpg, webp
	Width    int    `json:"width"`     // Image width in pixels
	Height   int    `json:"height"`    // Image height in pixels
	FilePath string `json:"file_path"` // Local file path
	URL      string `json:"url"`       // Accessible URL
}

// ParsedTextContent holds the structured output from text generation.
type ParsedTextContent struct {
	Titles []string // Candidate titles
	Body   string   // Main content body
	Tags   []string // Hashtag topics
}

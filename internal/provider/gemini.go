package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"auto-publisher/internal/config"
	"auto-publisher/internal/model"
)

// GeminiProvider generates text and images via the Google Gemini API.
// It is the primary image generator and serves as a text fallback when Claude Code is unavailable.
type GeminiProvider struct {
	apiKey     string
	baseURL    string
	textModel  string
	imageModel string
	client     *http.Client
	imageDir   string
}

// NewGeminiProvider creates a Gemini API provider.
func NewGeminiProvider(cfg *config.Provider, imageDir string) *GeminiProvider {
	return &GeminiProvider{
		apiKey:     cfg.GetAPIKey(),
		baseURL:    cfg.BaseURL,
		textModel:  cfg.TextModel,
		imageModel: cfg.ImageModel,
		client:     &http.Client{Timeout: 60 * time.Second},
		imageDir:   imageDir,
	}
}

// Name returns the provider identifier.
func (p *GeminiProvider) Name() string {
	return "gemini"
}

// Type returns ProviderImage (primary capability is image generation).
func (p *GeminiProvider) Type() ProviderType {
	return ProviderImage
}

// IsAvailable checks if the API key is configured.
func (p *GeminiProvider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != ""
}

// Generate routes to text or image generation based on request TaskType.
func (p *GeminiProvider) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	switch req.TaskType {
	case ProviderImage:
		return p.generateImage(ctx, req)
	case ProviderText:
		return p.generateText(ctx, req)
	default:
		return nil, fmt.Errorf("gemini: unsupported task type: %s", req.TaskType)
	}
}

// generateText calls the Gemini API for text generation (fallback mode).
func (p *GeminiProvider) generateText(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()

	fullPrompt := req.SystemPrompt + "\n\n## 选题\n" + req.UserPrompt
	slog.Debug("gemini text generate start", "prompt_len", len(fullPrompt))

	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": fullPrompt}}},
		},
		"generationConfig": map[string]any{
			"temperature":     0.7,
			"maxOutputTokens": 4096,
		},
	}

	apiURL := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		p.baseURL, p.textModel, p.apiKey)

	respBody, err := p.doRequest(ctx, apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("gemini text: %w", err)
	}

	text := extractTextFromGeminiResponse(respBody)

	return &GenerateResponse{
		Text:      text,
		Model:     p.textModel,
		Duration:  time.Since(start),
		RawOutput: text,
	}, nil
}

// generateImage calls the Gemini API for image generation.
func (p *GeminiProvider) generateImage(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()

	imagePrompt := buildImagePrompt(req)
	aspectRatio := req.ImageAspectRatio
	if aspectRatio == "" {
		aspectRatio = "3:4" // Default Xiaohongshu cover ratio
	}

	slog.Debug("gemini image generate start",
		"aspect_ratio", aspectRatio,
		"prompt_len", len(imagePrompt),
	)

	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": imagePrompt}}},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT", "IMAGE"},
			"imageConfig": map[string]any{
				"aspectRatio": aspectRatio,
			},
		},
	}

	apiURL := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		p.baseURL, p.imageModel, p.apiKey)

	respBody, err := p.doRequest(ctx, apiURL, payload)
	if err != nil {
		return nil, fmt.Errorf("gemini image: %w", err)
	}

	images := extractImagesFromGeminiResponse(respBody, p.imageDir)
	text := extractTextFromGeminiResponse(respBody)

	slog.Info("gemini image generated",
		"count", len(images),
		"duration", time.Since(start),
	)

	return &GenerateResponse{
		Text:     text,
		Images:   images,
		Model:    p.imageModel,
		Duration: time.Since(start),
	}, nil
}

// doRequest performs an HTTP POST request to the Gemini API.
func (p *GeminiProvider) doRequest(ctx context.Context, apiURL string, payload any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini API request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API error (HTTP %d): %s",
			resp.StatusCode, string(respBytes))
	}

	var result map[string]any
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return result, nil
}

// buildImagePrompt constructs an image generation prompt from the request.
func buildImagePrompt(req *GenerateRequest) string {
	style := req.ImageStyle
	if style == "" {
		style = "modern tech style, clean and professional"
	}

	aspectDesc := "portrait"
	switch req.ImageAspectRatio {
	case "16:9":
		aspectDesc = "landscape"
	case "1:1":
		aspectDesc = "square"
	case "3:4":
		aspectDesc = "portrait"
	}

	platformNames := map[model.Platform]string{
		model.PlatformXiaohongshu: "Xiaohongshu (RED)",
		model.PlatformZhihu:       "Zhihu (Chinese Quora)",
		model.PlatformBoth:        "social media",
	}

	return fmt.Sprintf(
		"Generate a cover image for a social media post about: %s. "+
			"The image should be %s, %s aspect ratio. "+
			"Suitable for %s platform. "+
			"No text in the image. Simple and eye-catching.",
		req.UserPrompt, style, aspectDesc, platformNames[req.Platform],
	)
}

// extractTextFromGeminiResponse extracts text parts from a Gemini API response.
func extractTextFromGeminiResponse(resp map[string]any) string {
	candidates, ok := resp["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}

	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		return ""
	}

	content, ok := candidate["content"].(map[string]any)
	if !ok {
		return ""
	}

	parts, ok := content["parts"].([]any)
	if !ok {
		return ""
	}

	var textParts []string
	for _, part := range parts {
		p, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := p["text"].(string); ok {
			textParts = append(textParts, t)
		}
	}

	return joinStrings(textParts, "\n")
}

// extractImagesFromGeminiResponse extracts inline images from a Gemini API response.
func extractImagesFromGeminiResponse(resp map[string]any, imageDir string) []ImageResult {
	var images []ImageResult

	candidates, ok := resp["candidates"].([]any)
	if !ok {
		return images
	}

	for _, c := range candidates {
		candidate, ok := c.(map[string]any)
		if !ok {
			continue
		}
		content, ok := candidate["content"].(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		for i, part := range parts {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			inlineData, ok := p["inlineData"].(map[string]any)
			if !ok {
				continue
			}

			mimeType, _ := inlineData["mimeType"].(string)
			data, _ := inlineData["data"].(string)
			if data == "" {
				continue
			}

			imgData, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				continue
			}

			// Determine file format
			format := "png"
			switch mimeType {
			case "image/jpeg":
				format = "jpg"
			case "image/webp":
				format = "webp"
			}

			// Save to disk
			if err := os.MkdirAll(imageDir, 0755); err != nil {
				slog.Warn("create image dir failed", "path", imageDir, "error", err)
				continue
			}
			filename := filepath.Join(imageDir,
				fmt.Sprintf("gemini_%d_%d.%s", time.Now().Unix(), i, format))
			if err := os.WriteFile(filename, imgData, 0644); err != nil {
				slog.Warn("save image failed", "path", filename, "error", err)
				continue
			}

			images = append(images, ImageResult{
				Data:     imgData,
				Format:   format,
				FilePath: filename,
				URL:      "/images/" + filepath.Base(filename),
			})
		}
	}

	return images
}

// joinStrings concatenates strings with a separator.
func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

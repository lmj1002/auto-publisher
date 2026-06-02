package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"auto-publisher/internal/config"
	"auto-publisher/internal/model"
)

// GeminiProvider Gemini Provider（支持图片生成 + 文字降级）
type GeminiProvider struct {
	apiKey     string
	baseURL    string
	textModel  string
	imageModel string
	client     *http.Client
	imageDir   string
}

// NewGeminiProvider 创建 Gemini Provider
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

// Name 返回名称
func (p *GeminiProvider) Name() string {
	return "gemini"
}

// Type 返回 AI 类型（主类型为 image，也可做 text 降级）
func (p *GeminiProvider) Type() ProviderType {
	return ProviderImage
}

// IsAvailable 检查 Gemini API 是否可用
func (p *GeminiProvider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != ""
}

// Generate 生成内容（根据 TaskType 路由到文字或图片生成）
func (p *GeminiProvider) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	switch req.TaskType {
	case ProviderImage:
		return p.generateImage(ctx, req)
	case ProviderText:
		return p.generateText(ctx, req)
	default:
		return nil, fmt.Errorf("gemini 不支持的任务类型: %s", req.TaskType)
	}
}

// GenerateText 作为文字 Provider 降级使用
func (p *GeminiProvider) GenerateText(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	return p.generateText(ctx, req)
}

// generateText 调用 Gemini 生成文字
func (p *GeminiProvider) generateText(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()

	fullPrompt := req.SystemPrompt + "\n\n## 选题\n" + req.UserPrompt

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": fullPrompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.7,
			"maxOutputTokens": 4096,
		},
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		p.baseURL, p.textModel, p.apiKey)

	respBody, err := p.doRequest(ctx, url, payload)
	if err != nil {
		return nil, err
	}

	text := extractTextFromGeminiResponse(respBody)

	return &GenerateResponse{
		Text:      text,
		Model:     p.textModel,
		Duration:  time.Since(start),
		RawOutput: text,
	}, nil
}

// generateImage 调用 Gemini 生成图片
func (p *GeminiProvider) generateImage(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()

	// 构建图片生成 Prompt（中英混合描述更稳定）
	imagePrompt := buildImagePrompt(req)

	aspectRatio := req.ImageAspectRatio
	if aspectRatio == "" {
		aspectRatio = "3:4" // 默认小红书封面比例
	}

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": imagePrompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"responseModalities": []string{"TEXT", "IMAGE"},
			"imageConfig": map[string]interface{}{
				"aspectRatio": aspectRatio,
			},
		},
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s",
		p.baseURL, p.imageModel, p.apiKey)

	respBody, err := p.doRequest(ctx, url, payload)
	if err != nil {
		return nil, err
	}

	// 提取图片数据
	images := extractImagesFromGeminiResponse(respBody, p.imageDir)

	// 提取文字说明
	text := extractTextFromGeminiResponse(respBody)

	return &GenerateResponse{
		Text:     text,
		Images:   images,
		Model:    p.imageModel,
		Duration: time.Since(start),
	}, nil
}

// doRequest 执行 HTTP 请求
func (p *GeminiProvider) doRequest(ctx context.Context, url string, payload interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Gemini API 失败: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini API 返回错误 (%d): %s", resp.StatusCode, string(respBytes))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return result, nil
}

// buildImagePrompt 构建图片生成 Prompt
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

	return fmt.Sprintf(
		"Generate a cover image for a social media post about: %s. "+
			"The image should be %s, %s aspect ratio. "+
			"Suitable for %s platform. "+
			"No text in the image. Simple and eye-catching.",
		req.UserPrompt, style, aspectDesc,
		map[model.Platform]string{
			model.PlatformXiaohongshu: "Xiaohongshu (RED)",
			model.PlatformZhihu:       "Zhihu (Chinese Quora)",
			model.PlatformBoth:        "social media",
		}[req.Platform],
	)
}

// extractTextFromGeminiResponse 从 Gemini 响应中提取文字
func extractTextFromGeminiResponse(resp map[string]interface{}) string {
	candidates, ok := resp["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return ""
	}

	parts, ok := content["parts"].([]interface{})
	if !ok {
		return ""
	}

	var textParts []string
	for _, part := range parts {
		p, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if t, ok := p["text"].(string); ok {
			textParts = append(textParts, t)
		}
	}

	return bytes.NewBufferString(stringsJoin(textParts, "\n")).String()
}

// extractImagesFromGeminiResponse 从 Gemini 响应中提取图片
func extractImagesFromGeminiResponse(resp map[string]interface{}, imageDir string) []ImageResult {
	var images []ImageResult

	candidates, ok := resp["candidates"].([]interface{})
	if !ok {
		return images
	}

	for _, c := range candidates {
		candidate, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := candidate["content"].(map[string]interface{})
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]interface{})
		if !ok {
			continue
		}

		for i, part := range parts {
			p, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			// Gemini 返回 inlineData 格式
			inlineData, ok := p["inlineData"].(map[string]interface{})
			if !ok {
				continue
			}

			mimeType, _ := inlineData["mimeType"].(string)
			data, _ := inlineData["data"].(string)
			if data == "" {
				continue
			}

			// Base64 解码
			imgData, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				continue
			}

			// 确定文件格式
			format := "png"
			switch mimeType {
			case "image/jpeg":
				format = "jpg"
			case "image/webp":
				format = "webp"
			}

			// 保存到本地
			os.MkdirAll(imageDir, 0755)
			filename := filepath.Join(imageDir, fmt.Sprintf("gemini_%d_%d.%s", time.Now().Unix(), i, format))
			os.WriteFile(filename, imgData, 0644)

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

// stringsJoin 简单的字符串拼接（避免引入额外依赖）
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

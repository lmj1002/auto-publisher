package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"auto-publisher/internal/model"
)

// ZhihuPublisher 知乎发布器
type ZhihuPublisher struct {
	client    *http.Client
	cookieStr string
	userAgent string
}

// ZhihuAPIs 知乎 API 端点
var ZhihuAPIs = map[string]string{
	"check_login": "https://www.zhihu.com/api/v4/me",
	"create_draft": "https://zhuanlan.zhihu.com/api/articles",
	"upload_image": "https://zhuanlan.zhihu.com/api/images",
}

// NewZhihuPublisher 创建知乎发布器
// cookieSource: cookie 字符串或文件路径
func NewZhihuPublisher(cookieSource string) *ZhihuPublisher {
	cookieStr := cookieSource
	// 如果 cookieSource 是一个文件路径，读取文件内容
	if strings.HasSuffix(cookieSource, ".txt") || strings.HasSuffix(cookieSource, ".cookie") {
		if data, err := os.ReadFile(cookieSource); err == nil {
			cookieStr = strings.TrimSpace(string(data))
		}
	}

	return &ZhihuPublisher{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		cookieStr: cookieStr,
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
	}
}

// Name 名称
func (p *ZhihuPublisher) Name() string {
	return "zhihu"
}

// Platform 平台
func (p *ZhihuPublisher) Platform() Platform {
	return PlatformZhihu
}

// IsAvailable 检查是否可用
func (p *ZhihuPublisher) IsAvailable(ctx context.Context) bool {
	if p.cookieStr == "" {
		return false
	}
	return p.Validate(ctx) == nil
}

// Validate 校验登录态
func (p *ZhihuPublisher) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", ZhihuAPIs["check_login"], nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求知乎 API 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("知乎登录态失效 (HTTP %d): %s", resp.StatusCode, string(body))
	}

	// 解析响应检查是否登录
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("解析登录状态失败: %w", err)
	}

	// 检查是否有 error 字段
	if errMsg, ok := result["error"]; ok {
		return fmt.Errorf("知乎登录态失效: %v", errMsg)
	}

	log.Println("[Zhihu] ✅ 登录态有效")
	return nil
}

// Publish 发布内容到知乎
func (p *ZhihuPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	// 1. 先校验登录态
	if err := p.Validate(ctx); err != nil {
		return p.failResult("登录态校验失败", err, start), err
	}

	// 2. 根据内容类型分发
	switch content.Type {
	case model.TypeArticle:
		return p.publishArticle(ctx, content, start)
	case model.TypeIdea:
		return p.publishIdea(ctx, content, start)
	default:
		return p.publishArticle(ctx, content, start) // 默认按文章发布
	}
}

// publishArticle 发布知乎文章
func (p *ZhihuPublisher) publishArticle(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	title := content.ZHTitle
	if title == "" && len(content.Topic) > 0 {
		title = content.Topic
	}
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
	}

	// 1. 创建草稿
	draftPayload := map[string]interface{}{
		"title":     title,
		"content":   body,
		"titleImage": "",
	}
	draftID, err := p.createDraft(ctx, draftPayload)
	if err != nil {
		return p.failResult("创建草稿失败", err, start), err
	}
	log.Printf("[Zhihu] 草稿创建成功: %s", draftID)

	// 2. 发布草稿
	if err := p.publishDraft(ctx, draftID); err != nil {
		return p.failResult("发布失败", err, start), err
	}

	url := fmt.Sprintf("https://zhuanlan.zhihu.com/p/%s", draftID)
	log.Printf("[Zhihu] 文章发布成功: %s", url)

	return &PublishResult{
		Success:     true,
		Platform:    "zhihu",
		ContentID:   draftID,
		URL:         url,
		Message:     "发布成功",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// publishIdea 发布知乎想法（短内容）
func (p *ZhihuPublisher) publishIdea(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
	}

	payload := map[string]interface{}{
		"content": body,
	}

	// POST https://www.zhihu.com/api/v4/pins
	url := "https://www.zhihu.com/api/v4/pins"
	respBody, err := p.doAPI(ctx, "POST", url, payload)
	if err != nil {
		return p.failResult("发布想法失败", err, start), err
	}

	pinID := extractID(respBody, "id")
	log.Printf("[Zhihu] 想法发布成功: %s", pinID)

	return &PublishResult{
		Success:     true,
		Platform:    "zhihu",
		ContentID:   pinID,
		URL:         fmt.Sprintf("https://www.zhihu.com/pin/%s", pinID),
		Message:     "发布成功",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// createDraft 创建草稿文章
func (p *ZhihuPublisher) createDraft(ctx context.Context, payload map[string]interface{}) (string, error) {
	respBody, err := p.doAPI(ctx, "POST", ZhihuAPIs["create_draft"], payload)
	if err != nil {
		return "", err
	}

	id := extractID(respBody, "id")
	if id == "" {
		return "", fmt.Errorf("未能从响应中提取文章 ID: %s", string(respBody))
	}
	return id, nil
}

// publishDraft 发布草稿
func (p *ZhihuPublisher) publishDraft(ctx context.Context, draftID string) error {
	url := fmt.Sprintf("%s/%s/publish", ZhihuAPIs["create_draft"], draftID)
	_, err := p.doAPI(ctx, "PUT", url, map[string]interface{}{
		"status": "published",
	})
	return err
}

// doAPI 执行知乎 API 请求
func (p *ZhihuPublisher) doAPI(ctx context.Context, method, url string, payload interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("序列化请求失败: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("知乎 API 返回错误 (HTTP %d): %s", resp.StatusCode, truncate(string(respBytes), 200))
	}

	return respBytes, nil
}

// setHeaders 设置请求头
func (p *ZhihuPublisher) setHeaders(req *http.Request) {
	req.Header.Set("Cookie", p.cookieStr)
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", "https://zhuanlan.zhihu.com")
	req.Header.Set("Referer", "https://zhuanlan.zhihu.com/")
}

// failResult 生成失败结果
func (p *ZhihuPublisher) failResult(msg string, err error, start time.Time) *PublishResult {
	return &PublishResult{
		Success:     false,
		Platform:    "zhihu",
		Message:     fmt.Sprintf("%s: %v", msg, err),
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}
}

// extractID 从 JSON 响应中提取 ID 字段
func extractID(data []byte, key string) string {
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	if id, ok := result[key]; ok {
		return fmt.Sprintf("%v", id)
	}
	return ""
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

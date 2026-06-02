package provider

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"auto-publisher/internal/model"
)

var mu sync.Mutex

// ClaudeCodeProvider 通过 Claude Code CLI 生成文字内容
type ClaudeCodeProvider struct {
	workDir   string
	timeout   time.Duration
	promptDir string
}

// NewClaudeCodeProvider 创建 Claude Code CLI Provider
func NewClaudeCodeProvider(workDir string, timeout time.Duration) *ClaudeCodeProvider {
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &ClaudeCodeProvider{
		workDir:   workDir,
		timeout:   timeout,
		promptDir: filepath.Join(os.TempDir(), "auto-publisher-prompts"),
	}
}

// Name 返回名称
func (p *ClaudeCodeProvider) Name() string {
	return "claude-code"
}

// Type 返回 AI 类型
func (p *ClaudeCodeProvider) Type() ProviderType {
	return ProviderText
}

// IsAvailable 检查 claude CLI 是否可用
func (p *ClaudeCodeProvider) IsAvailable(ctx context.Context) bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Generate 通过调用 claude CLI 生成内容
func (p *ClaudeCodeProvider) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()

	// 确保 prompt 目录存在
	os.MkdirAll(p.promptDir, 0755)

	// 构建完整的 prompt
	fullPrompt := p.buildFullPrompt(req)

	// 并发控制：Claude CLI 同时只允许运行一个实例
	mu.Lock()
	defer mu.Unlock()

	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// 调用 claude CLI
	cmd := exec.CommandContext(ctx, "claude",
		"-p", fullPrompt,
		"--output-format", "text",
		"--max-turns", "1",
	)
	cmd.Dir = p.workDir

	// 设置环境变量（避免交互式提示）
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_HEADLESS=true")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("claude CLI 执行失败: %w\nstderr: %s", err, stderr.String())
	}

	rawOutput := stdout.String()

	// 解析输出
	parsed := ParseMarkedOutput(rawOutput, req.Platform)

	return &GenerateResponse{
		Text:      parsed.Body,
		Model:     "claude-code-cli",
		Duration:  time.Since(start),
		RawOutput: rawOutput,
	}, nil
}

// buildFullPrompt 构建完整的 Prompt
func (p *ClaudeCodeProvider) buildFullPrompt(req *GenerateRequest) string {
	var sb strings.Builder

	sb.WriteString(req.SystemPrompt)
	sb.WriteString("\n\n---\n\n")
	sb.WriteString("## 选题\n")
	sb.WriteString(req.UserPrompt)

	if req.Platform == model.PlatformBoth {
		sb.WriteString("\n\n注意：请分别为小红书和知乎各生成一份内容。")
	}

	return sb.String()
}

// ParseMarkedOutput 解析带有标记的 AI 输出（导出供外部使用）
// 支持的标记格式：
//
//	---TITLE---
//	标题1：xxx
//	---BODY---
//	正文内容
//	---TAGS---
//	#xxx #xxx
var prefixRegex = regexp.MustCompile(`^(标题\d+|主标题|备选标题)[：:]\s*`)
var tagRegex = regexp.MustCompile(`#\S+`)

func ParseMarkedOutput(raw string, platform model.Platform) *ParsedTextContent {
	result := &ParsedTextContent{}

	// 用字符串分割替代 lookahead 正则（Go 的 regexp 不支持 (?=...)）
	titleSection := extractSection(raw, "---TITLE---", "---BODY---")
	bodySection := extractSection(raw, "---BODY---", "---TAGS---")
	tagsSection := extractSection(raw, "---TAGS---", "")
	if tagsSection == "" {
		// 知乎格式使用 ---TOPICS---
		tagsSection = extractSection(raw, "---TOPICS---", "")
	}

	// 解析标题
	if titleSection != "" {
		lines := strings.Split(strings.TrimSpace(titleSection), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			line = prefixRegex.ReplaceAllString(line, "")
			if line != "" {
				result.Titles = append(result.Titles, line)
			}
		}
	}

	// 解析正文
	if bodySection != "" {
		result.Body = strings.TrimSpace(bodySection)
	}

	// 解析标签
	if tagsSection != "" {
		for _, tag := range tagRegex.FindAllString(tagsSection, -1) {
			result.Tags = append(result.Tags, tag)
		}
	}

	// 如果没有匹配到标记格式，直接把全部输出作为正文
	if result.Body == "" && len(result.Titles) == 0 {
		result.Body = strings.TrimSpace(raw)
	}

	return result
}

// extractSection 提取两个标记之间的内容
// startMarker 为空时从头开始；endMarker 为空时取到末尾
func extractSection(text, startMarker, endMarker string) string {
	startIdx := 0
	if startMarker != "" {
		idx := strings.Index(text, startMarker)
		if idx == -1 {
			return ""
		}
		startIdx = idx + len(startMarker)
		// 跳过换行
		if startIdx < len(text) && text[startIdx] == '\n' {
			startIdx++
		}
	}

	if endMarker == "" {
		return text[startIdx:]
	}

	endIdx := strings.Index(text[startIdx:], endMarker)
	if endIdx == -1 {
		return text[startIdx:]
	}

	return text[startIdx : startIdx+endIdx]
}

package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"auto-publisher/internal/model"
)

// claudeCLIMutex prevents concurrent Claude CLI invocations.
var claudeCLIMutex sync.Mutex

// ClaudeCodeProvider generates text using the local Claude Code CLI.
// It invokes `claude` as a subprocess, locked to one concurrent invocation.
type ClaudeCodeProvider struct {
	workDir string
	timeout time.Duration
}

// NewClaudeCodeProvider creates a Claude Code CLI provider.
// workDir is the project directory for the CLI context.
// timeout caps each CLI invocation; defaults to 120s if zero.
func NewClaudeCodeProvider(workDir string, timeout time.Duration) *ClaudeCodeProvider {
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &ClaudeCodeProvider{
		workDir: workDir,
		timeout: timeout,
	}
}

// Name returns the provider identifier.
func (p *ClaudeCodeProvider) Name() string {
	return "claude-code"
}

// Type returns ProviderText.
func (p *ClaudeCodeProvider) Type() ProviderType {
	return ProviderText
}

// IsAvailable checks if the `claude` CLI is on PATH.
func (p *ClaudeCodeProvider) IsAvailable(ctx context.Context) bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Generate produces text by running the Claude CLI as a subprocess.
func (p *ClaudeCodeProvider) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	start := time.Now()
	fullPrompt := p.buildFullPrompt(req)
	slog.Debug("claude generate start", "prompt_len", len(fullPrompt), "platform", req.Platform)

	// Global mutex — only one CLI instance at a time
	claudeCLIMutex.Lock()
	defer claudeCLIMutex.Unlock()

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude",
		"-p", fullPrompt,
		"--output-format", "text",
		"--max-turns", "1",
	)
	cmd.Dir = p.workDir
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_HEADLESS=true")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		dur := time.Since(start)

		// Detect timeout vs other failures
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("claude CLI timed out",
				"timeout", p.timeout,
				"prompt_len", len(fullPrompt),
				"duration", dur,
			)
			return nil, fmt.Errorf("claude CLI timed out after %v (max_turns=1)", p.timeout)
		}

		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		slog.Error("claude CLI subprocess failed",
			"error", err,
			"stderr", stderrStr,
			"exit_code", exitCode,
			"duration", dur,
			"prompt_len", len(fullPrompt),
			"platform", req.Platform,
			"work_dir", p.workDir,
		)
		return nil, fmt.Errorf("claude CLI: %w\nstderr: %s", err, stderrStr)
	}

	rawOutput := stdout.String()
	slog.Debug("claude generate complete",
		"output_len", len(rawOutput),
		"duration", time.Since(start),
	)

	return &GenerateResponse{
		Text:      rawOutput,
		Model:     "claude-code-cli",
		Duration:  time.Since(start),
		RawOutput: rawOutput,
	}, nil
}

// buildFullPrompt constructs the complete prompt from system and user components.
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

// prefixRegex matches title number prefixes like "标题1:", "主标题:", "备选标题:".
var prefixRegex = regexp.MustCompile(`^(标题\d+|主标题|备选标题)[：:]\s*`)

// tagRegex matches hashtag-style tags (#xxx).
var tagRegex = regexp.MustCompile(`#\S+`)

// ParseMarkedOutput parses AI-generated text with marker-delimited sections.
// Supported markers: ---TITLE---, ---BODY---, ---TAGS---, ---TOPICS--- (Zhihu).
// If no markers are found, the entire output is treated as the body.
func ParseMarkedOutput(raw string, platform model.Platform) *ParsedTextContent {
	result := &ParsedTextContent{}

	// Extract sections using string scanning (Go regexp doesn't support lookahead)
	titleSection := extractSection(raw, "---TITLE---", "---BODY---")
	bodySection := extractSection(raw, "---BODY---", "---TAGS---")
	tagsSection := extractSection(raw, "---TAGS---", "")
	if tagsSection == "" {
		// Zhihu format uses ---TOPICS--- instead of ---TAGS---
		tagsSection = extractSection(raw, "---TOPICS---", "")
	}

	// Parse titles
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

	// Parse body
	if bodySection != "" {
		result.Body = strings.TrimSpace(bodySection)
	}

	// Parse tags
	if tagsSection != "" {
		for _, tag := range tagRegex.FindAllString(tagsSection, -1) {
			result.Tags = append(result.Tags, tag)
		}
	}

	// Fallback: if no markers matched, treat entire output as body
	if result.Body == "" && len(result.Titles) == 0 {
		result.Body = strings.TrimSpace(raw)
	}

	return result
}

// extractSection extracts text between startMarker and endMarker.
// If startMarker is empty, extracts from the beginning.
// If endMarker is empty, extracts to the end.
func extractSection(text, startMarker, endMarker string) string {
	startIdx := 0
	if startMarker != "" {
		idx := strings.Index(text, startMarker)
		if idx == -1 {
			return ""
		}
		startIdx = idx + len(startMarker)
		// Skip the newline after the marker
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

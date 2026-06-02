package provider

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
)

// PromptManager Prompt 模板管理器
type PromptManager struct {
	promptDir string
	cache     map[string]*PromptTemplate
	mu        sync.RWMutex
}

// PromptTemplate 单个 Prompt 模板
type PromptTemplate struct {
	Name     string // 模板名称（文件名，不含扩展名）
	FilePath string // 文件路径
	Content  string // 模板原始内容
	tmpl     *template.Template
}

// NewPromptManager 创建 Prompt 管理器
func NewPromptManager(promptDir string) *PromptManager {
	return &PromptManager{
		promptDir: promptDir,
		cache:     make(map[string]*PromptTemplate),
	}
}

// LoadAll 加载所有 Prompt 模板
func (m *PromptManager) LoadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(m.promptDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在不算错误
		}
		return fmt.Errorf("读取 Prompt 目录失败: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		filePath := filepath.Join(m.promptDir, entry.Name())

		if err := m.loadFile(name, filePath); err != nil {
			return fmt.Errorf("加载 %s 失败: %w", name, err)
		}
	}

	return nil
}

// loadFile 加载单个 Prompt 文件
func (m *PromptManager) loadFile(name, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	content := string(data)
	tmpl, err := template.New(name).Parse(content)
	if err != nil {
		// 如果解析模板失败，当作纯文本存储
		tmpl = nil
	}

	m.cache[name] = &PromptTemplate{
		Name:     name,
		FilePath: filePath,
		Content:  content,
		tmpl:     tmpl,
	}

	return nil
}

// Get 获取 Prompt 模板内容（不渲染）
func (m *PromptManager) Get(name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pt, ok := m.cache[name]
	if !ok {
		return "", fmt.Errorf("Prompt 模板 '%s' 不存在", name)
	}

	return pt.Content, nil
}

// Render 获取并渲染 Prompt 模板
func (m *PromptManager) Render(name string, data interface{}) (string, error) {
	m.mu.RLock()
	pt, ok := m.cache[name]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("Prompt 模板 '%s' 不存在", name)
	}

	if pt.tmpl == nil {
		return pt.Content, nil
	}

	var buf bytes.Buffer
	if err := pt.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("渲染模板 '%s' 失败: %w", name, err)
	}

	return buf.String(), nil
}

// GetSystemPrompt 获取平台 System Prompt（优先从文件读取，fallback 到 config）
func (m *PromptManager) GetSystemPrompt(platform string, fallback string) string {
	name := platform + "_system"
	if content, err := m.Get(name); err == nil && content != "" {
		return content
	}
	return fallback
}

// GetCoverPrompt 获取封面图 Prompt（模板渲染）
func (m *PromptManager) GetCoverPrompt(data CoverPromptData) (string, error) {
	return m.Render("image_cover", data)
}

// CoverPromptData 封面图 Prompt 数据
type CoverPromptData struct {
	Topic       string
	Platform    string
	Style       string
	AspectRatio string
	ColorScheme string
}

// Reload 重新加载所有模板（热更新）
func (m *PromptManager) Reload() error {
	m.mu.Lock()
	m.cache = make(map[string]*PromptTemplate)
	m.mu.Unlock()
	return m.LoadAll()
}

// List 列出所有已加载的模板
func (m *PromptManager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	for name := range m.cache {
		names = append(names, name)
	}
	return names
}

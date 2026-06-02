package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromptManager_LoadAndGet(t *testing.T) {
	// 创建临时目录和文件
	tmpDir := t.TempDir()

	testContent := "你是一个测试助手。\n\n## 创作要求\n- 要求1\n- 要求2"
	os.WriteFile(filepath.Join(tmpDir, "test_system.md"), []byte(testContent), 0644)

	pm := NewPromptManager(tmpDir)
	err := pm.LoadAll()
	if err != nil {
		t.Fatalf("加载模板失败: %v", err)
	}

	content, err := pm.Get("test_system")
	if err != nil {
		t.Fatalf("获取模板失败: %v", err)
	}

	if content != testContent {
		t.Errorf("模板内容不匹配:\n期望: %s\n实际: %s", testContent, content)
	}
}

func TestPromptManager_Render(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建可渲染的模板
	tmplContent := "Topic: {{.Topic}}\nPlatform: {{.Platform}}\nStyle: {{.Style}}"
	os.WriteFile(filepath.Join(tmpDir, "render_test.md"), []byte(tmplContent), 0644)

	pm := NewPromptManager(tmpDir)
	pm.LoadAll()

	data := CoverPromptData{
		Topic:    "Go 语言优势",
		Platform: "知乎",
		Style:    "科技风",
	}

	result, err := pm.Render("render_test", data)
	if err != nil {
		t.Fatalf("渲染模板失败: %v", err)
	}

	expected := "Topic: Go 语言优势\nPlatform: 知乎\nStyle: 科技风"
	if result != expected {
		t.Errorf("渲染结果不匹配:\n期望: %s\n实际: %s", expected, result)
	}
}

func TestPromptManager_GetSystemPrompt_PreferFile(t *testing.T) {
	tmpDir := t.TempDir()

	fileContent := "这是从文件加载的 System Prompt"
	os.WriteFile(filepath.Join(tmpDir, "xiaohongshu_system.md"), []byte(fileContent), 0644)

	pm := NewPromptManager(tmpDir)
	pm.LoadAll()

	fallback := "这是 fallback 内容"
	result := pm.GetSystemPrompt("xiaohongshu", fallback)

	if result != fileContent {
		t.Errorf("应该优先使用文件内容:\n期望: %s\n实际: %s", fileContent, result)
	}
}

func TestPromptManager_GetSystemPrompt_Fallback(t *testing.T) {
	tmpDir := t.TempDir()

	pm := NewPromptManager(tmpDir)
	pm.LoadAll()

	fallback := "这是 fallback 内容"
	result := pm.GetSystemPrompt("nonexistent", fallback)

	if result != fallback {
		t.Errorf("文件不存在时应使用 fallback:\n期望: %s\n实际: %s", fallback, result)
	}
}

func TestPromptManager_Reload(t *testing.T) {
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "reload_test.md"), []byte("version 1"), 0644)

	pm := NewPromptManager(tmpDir)
	pm.LoadAll()

	// 修改文件
	os.WriteFile(filepath.Join(tmpDir, "reload_test.md"), []byte("version 2"), 0644)

	// 重新加载
	pm.Reload()

	content, _ := pm.Get("reload_test")
	if content != "version 2" {
		t.Errorf("重新加载后内容应为 version 2，实际: %s", content)
	}
}

func TestPromptManager_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	pm := NewPromptManager(tmpDir)
	err := pm.LoadAll()

	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}

	if len(pm.List()) != 0 {
		t.Error("空目录应返回空列表")
	}
}

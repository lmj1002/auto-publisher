package provider

import (
	"context"
	"testing"

	"auto-publisher/internal/config"
	"auto-publisher/internal/model"
)

// mockProvider 用于测试的模拟 Provider
type mockProvider struct {
	name    string
	ptype   ProviderType
	avail   bool
	called  bool
}

func (m *mockProvider) Name() string            { return m.name }
func (m *mockProvider) Type() ProviderType       { return m.ptype }
func (m *mockProvider) IsAvailable(ctx context.Context) bool { return m.avail }
func (m *mockProvider) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	m.called = true
	return &GenerateResponse{Text: "mock output", Model: m.name}, nil
}

func TestRouter_SelectDefaultProvider(t *testing.T) {
	cfg := &config.ModelsConfig{
		DefaultTextProvider: "claude-code",
		Routing: config.RoutingConfig{
			Text:  config.RouteRule{Default: "claude-code", Fallback: "gemini"},
			Image: config.RouteRule{Default: "gemini"},
		},
	}

	providers := map[string]AIProvider{
		"claude-code": &mockProvider{name: "claude-code", ptype: ProviderText, avail: true},
		"gemini":      &mockProvider{name: "gemini", ptype: ProviderImage, avail: true},
	}

	router := NewRouter(cfg, providers)
	ctx := context.Background()

	// 测试文字任务路由到 claude-code
	req := &GenerateRequest{
		TaskType: ProviderText,
		Platform: model.PlatformXiaohongshu,
	}

	resp, err := router.Generate(ctx, req)
	if err != nil {
		t.Fatalf("路由失败: %v", err)
	}

	if resp.Model != "claude-code" {
		t.Errorf("期望路由到 claude-code，实际 %s", resp.Model)
	}
}

func TestRouter_FallbackWhenDefaultUnavailable(t *testing.T) {
	cfg := &config.ModelsConfig{
		DefaultTextProvider: "claude-code",
		Routing: config.RoutingConfig{
			Text:  config.RouteRule{Default: "claude-code", Fallback: "gemini"},
			Image: config.RouteRule{Default: "gemini"},
		},
	}

	// claude-code 不可用，gemini 可用
	geminiMock := &mockProvider{name: "gemini", ptype: ProviderImage, avail: true}

	providers := map[string]AIProvider{
		"claude-code": &mockProvider{name: "claude-code", ptype: ProviderText, avail: false},
		"gemini":      geminiMock,
	}

	router := NewRouter(cfg, providers)
	ctx := context.Background()

	req := &GenerateRequest{
		TaskType: ProviderText,
		Platform: model.PlatformXiaohongshu,
	}

	resp, err := router.Generate(ctx, req)
	if err != nil {
		t.Fatalf("降级路由失败: %v", err)
	}

	if resp.Model != "gemini" {
		t.Errorf("期望降级到 gemini，实际 %s", resp.Model)
	}
}

func TestRouter_NoAvailableProvider(t *testing.T) {
	cfg := &config.ModelsConfig{
		Routing: config.RoutingConfig{
			Text:  config.RouteRule{Default: "claude-code"},
			Image: config.RouteRule{Default: "gemini"},
		},
	}

	providers := map[string]AIProvider{
		"claude-code": &mockProvider{name: "claude-code", ptype: ProviderText, avail: false},
	}

	router := NewRouter(cfg, providers)
	ctx := context.Background()

	req := &GenerateRequest{
		TaskType: ProviderText,
		Platform: model.PlatformXiaohongshu,
	}

	_, err := router.Generate(ctx, req)
	if err == nil {
		t.Error("期望返回错误，但没有")
	}
}

func TestRouter_ListProviders(t *testing.T) {
	cfg := &config.ModelsConfig{}
	providers := map[string]AIProvider{
		"claude-code": &mockProvider{name: "claude-code", ptype: ProviderText, avail: true},
		"gemini":      &mockProvider{name: "gemini", ptype: ProviderImage, avail: false},
	}

	router := NewRouter(cfg, providers)
	ctx := context.Background()

	list := router.ListProviders(ctx)
	if len(list) != 2 {
		t.Errorf("期望 2 个 Provider，实际 %d 个", len(list))
	}

	for _, info := range list {
		switch info.Name {
		case "claude-code":
			if !info.Available {
				t.Error("claude-code 应该可用")
			}
		case "gemini":
			if info.Available {
				t.Error("gemini 应该不可用")
			}
		}
	}
}

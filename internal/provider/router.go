package provider

import (
	"context"
	"fmt"
	"log"
	"sync"

	"auto-publisher/internal/config"
)

// Router 模型路由器
type Router struct {
	providers map[string]AIProvider
	cfg       *config.ModelsConfig
	mu        sync.RWMutex
}

// NewRouter 创建路由器
func NewRouter(cfg *config.ModelsConfig, providers map[string]AIProvider) *Router {
	return &Router{
		providers: providers,
		cfg:       cfg,
	}
}

// Register 注册 Provider
func (r *Router) Register(name string, p AIProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
}

// Generate 执行生成任务（自动路由）
func (r *Router) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	provider, err := r.selectProvider(ctx, req.TaskType)
	if err != nil {
		return nil, fmt.Errorf("模型路由失败: %w", err)
	}

	log.Printf("[Router] 任务类型=%s → Provider=%s", req.TaskType, provider.Name())

	return provider.Generate(ctx, req)
}

// GenerateWithProvider 指定 Provider 生成
func (r *Router) GenerateWithProvider(ctx context.Context, providerName string, req *GenerateRequest) (*GenerateResponse, error) {
	r.mu.RLock()
	p, ok := r.providers[providerName]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("Provider '%s' 不存在", providerName)
	}

	return p.Generate(ctx, req)
}

// selectProvider 按优先级选择 Provider
// 1. routing 配置指定的 default
// 2. 如果 default 不可用，尝试 fallback
// 3. 如果都不可用，返回错误
func (r *Router) selectProvider(ctx context.Context, taskType ProviderType) (AIProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 获取路由规则
	rule := r.getRouteRule(taskType)

	// 优先尝试默认 Provider
	if rule.Default != "" {
		if p, ok := r.providers[rule.Default]; ok && p.IsAvailable(ctx) {
			return p, nil
		}
		log.Printf("[Router] 默认 Provider '%s' 不可用，尝试降级", rule.Default)
	}

	// 尝试降级 Provider
	if rule.Fallback != "" {
		if p, ok := r.providers[rule.Fallback]; ok && p.IsAvailable(ctx) {
			log.Printf("[Router] 降级到 Provider '%s'", rule.Fallback)
			return p, nil
		}
	}

	// 遍历所有已注册的 Provider，找同类型的可用 Provider
	for name, p := range r.providers {
		if p.Type() == taskType && p.IsAvailable(ctx) {
			log.Printf("[Router] 使用备用 Provider '%s'", name)
			return p, nil
		}
	}

	return nil, fmt.Errorf("没有可用的 %s 类型 Provider", taskType)
}

// getRouteRule 获取路由规则
func (r *Router) getRouteRule(taskType ProviderType) config.RouteRule {
	switch taskType {
	case ProviderText:
		return r.cfg.Routing.Text
	case ProviderImage:
		return r.cfg.Routing.Image
	case ProviderVideo:
		return r.cfg.Routing.Video
	default:
		return config.RouteRule{}
	}
}

// ListProviders 列出所有已注册的 Provider
func (r *Router) ListProviders(ctx context.Context) []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var infos []ProviderInfo
	for name, p := range r.providers {
		infos = append(infos, ProviderInfo{
			Name:        name,
			Type:        string(p.Type()),
			Available:   p.IsAvailable(ctx),
		})
	}
	return infos
}

// ProviderInfo Provider 信息
type ProviderInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
}

package provider

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"auto-publisher/internal/config"
)

// Router manages AI provider selection with automatic routing and fallback.
// It selects providers based on task type (text/image/video) and configured routing rules.
type Router struct {
	providers map[string]AIProvider
	cfg       *config.ModelsConfig
	mu        sync.RWMutex
}

// NewRouter creates a new provider router.
func NewRouter(cfg *config.ModelsConfig, providers map[string]AIProvider) *Router {
	return &Router{
		providers: providers,
		cfg:       cfg,
	}
}

// Register adds or replaces a named provider in the router.
func (r *Router) Register(name string, p AIProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
	slog.Info("provider registered", "name", name, "type", p.Type())
}

// Generate routes a generation request to the appropriate provider.
// Provider selection order: default → fallback → any available provider of the same type.
// If a provider fails during generation, the router falls through to the next candidate
// rather than returning an error immediately.
func (r *Router) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	candidates := r.buildCandidateList(req.TaskType)

	var lastErr error
	for _, name := range candidates {
		r.mu.RLock()
		p, ok := r.providers[name]
		r.mu.RUnlock()

		if !ok {
			continue
		}
		if !p.IsAvailable(ctx) {
			slog.Debug("provider unavailable, skipping", "provider", name)
			continue
		}

		slog.Debug("generating with provider", "provider", name, "task_type", req.TaskType)
		resp, err := p.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}

		slog.Warn("provider generation failed, trying next",
			"provider", name,
			"error", err,
		)
		lastErr = err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all %d provider(s) failed for task type %s: last error: %w",
			len(candidates), req.TaskType, lastErr)
	}
	return nil, fmt.Errorf("no available provider for task type %s", req.TaskType)
}

// buildCandidateList returns an ordered list of provider names to try.
// Order: configured default → configured fallback → any other same-type provider.
func (r *Router) buildCandidateList(taskType ProviderType) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rule := r.getRouteRule(taskType)
	seen := make(map[string]bool)
	var candidates []string

	// 1. Default provider
	if rule.Default != "" {
		candidates = append(candidates, rule.Default)
		seen[rule.Default] = true
	}

	// 2. Fallback provider
	if rule.Fallback != "" && !seen[rule.Fallback] {
		candidates = append(candidates, rule.Fallback)
		seen[rule.Fallback] = true
	}

	// 3. Any other provider of the same type
	for name, p := range r.providers {
		if !seen[name] && p.Type() == taskType {
			candidates = append(candidates, name)
			seen[name] = true
		}
	}

	return candidates
}

// GenerateWithProvider explicitly routes a request to a named provider.
func (r *Router) GenerateWithProvider(ctx context.Context, providerName string, req *GenerateRequest) (*GenerateResponse, error) {
	r.mu.RLock()
	p, ok := r.providers[providerName]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	return p.Generate(ctx, req)
}

// buildCandidateList is now the primary provider resolution logic.
// selectProvider is kept for backward compatibility and tests.
func (r *Router) selectProvider(ctx context.Context, taskType ProviderType) (AIProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rule := r.getRouteRule(taskType)

	// Try default provider
	if rule.Default != "" {
		if p, ok := r.providers[rule.Default]; ok && p.IsAvailable(ctx) {
			return p, nil
		}
		slog.Warn("default provider unavailable, trying fallback",
			"default", rule.Default, "task_type", taskType)
	}

	// Try fallback provider
	if rule.Fallback != "" {
		if p, ok := r.providers[rule.Fallback]; ok && p.IsAvailable(ctx) {
			slog.Info("fallback provider selected",
				"fallback", rule.Fallback, "task_type", taskType)
			return p, nil
		}
	}

	// Scan all registered providers of the matching type
	for name, p := range r.providers {
		if p.Type() == taskType && p.IsAvailable(ctx) {
			slog.Info("alternative provider selected", "name", name, "task_type", taskType)
			return p, nil
		}
	}

	return nil, fmt.Errorf("no available provider for task type %s", taskType)
}

// getRouteRule returns the routing configuration for a task type.
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

// ListProviders returns information about all registered providers.
func (r *Router) ListProviders(ctx context.Context) []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var infos []ProviderInfo
	for name, p := range r.providers {
		infos = append(infos, ProviderInfo{
			Name:      name,
			Type:      string(p.Type()),
			Available: p.IsAvailable(ctx),
		})
	}
	return infos
}

// ProviderInfo describes a registered AI provider's status.
type ProviderInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
}

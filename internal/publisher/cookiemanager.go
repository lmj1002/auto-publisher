// Package publisher provides platform-specific content publishing capabilities.
package publisher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CookieManager handles automatic cookie acquisition, caching, and refresh.
// It supports both account/password login (recommended) and manual cookie strings
// (legacy fallback). Cached cookies are stored on disk to avoid re-login on restart.
type CookieManager struct {
	platform   string       // platform name for logging and file naming
	cookieDir  string       // directory for cached cookie files
	cachedFile string       // path to the platform's cookie cache file

	// Manual cookie string (legacy fallback)
	manualCookie string

	// Login function — platform-specific implementation
	loginFunc func(ctx context.Context) (string, error)

	mu       sync.RWMutex
	cookie   string
	loadedAt time.Time
}

// CookieManagerOption is a functional option for configuring a CookieManager.
type CookieManagerOption func(*CookieManager)

// WithManualCookie sets a manual cookie string as fallback.
func WithManualCookie(cookie string) CookieManagerOption {
	return func(cm *CookieManager) {
		if cookie != "" {
			cm.manualCookie = cookie
		}
	}
}

// WithLoginFunc sets the platform-specific login function.
func WithLoginFunc(fn func(ctx context.Context) (string, error)) CookieManagerOption {
	return func(cm *CookieManager) {
		cm.loginFunc = fn
	}
}

// NewCookieManager creates a new CookieManager for the given platform.
// cookieDir is the directory where cached cookie files are stored.
// opts are optional functional configuration options.
func NewCookieManager(platform, cookieDir string, opts ...CookieManagerOption) *CookieManager {
	cm := &CookieManager{
		platform:   platform,
		cookieDir:  cookieDir,
		cachedFile: filepath.Join(cookieDir, platform+".cookie"),
	}
	for _, opt := range opts {
		opt(cm)
	}
	return cm
}

// GetCookie returns a valid cookie string, performing login if necessary.
// It first checks the in-memory cache, then the disk cache, then manual cookie,
// and finally performs a fresh login.
func (cm *CookieManager) GetCookie(ctx context.Context) (string, error) {
	// Fast path: in-memory cache (valid for 1 hour)
	cm.mu.RLock()
	if cm.cookie != "" && time.Since(cm.loadedAt) < 1*time.Hour {
		c := cm.cookie
		cm.mu.RUnlock()
		return c, nil
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Double-check after acquiring write lock
	if cm.cookie != "" && time.Since(cm.loadedAt) < 1*time.Hour {
		return cm.cookie, nil
	}

	// 1. Try disk cache
	if cached, ok := cm.loadFromDisk(); ok {
		slog.Info("cookie loaded from disk cache", "platform", cm.platform)
		cm.cookie = cached
		cm.loadedAt = time.Now()
		return cm.cookie, nil
	}

	// 2. Try manual cookie (legacy fallback)
	if cm.manualCookie != "" {
		slog.Info("using manual cookie", "platform", cm.platform)
		cm.cookie = cm.manualCookie
		cm.loadedAt = time.Now()
		return cm.cookie, nil
	}

	// 3. Perform login
	if cm.loginFunc == nil {
		return "", fmt.Errorf("%s: no login function configured and no cached cookie available", cm.platform)
	}

	slog.Info("performing auto-login", "platform", cm.platform)
	cookie, err := cm.loginFunc(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: auto-login failed: %w", cm.platform, err)
	}

	// Save to disk and memory
	if err := cm.saveToDisk(cookie); err != nil {
		slog.Warn("failed to save cookie to disk", "platform", cm.platform, "error", err)
	}
	cm.cookie = cookie
	cm.loadedAt = time.Now()
	slog.Info("auto-login successful, cookie cached", "platform", cm.platform)
	return cm.cookie, nil
}

// Invalidate clears the cached cookie, forcing a fresh login on next GetCookie call.
func (cm *CookieManager) Invalidate() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cookie = ""
	slog.Info("cookie invalidated", "platform", cm.platform)
}

// HasManualCookie reports whether a manual cookie string is configured.
func (cm *CookieManager) HasManualCookie() bool {
	return cm.manualCookie != ""
}

// HasCredentials reports whether a login function is configured.
func (cm *CookieManager) HasCredentials() bool {
	return cm.loginFunc != nil
}

// loadFromDisk reads the cached cookie from the disk file.
// Returns the cookie string and true if successful.
func (cm *CookieManager) loadFromDisk() (string, bool) {
	data, err := os.ReadFile(cm.cachedFile)
	if err != nil {
		return "", false
	}
	cookie := strings.TrimSpace(string(data))
	if cookie == "" {
		return "", false
	}

	// Check file modification time — expire after 7 days
	info, err := os.Stat(cm.cachedFile)
	if err != nil {
		return "", false
	}
	if time.Since(info.ModTime()) > 7*24*time.Hour {
		slog.Info("cached cookie expired (>7 days), will re-login", "platform", cm.platform)
		return "", false
	}

	return cookie, true
}

// saveToDisk writes the cookie string to the disk cache file.
func (cm *CookieManager) saveToDisk(cookie string) error {
	if err := os.MkdirAll(cm.cookieDir, 0700); err != nil {
		return fmt.Errorf("create cookie dir: %w", err)
	}
	if err := os.WriteFile(cm.cachedFile, []byte(cookie), 0600); err != nil {
		return fmt.Errorf("write cookie file: %w", err)
	}
	return nil
}

// IsAvailable checks whether any cookie source is available.
func (cm *CookieManager) IsAvailable() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cookie != "" || cm.manualCookie != "" || cm.loginFunc != nil
}

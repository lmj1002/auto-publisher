// Package config provides configuration loading and management for the auto-publisher.
// It supports YAML config files with environment variable interpolation and .env file loading.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config is the top-level application configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Models    ModelsConfig    `yaml:"models"`
	Prompts   PromptsConfig   `yaml:"prompts"`
	Platforms PlatformsConfig `yaml:"platforms"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

// ServerConfig holds HTTP server and data directory settings.
type ServerConfig struct {
	Port    int    `yaml:"port"`
	Mode    string `yaml:"mode"`     // debug | release
	DataDir string `yaml:"data_dir"` // data storage root directory
}

// IsDebug reports whether the server is running in debug mode.
func (s *ServerConfig) IsDebug() bool {
	return s.Mode == "debug"
}

// DatabaseConfig holds MySQL connection settings.
type DatabaseConfig struct {
	Driver       string `yaml:"driver"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	DBName       string `yaml:"dbname"`
	Charset      string `yaml:"charset"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
	MaxOpenConns int    `yaml:"max_open_conns"`
}

// DSN returns the MySQL Data Source Name connection string.
func (d *DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.Charset)
}

// ModelsConfig holds AI model provider routing configuration.
type ModelsConfig struct {
	DefaultTextProvider string             `yaml:"default_text_provider"`
	Providers           map[string]Provider `yaml:"providers"`
	Routing             RoutingConfig      `yaml:"routing"`
}

// Provider holds configuration for a single AI provider.
type Provider struct {
	Type          string `yaml:"type"` // text / image / video
	Enabled       bool   `yaml:"enabled"`
	WorkDir       string `yaml:"work_dir,omitempty"`
	Timeout       string `yaml:"timeout,omitempty"`
	MaxConcurrent int    `yaml:"max_concurrent,omitempty"`
	APIKey        string `yaml:"api_key,omitempty"`
	TextModel     string `yaml:"text_model,omitempty"`
	ImageModel    string `yaml:"image_model,omitempty"`
	BaseURL       string `yaml:"base_url,omitempty"`
}

// GetAPIKey resolves the API key, supporting ${ENV_VAR} environment variable references.
func (p *Provider) GetAPIKey() string {
	key := strings.TrimSpace(p.APIKey)
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		envName := key[2 : len(key)-1]
		return os.Getenv(envName)
	}
	return key
}

// RoutingConfig defines model routing rules per content type.
type RoutingConfig struct {
	Text  RouteRule `yaml:"text"`
	Image RouteRule `yaml:"image"`
	Video RouteRule `yaml:"video"`
}

// RouteRule specifies a default provider and an optional fallback.
type RouteRule struct {
	Default  string `yaml:"default"`
	Fallback string `yaml:"fallback"`
}

// PromptsConfig holds platform-specific prompt templates (fallback if prompt files missing).
type PromptsConfig struct {
	Xiaohongshu PlatformPromptConfig `yaml:"xiaohongshu"`
	Zhihu       PlatformPromptConfig `yaml:"zhihu"`
}

// PlatformPromptConfig holds the system prompt for a platform.
type PlatformPromptConfig struct {
	System string `yaml:"system"`
}

// PlatformsConfig holds publishing configuration for all platforms.
type PlatformsConfig struct {
	Xiaohongshu PlatformPubConfig `yaml:"xiaohongshu"`
	Zhihu       PlatformPubConfig `yaml:"zhihu"`
}

// PlatformPubConfig holds publishing settings for a single platform.
type PlatformPubConfig struct {
	Enabled       bool   `yaml:"enabled"`
	MaxDailyPosts int    `yaml:"max_daily_posts"`
	Username      string `yaml:"username"` // login username (replaces manual cookie)
	Password      string `yaml:"password"` // login password (replaces manual cookie)
}

// HasCredentials reports whether username and password are both configured.
func (p *PlatformPubConfig) HasCredentials() bool {
	return p.Username != "" && p.Password != ""
}

// ResolveCredentials resolves credential fields that may reference environment variables.
// Returns resolved username and password.
func (p *PlatformPubConfig) ResolveCredentials() (username, password string) {
	username = p.Username
	password = p.Password

	// Support ${ENV_VAR} syntax in username/password
	if strings.HasPrefix(username, "${") && strings.HasSuffix(username, "}") {
		username = os.Getenv(username[2 : len(username)-1])
	}
	if strings.HasPrefix(password, "${") && strings.HasSuffix(password, "}") {
		password = os.Getenv(password[2 : len(password)-1])
	}
	return username, password
}

// SchedulerConfig holds the scheduling configuration.
type SchedulerConfig struct {
	ScanInterval string `yaml:"scan_interval"` // e.g. "5m"
	Timezone     string `yaml:"timezone"`      // e.g. "Asia/Shanghai"
	MaxRetries   int    `yaml:"max_retries"`
	RetryDelay   string `yaml:"retry_delay"` // e.g. "10m"
}

// ScanIntervalDuration parses and returns the scan interval as a time.Duration.
// Defaults to 5 minutes if parsing fails.
func (s *SchedulerConfig) ScanIntervalDuration() time.Duration {
	if s.ScanInterval == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(s.ScanInterval)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// RetryDelayDuration parses and returns the retry delay as a time.Duration.
// Defaults to 10 minutes if parsing fails.
func (s *SchedulerConfig) RetryDelayDuration() time.Duration {
	if s.RetryDelay == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(s.RetryDelay)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

// Load reads and parses the config file at path, automatically loading .env files.
func Load(path string) (*Config, error) {
	// Load .env from config file directory
	configDir := filepath.Dir(path)
	envFile := filepath.Join(configDir, ".env")
	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			return nil, fmt.Errorf("load .env file: %w", err)
		}
	}
	// Also try .env in current working directory
	if _, err := os.Stat(".env"); err == nil {
		_ = godotenv.Load(".env")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Environment variable overrides for database
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("DB_USER"); v != "" {
		cfg.Database.User = v
	}
	if v := os.Getenv("DB_HOST"); v != "" {
		cfg.Database.Host = v
	}

	cfg.applyDefaults()

	return cfg, nil
}

// applyDefaults sets default values for optional configuration fields.
func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.DataDir == "" {
		c.Server.DataDir = "./data"
	}
	if c.Scheduler.MaxRetries == 0 {
		c.Scheduler.MaxRetries = 3
	}
	if c.Scheduler.RetryDelay == "" {
		c.Scheduler.RetryDelay = "10m"
	}
}

// GetTextProvider returns the configured default text generation provider name.
func (c *Config) GetTextProvider() string {
	if c.Models.Routing.Text.Default != "" {
		return c.Models.Routing.Text.Default
	}
	return c.Models.DefaultTextProvider
}

// GetTextFallback returns the fallback text generation provider name.
func (c *Config) GetTextFallback() string {
	return c.Models.Routing.Text.Fallback
}

// GetImageProvider returns the configured image generation provider name.
func (c *Config) GetImageProvider() string {
	return c.Models.Routing.Image.Default
}

// GetProvider returns the provider configuration by name.
func (c *Config) GetProvider(name string) (*Provider, bool) {
	p, ok := c.Models.Providers[name]
	return &p, ok
}

// CookieDir returns the directory path for cached cookie files.
func (c *Config) CookieDir() string {
	return filepath.Join(c.Server.DataDir, "cookies")
}

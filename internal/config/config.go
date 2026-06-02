package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config 全局配置
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Models    ModelsConfig    `yaml:"models"`
	Prompts   PromptsConfig   `yaml:"prompts"`
	Platforms PlatformsConfig `yaml:"platforms"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port    int    `yaml:"port"`
	Mode    string `yaml:"mode"`
	DataDir string `yaml:"data_dir"`
}

// IsDebug 是否调试模式
func (s *ServerConfig) IsDebug() bool {
	return s.Mode == "debug"
}

// DatabaseConfig 数据库配置
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

// DSN 返回 MySQL 连接字符串
func (d *DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.Charset)
}

// ModelsConfig 模型路由配置
type ModelsConfig struct {
	DefaultTextProvider string             `yaml:"default_text_provider"`
	Providers           map[string]Provider `yaml:"providers"`
	Routing             RoutingConfig      `yaml:"routing"`
}

// Provider 单个 AI Provider 配置
type Provider struct {
	Type           string `yaml:"type"`            // text / image / video
	Enabled        bool   `yaml:"enabled"`
	WorkDir        string `yaml:"work_dir,omitempty"`
	Timeout        string `yaml:"timeout,omitempty"`
	MaxConcurrent  int    `yaml:"max_concurrent,omitempty"`
	APIKey         string `yaml:"api_key,omitempty"`
	TextModel      string `yaml:"text_model,omitempty"`
	ImageModel     string `yaml:"image_model,omitempty"`
	BaseURL        string `yaml:"base_url,omitempty"`
}

// GetAPIKey 获取 API Key，支持 ${ENV_VAR} 格式的环境变量引用
func (p *Provider) GetAPIKey() string {
	key := strings.TrimSpace(p.APIKey)
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		envName := key[2 : len(key)-1]
		return os.Getenv(envName)
	}
	return key
}

// RoutingConfig 路由规则配置
type RoutingConfig struct {
	Text  RouteRule `yaml:"text"`
	Image RouteRule `yaml:"image"`
	Video RouteRule `yaml:"video"`
}

// RouteRule 单条路由规则
type RouteRule struct {
	Default  string `yaml:"default"`
	Fallback string `yaml:"fallback"`
}

// PromptsConfig Prompt 模板配置
type PromptsConfig struct {
	Xiaohongshu PlatformPromptConfig `yaml:"xiaohongshu"`
	Zhihu       PlatformPromptConfig `yaml:"zhihu"`
}

// PlatformPromptConfig 平台 Prompt 配置
type PlatformPromptConfig struct {
	System string `yaml:"system"`
}

// PlatformsConfig 平台发布配置
type PlatformsConfig struct {
	Xiaohongshu PlatformPubConfig `yaml:"xiaohongshu"`
	Zhihu       PlatformPubConfig `yaml:"zhihu"`
}

// PlatformPubConfig 单个平台的发布配置
type PlatformPubConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxDailyPosts int  `yaml:"max_daily_posts"`
}

// SchedulerConfig 调度配置
type SchedulerConfig struct {
	ScanInterval string `yaml:"scan_interval"`
	Timezone     string `yaml:"timezone"`
	MaxRetries   int    `yaml:"max_retries"`
	RetryDelay   string `yaml:"retry_delay"`
}

// Load 加载配置文件（自动加载同目录下的 .env 文件）
func Load(path string) (*Config, error) {
	// 尝试加载 .env 文件（config.yaml 同目录）
	configDir := filepath.Dir(path)
	envFile := filepath.Join(configDir, ".env")
	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			return nil, fmt.Errorf("加载 .env 文件失败: %w", err)
		}
	}
	// 同时尝试当前工作目录的 .env
	if _, err := os.Stat(".env"); err == nil {
		godotenv.Load(".env")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 环境变量覆盖数据库配置
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("DB_USER"); v != "" {
		cfg.Database.User = v
	}
	if v := os.Getenv("DB_HOST"); v != "" {
		cfg.Database.Host = v
	}

	// 设置默认值
	cfg.applyDefaults()

	return cfg, nil
}

// applyDefaults 应用默认值
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

// GetTextProvider 获取文字生成 Provider 名称
func (c *Config) GetTextProvider() string {
	if c.Models.Routing.Text.Default != "" {
		return c.Models.Routing.Text.Default
	}
	return c.Models.DefaultTextProvider
}

// GetTextFallback 获取文字降级 Provider 名称
func (c *Config) GetTextFallback() string {
	return c.Models.Routing.Text.Fallback
}

// GetImageProvider 获取图片生成 Provider 名称
func (c *Config) GetImageProvider() string {
	return c.Models.Routing.Image.Default
}

// GetProvider 按名称获取 Provider 配置
func (c *Config) GetProvider(name string) (*Provider, bool) {
	p, ok := c.Models.Providers[name]
	return &p, ok
}

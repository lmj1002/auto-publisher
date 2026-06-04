// Command server is the entry point for the auto-publisher application.
// It initializes all subsystems (database, AI providers, publishers, scheduler, collectors)
// and starts the HTTP server.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"auto-publisher/internal/collector"
	"auto-publisher/internal/config"
	"auto-publisher/internal/handler"
	"auto-publisher/internal/model"
	"auto-publisher/internal/provider"
	"auto-publisher/internal/publisher"
	"auto-publisher/internal/scheduler"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	flag.Parse()

	// Initialize structured logging
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	// Ensure data directories exist
	dataDir := cfg.Server.DataDir
	dirs := []string{
		dataDir,
		filepath.Join(dataDir, "images"),
		filepath.Join(dataDir, "prompts"),
		filepath.Join(dataDir, "screenshots"),
		cfg.CookieDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			slog.Warn("create directory failed", "path", d, "error", err)
		}
	}

	// Initialize database
	db := initDatabase(cfg)
	db.AutoMigrate(&model.Content{}, &model.CollectedContent{})

	// Migration: change JSON columns to TEXT to avoid MySQL JSON validation errors
	// (XHSImages and ZHTopics store comma-separated strings, not JSON arrays)
	migrateJSONColumns(db)

	// Initialize Prompt manager
	pm := provider.NewPromptManager("prompts")
	if err := pm.LoadAll(); err != nil {
		slog.Warn("load prompt templates warning", "error", err)
	} else {
		slog.Info("prompt templates loaded", "count", len(pm.List()), "names", pm.List())
	}

	// Initialize AI Provider router
	aiRouter := initRouter(cfg, dataDir)

	// Initialize publishers with auto-login support
	pubs := initPublishers(cfg)

	// Initialize content collectors
	collectors := initCollectors(cfg, pubs)

	// Initialize scheduler
	sched := initScheduler(cfg, db, pubs)

	// Initialize HTTP handlers
	contentHandler := handler.NewContentHandler(db, aiRouter, cfg, pm)
	collectionHandler := handler.NewCollectionHandler(db, collectors)

	// Initialize Gin
	if !cfg.Server.IsDebug() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// Static file serving
	r.Static("/images", filepath.Join(dataDir, "images"))
	r.Static("/static", "./web/static")

	// Frontend page
	r.LoadHTMLGlob("web/templates/*")

	// Homepage
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title": "自媒体内容管理",
		})
	})

	// API routes
	api := r.Group("/api")
	{
		// Content CRUD
		api.GET("/contents", contentHandler.List)
		api.GET("/contents/:id", contentHandler.Get)
		api.PUT("/contents/:id", contentHandler.Update)
		api.DELETE("/contents/:id", contentHandler.Delete)

		// AI generation
		api.POST("/generate/draft", contentHandler.GenerateDraft)
		api.POST("/generate/draft-with-image", contentHandler.GenerateDraftWithImage)

		// Scheduling
		api.POST("/contents/:id/schedule", contentHandler.Schedule)

		// Publishing
		api.POST("/contents/:id/publish", func(c *gin.Context) {
			id, err := strconv.ParseInt(c.Param("id"), 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid ID"})
				return
			}
			if err := sched.PublishNow(id); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "publish task submitted"})
		})

		// Content collection (new)
		api.POST("/collect", collectionHandler.Collect)
		api.GET("/collected", collectionHandler.List)

		// System status
		api.GET("/status", func(c *gin.Context) {
			providers := aiRouter.ListProviders(c.Request.Context())
			var pubStatus []gin.H
			for _, p := range pubs {
				pubStatus = append(pubStatus, gin.H{
					"name":      p.Name(),
					"platform":  p.Platform(),
					"available": p.IsAvailable(c.Request.Context()),
				})
			}
			var collectorStatus []gin.H
			for _, col := range collectors {
				collectorStatus = append(collectorStatus, gin.H{
					"name":      col.Name(),
					"platform":  col.Platform(),
					"available": col.IsAvailable(c.Request.Context()),
				})
			}
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"data": gin.H{
					"providers":  providers,
					"publishers": pubStatus,
					"collectors": collectorStatus,
				},
			})
		})
	}

	// Start scheduler
	sched.Start()
	defer sched.Stop()

	// Start HTTP server
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	slog.Info("server starting", "addr", addr)
	if err := r.Run(addr); err != nil {
		slog.Error("server start failed", "error", err)
		os.Exit(1)
	}
}

// initDatabase initializes the MySQL database connection with GORM.
func initDatabase(cfg *config.Config) *gorm.DB {
	logLevel := logger.Info
	if !cfg.Server.IsDebug() {
		logLevel = logger.Warn
	}

	db, err := gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}

	sqlDB, err := db.DB()
	if err != nil {
		slog.Error("get database instance failed", "error", err)
		os.Exit(1)
	}

	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	slog.Info("database connected", "host", cfg.Database.Host, "db", cfg.Database.DBName)
	return db
}

// initRouter initializes the AI provider router with configured providers.
func initRouter(cfg *config.Config, dataDir string) *provider.Router {
	providers := make(map[string]provider.AIProvider)

	// Claude Code Provider
	if claudeCfg, ok := cfg.GetProvider("claude-code"); ok && claudeCfg.Enabled {
		timeout := 120 * time.Second
		if claudeCfg.Timeout != "" {
			if d, err := time.ParseDuration(claudeCfg.Timeout); err == nil {
				timeout = d
			}
		}
		workDir := claudeCfg.WorkDir
		if envDir := os.Getenv("CLAUDE_CODE_WORK_DIR"); envDir != "" {
			workDir = envDir
		}
		ccProvider := provider.NewClaudeCodeProvider(workDir, timeout)
		providers["claude-code"] = ccProvider
		slog.Info("Claude Code provider registered", "work_dir", workDir)
	}

	// Gemini Provider
	if geminiCfg, ok := cfg.GetProvider("gemini"); ok && geminiCfg.Enabled {
		imageDir := filepath.Join(dataDir, "images")
		geminiProvider := provider.NewGeminiProvider(geminiCfg, imageDir)
		providers["gemini"] = geminiProvider
		slog.Info("Gemini provider registered")
	}

	return provider.NewRouter(&cfg.Models, providers)
}

// initPublishers initializes platform publishers with auto-login support.
// Priority: account/password login > manual cookie > .cookie file
func initPublishers(cfg *config.Config) map[string]publisher.Publisher {
	pubs := make(map[string]publisher.Publisher)
	cookieDir := cfg.CookieDir()

	// Zhihu publisher
	if cfg.Platforms.Zhihu.Enabled {
		zhCfg := cfg.Platforms.Zhihu
		username, password := zhCfg.ResolveCredentials()

		var zhPub publisher.Publisher
		if username != "" && password != "" {
			// Auto-login mode
			zhPub = publisher.NewZhihuPublisherWithLogin(cookieDir, username, password)
			slog.Info("zhihu publisher registered (auto-login)", "username", username)
		} else {
			// Legacy cookie mode
			cookieSource := os.Getenv("ZHIHU_COOKIE")
			if cookieSource == "" {
				if data, err := os.ReadFile("zhihu.cookie"); err == nil {
					cookieSource = string(data)
				}
			}
			if cookieSource != "" {
				zhPub = publisher.NewZhihuPublisher(cookieSource)
				slog.Info("zhihu publisher registered (manual cookie)")
			} else {
				slog.Warn("zhihu publisher not enabled: missing credentials or cookie")
			}
		}

		if zhPub != nil {
			pubs["zhihu"] = zhPub
		}
	}

	// Xiaohongshu publisher
	if cfg.Platforms.Xiaohongshu.Enabled {
		xhsCfg := cfg.Platforms.Xiaohongshu
		username, password := xhsCfg.ResolveCredentials()
		screenshotDir := filepath.Join(cfg.Server.DataDir, "screenshots")

		var xhsPub publisher.Publisher
		if username != "" && password != "" {
			// Auto-login mode
			xhsPub = publisher.NewXHSPublisherWithLogin(cookieDir, screenshotDir,
				username, password, !cfg.Server.IsDebug())
			slog.Info("xiaohongshu publisher registered (auto-login)", "username", username)
		} else {
			// Legacy cookie mode
			xhsCookie := os.Getenv("XHS_COOKIE")
			if xhsCookie == "" {
				if data, err := os.ReadFile("xiaohongshu.cookie"); err == nil {
					xhsCookie = string(data)
				}
			}
			if xhsCookie != "" {
				xhsPub = publisher.NewXHSPublisher(xhsCookie, screenshotDir, !cfg.Server.IsDebug())
				slog.Info("xiaohongshu publisher registered (manual cookie)")
			} else {
				slog.Warn("xiaohongshu publisher not enabled: missing credentials or cookie")
			}
		}

		if xhsPub != nil {
			pubs["xiaohongshu"] = xhsPub
		}
	}

	return pubs
}

// initCollectors initializes content collectors for researching similar content.
func initCollectors(cfg *config.Config, pubs map[string]publisher.Publisher) map[string]collector.Collector {
	collectors := make(map[string]collector.Collector)

	// Zhihu collector — use cookie from publisher if available
	var zhCookies string
	if zhPub, ok := pubs["zhihu"]; ok {
		// Try to get cookie from publisher for authenticated search
		if zh, ok := zhPub.(*publisher.ZhihuPublisher); ok {
			zhCookies = os.Getenv("ZHIHU_COOKIE")
			if zhCookies == "" {
				if data, err := os.ReadFile("zhihu.cookie"); err == nil {
					zhCookies = string(data)
				}
			}
			_ = zh // reference to avoid import issue
		}
	}
	zhCollector := collector.NewZhihuCollector(zhCookies)
	collectors["zhihu"] = zhCollector
	slog.Info("zhihu collector registered")

	// Xiaohongshu collector — use cookie from publisher if available
	var xhsCookies string
	if _, ok := pubs["xiaohongshu"]; ok {
		xhsCookies = os.Getenv("XHS_COOKIE")
		if xhsCookies == "" {
			if data, err := os.ReadFile("xiaohongshu.cookie"); err == nil {
				xhsCookies = string(data)
			}
		}
	}
	xhsCollector := collector.NewXHSCollector(xhsCookies, !cfg.Server.IsDebug())
	collectors["xiaohongshu"] = xhsCollector
	slog.Info("xiaohongshu collector registered")

	return collectors
}

// initScheduler initializes the publication scheduler with all registered publishers.
func initScheduler(cfg *config.Config, db *gorm.DB, pubs map[string]publisher.Publisher) *scheduler.Scheduler {
	interval := cfg.Scheduler.ScanIntervalDuration()
	retryDelay := cfg.Scheduler.RetryDelayDuration()

	sched, err := scheduler.New(db, interval, cfg.Scheduler.MaxRetries, retryDelay, cfg.Scheduler.Timezone)
	if err != nil {
		slog.Error("create scheduler failed", "error", err)
		os.Exit(1)
	}

	// Register all publishers
	for name, pub := range pubs {
		sched.Register(name, pub)
	}

	slog.Info("scheduler initialized",
		"interval", interval,
		"max_retries", cfg.Scheduler.MaxRetries,
		"retry_delay", retryDelay,
	)
	return sched
}

// migrateJSONColumns alters XHSImages and ZHTopics from JSON to TEXT type.
// These columns store comma-separated strings, not JSON arrays, so MySQL's JSON
// type would reject them with "Invalid JSON text" errors on insert.
// This migration is idempotent — it silently succeeds if the columns are already TEXT.
func migrateJSONColumns(db *gorm.DB) {
	columns := []struct {
		table  string
		column string
	}{
		{"contents", "xhs_images"},
		{"contents", "zh_topics"},
	}

	for _, c := range columns {
		// Check current column type
		var colType string
		row := db.Raw(
			"SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?",
			db.Migrator().CurrentDatabase(), c.table, c.column,
		).Row()
		if row != nil {
			row.Scan(&colType)
		}

		if colType == "" {
			slog.Warn("column type check skipped, column may not exist", "table", c.table, "column", c.column)
			continue
		}

		// If it's still JSON, alter to TEXT
		if colType == "json" {
			sql := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s TEXT", c.table, c.column)
			if err := db.Exec(sql).Error; err != nil {
				slog.Warn("failed to alter column type", "sql", sql, "error", err)
			} else {
				slog.Info("column type migrated", "table", c.table, "column", c.column,
					"from", colType, "to", "text")
			}
		} else {
			slog.Debug("column already correct type, skipping migration",
				"table", c.table, "column", c.column, "type", colType)
		}
	}
}

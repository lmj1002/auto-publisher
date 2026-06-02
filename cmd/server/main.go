package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

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
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 确保数据目录存在
	dataDir := cfg.Server.DataDir
	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(filepath.Join(dataDir, "images"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "prompts"), 0755)

	// 初始化数据库
	db := initDatabase(cfg)
	db.AutoMigrate(&model.Content{})

	// 初始化 Prompt 模板管理器
	pm := provider.NewPromptManager("prompts")
	if err := pm.LoadAll(); err != nil {
		log.Printf("⚠️  加载 Prompt 模板警告: %v", err)
	} else {
		log.Printf("✅ 已加载 %d 个 Prompt 模板: %v", len(pm.List()), pm.List())
	}

	// 初始化 AI Provider 路由
	aiRouter := initRouter(cfg, dataDir)

	// 初始化发布器
	pubs := initPublishers(cfg)

	// 初始化调度器
	sched := initScheduler(cfg, db, pubs)

	// 初始化 Handler
	contentHandler := handler.NewContentHandler(db, aiRouter, cfg, pm)

	// 初始化 Gin
	if !cfg.Server.IsDebug() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// 静态文件服务
	r.Static("/images", filepath.Join(dataDir, "images"))
	r.Static("/static", "./web/static")

	// 前端页面
	r.LoadHTMLGlob("web/templates/*")

	// 首页
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"title": "自媒体内容管理",
		})
	})

	// API 路由组
	api := r.Group("/api")
	{
		// 内容 CRUD
		api.GET("/contents", contentHandler.List)
		api.GET("/contents/:id", contentHandler.Get)
		api.PUT("/contents/:id", contentHandler.Update)
		api.DELETE("/contents/:id", contentHandler.Delete)

		// AI 生成
		api.POST("/generate/draft", contentHandler.GenerateDraft)
		api.POST("/generate/draft-with-image", contentHandler.GenerateDraftWithImage)

		// 排期
		api.POST("/contents/:id/schedule", contentHandler.Schedule)

		// 发布操作
		api.POST("/contents/:id/publish", func(c *gin.Context) {
			id, err := strconv.ParseInt(c.Param("id"), 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "无效的 ID"})
				return
			}
			if err := sched.PublishNow(id); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "已提交发布任务"})
		})

		// 系统状态
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
			c.JSON(http.StatusOK, gin.H{
				"code": 0,
				"data": gin.H{
					"providers":  providers,
					"publishers": pubStatus,
				},
			})
		})
	}

	// 启动调度器
	sched.Start()
	defer sched.Stop()

	// 启动服务
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("🚀 服务启动: http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// initDatabase 初始化 MySQL 数据库连接
func initDatabase(cfg *config.Config) *gorm.DB {
	logLevel := logger.Info
	if !cfg.Server.IsDebug() {
		logLevel = logger.Warn
	}

	db, err := gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取数据库实例失败: %v", err)
	}

	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	log.Println("✅ 数据库连接成功")
	return db
}

// initRouter 初始化 AI Provider 路由器
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
		log.Printf("✅ Claude Code Provider 已注册 (work_dir=%s)", workDir)
	}

	// Gemini Provider
	if geminiCfg, ok := cfg.GetProvider("gemini"); ok && geminiCfg.Enabled {
		imageDir := filepath.Join(dataDir, "images")
		geminiProvider := provider.NewGeminiProvider(geminiCfg, imageDir)
		providers["gemini"] = geminiProvider
		log.Println("✅ Gemini Provider 已注册")
	}

	return provider.NewRouter(&cfg.Models, providers)
}

// initPublishers 初始化平台发布器
func initPublishers(cfg *config.Config) map[string]publisher.Publisher {
	pubs := make(map[string]publisher.Publisher)

	// 知乎发布器
	if cfg.Platforms.Zhihu.Enabled {
		cookieSource := os.Getenv("ZHIHU_COOKIE")
		if cookieSource == "" {
			if data, err := os.ReadFile("zhihu.cookie"); err == nil {
				cookieSource = string(data)
			}
		}
		if cookieSource != "" {
			pubs["zhihu"] = publisher.NewZhihuPublisher(cookieSource)
			log.Println("✅ 知乎发布器已注册")
		} else {
			log.Println("⚠️  知乎发布器未启用：缺少 ZHIHU_COOKIE")
		}
	}

	// 小红书发布器
	if cfg.Platforms.Xiaohongshu.Enabled {
		xhsCookie := os.Getenv("XHS_COOKIE")
		if xhsCookie == "" {
			if data, err := os.ReadFile("xiaohongshu.cookie"); err == nil {
				xhsCookie = string(data)
			}
		}
		if xhsCookie != "" {
			screenshotDir := filepath.Join(cfg.Server.DataDir, "screenshots")
			xhs := publisher.NewXHSPublisher(xhsCookie, screenshotDir, cfg.Server.IsDebug())
			pubs["xiaohongshu"] = xhs
			log.Println("✅ 小红书发布器已注册")
		} else {
			log.Println("⚠️  小红书发布器未启用：缺少 XHS_COOKIE")
		}
	}

	return pubs
}

// initScheduler 初始化调度器
func initScheduler(cfg *config.Config, db *gorm.DB, pubs map[string]publisher.Publisher) *scheduler.Scheduler {
	interval := 5 * time.Minute
	if cfg.Scheduler.ScanInterval != "" {
		if d, err := time.ParseDuration(cfg.Scheduler.ScanInterval); err == nil {
			interval = d
		}
	}

	retryDelay := 10 * time.Minute
	if cfg.Scheduler.RetryDelay != "" {
		if d, err := time.ParseDuration(cfg.Scheduler.RetryDelay); err == nil {
			retryDelay = d
		}
	}

	sched, err := scheduler.New(db, interval, cfg.Scheduler.MaxRetries, retryDelay, cfg.Scheduler.Timezone)
	if err != nil {
		log.Fatalf("创建调度器失败: %v", err)
	}

	// 注册所有发布器
	for name, pub := range pubs {
		sched.Register(name, pub)
	}

	return sched
}

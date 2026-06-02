# CLAUDE.md — 自媒体自动化发布系统

> 本地化自媒体内容生产与发布工具，AI 辅助生成图文内容，定时自动发布到小红书和知乎。

---

## 项目概述

- **目标**：AI 辅助生成小红书笔记/知乎文章，定时自动发布，实现副业变现
- **语言**：Go 1.25+（Gin + GORM + MySQL）
- **AI 能力**：Claude Code CLI（文字生成）+ Google Gemini API（封面图生成）
- **平台**：小红书（Rod 浏览器自动化）+ 知乎（HTTP API）
- **运行方式**：纯本地服务，Web 管理后台操作，定时调度器自动发布

---

## 技术栈

| 组件 | 选型 | 用途 |
|------|------|------|
| Web 框架 | Gin | HTTP API + 静态文件 |
| ORM | GORM + MySQL 8.0 | 内容数据持久化 |
| 配置 | YAML + .env | config.yaml 定义结构，.env 存密钥 |
| AI 文字 | Claude Code CLI (`os/exec`) | 子进程调用本地 `claude` 命令 |
| AI 图片 | Google Gemini API | 封面图生成 |
| 浏览器自动化 | go-rod/rod | 小红书发布 |
| 定时调度 | 标准库 `time.Ticker` | 扫描排期表，到点触发发布 |
| 前端 | Go template + 原生 JS | 极简管理页面 |

---

## 目录结构

```
auto-publisher/
├── cmd/server/main.go                   # 入口：DB → Providers → Publishers → Scheduler → Gin
├── internal/
│   ├── config/config.go                 # YAML配置 + .env自动加载 + 多模型路由
│   ├── model/content.go                 # GORM 模型 + 状态枚举 + DTO
│   ├── handler/content.go               # HTTP API：CRUD + AI生成 + 排期
│   ├── provider/                        # AI 能力层
│   │   ├── provider.go                  # AIProvider 统一接口
│   │   ├── claude_code.go               # ClaudeCodeProvider：os/exec 调 claude CLI
│   │   ├── gemini.go                    # GeminiProvider：图片生成 + 文字降级
│   │   ├── router.go                    # 模型路由器（路由规则 + fallback 降级）
│   │   └── prompt_manager.go            # Prompt模板管理（加载/渲染/热更新）
│   ├── publisher/                       # 平台发布层
│   │   ├── publisher.go                 # Publisher 统一接口
│   │   ├── zhihu.go                     # 知乎：Cookie鉴权 + HTTP API 发布
│   │   └── xiaohongshu.go              # 小红书：Rod 浏览器自动化发布
│   └── scheduler/
│       └── scheduler.go                 # 轻量定时调度器（扫描+重试+截图）
├── prompts/                             # Prompt 模板（.md文件，支持Go template）
│   ├── xiaohongshu_system.md
│   ├── zhihu_system.md
│   └── image_cover.md
├── web/templates/index.html             # Web 管理后台
├── config.yaml                          # 主配置文件
├── .env.example                         # 环境变量模板（不提交Git）
├── CLAUDE.md                            # 本文档
├── PROJECT.md                           # 完整项目文档
└── README.md                            # 用户文档
```

---

## 启动方式

```bash
# 1. 环境配置
cp .env.example .env
# 编辑 .env 填入 GEMINI_API_KEY, ZHIHU_COOKIE, XHS_COOKIE

# 2. 数据库
mysql -u root -e "CREATE DATABASE IF NOT EXISTS auto_publisher DEFAULT CHARSET utf8mb4"

# 3. 编译运行
go build -o auto-publisher.exe ./cmd/server/
.\auto-publisher.exe

# 4. 浏览器打开 http://localhost:8080
```

---

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/` | Web 管理页面 |
| GET | `/api/contents` | 内容列表 `?status=&platform=&page=&page_size=` |
| GET | `/api/contents/:id` | 单条内容详情 |
| PUT | `/api/contents/:id` | 更新内容（人工修改/定稿） |
| DELETE | `/api/contents/:id` | 删除 |
| POST | `/api/generate/draft` | AI 生成文字初稿 |
| POST | `/api/generate/draft-with-image` | AI 生成文字 + 封面图 |
| POST | `/api/contents/:id/schedule` | 设置定时发布 |
| POST | `/api/contents/:id/publish` | 立即发布 |
| GET | `/api/status` | Provider + Publisher 在线状态 |

---

## 核心设计

### AI Provider 路由

统一接口，按内容类型自动路由，支持 Fallback 降级：

```
text  → claude-code（默认） → 不可用时降级 gemini
image → gemini（默认）
video →（预留）
```

路由规则在 `config.yaml` → `models.routing` 中配置。

### Claude Code CLI 调用

```go
cmd := exec.CommandContext(ctx, "claude",
    "-p", fullPrompt,
    "--output-format", "text",
    "--max-turns", "1",
)
cmd.Dir = workDir
```

- 全局互斥锁（`sync.Mutex`），同时只允许一个 CLI 实例
- 超时 120 秒
- 输出解析格式：`---TITLE---` / `---BODY---` / `---TAGS---`（兼容 `---TOPICS---`）
- 背后模型（DeepSeek/Claude）对 Go 代码完全透明

### 内容状态流转

```
draft → ai_generated → review → approved → scheduled → published
                                                    ↓
                                                  failed →（重试）
```

### Prompt 管理

- `PromptManager` 从 `prompts/` 加载 `.md` 模板
- Go template 语法支持变量渲染（如 `{{.Topic}}`）
- `Reload()` 热更新，无需重启
- 优先级：文件模板 > config.yaml fallback

### 平台发布

| 平台 | 方式 | 关键点 |
|------|------|--------|
| 知乎 | Cookie + HTTP API | 创建草稿 → 发布文章/想法 |
| 小红书 | Rod 浏览器自动化 | 启动Chrome → 注入Cookie → 填写表单 → 上传图片 → 发布 → 截图 |

### 调度器

- 基于 `time.Ticker` 的轻量实现
- 启动时和每 N 分钟扫描排期表
- 发布失败自动重试（可配置次数和间隔）
- 小红书发布自动截图留存

---

## 配置管理优先级

```
环境变量 (.env) > config.yaml 中的 ${VAR} 引用 > config.yaml 硬编码值
```

- `.env` 不提交 Git
- 密钥（API Key、Cookie）一律走环境变量

---

## 测试

```bash
go test ./... -v

# 覆盖范围（20个测试）：
# provider/   — Parser（4）+ Router（4）+ PromptManager（6）
# scheduler/  — Scheduler（6）
```

---

## 注意事项

- **Go regexp 不支持 lookahead `(?=...)`** — 文本解析用 `strings.Index`，不用 Perl 风格正则
- **Claude Code CLI 并发控制** — 全局锁不可移除
- **模型切换** — 改 `config.yaml` routing 即可，Provider 代码不变
- **小红书反爬** — 浏览器自动化频率控制在每天 1-2 条
- **Cookie 过期** — 知乎/小红书 Cookie 失效时需手动更新 `.env` 或 `.cookie` 文件
- **先跑内容再跑系统** — 写代码前先用 Claude Code 手动生成内容测试方向

# 📝 自媒体自动化发布系统

一个基于 Go 的本地化自媒体内容生产与发布工具。AI 辅助生成小红书笔记和知乎文章，支持定时自动发布，并提供竞品内容采集功能。

## ✨ 功能

- 🤖 **AI 文案生成** — 使用 Claude Code CLI 生成小红书/知乎风格文案，也可降级到 Google Gemini
- 🖼️ **AI 封面图** — 使用 Google Gemini 生成封面图，支持多种宽高比（1:1、3:4、16:9）
- 🔐 **自动登录** — 支持账号密码配置，自动登录获取 Cookie，无需手动复制（v2.0 新特性）
- 🔍 **内容采集** — 搜索知乎/小红书上的类似内容，关键字相关度评分，辅助选题调研（v2.0 新特性）
- 📋 **内容管理** — Web 界面管理选题、编辑、审核、排期全流程
- ⏰ **定时发布** — 设置发布时间，调度器自动扫描并发布到目标平台
- 📘 **知乎发布** — Cookie/账号密码鉴权，支持发布文章和想法
- 📕 **小红书发布** — 浏览器自动化，模拟人工操作发布笔记
- 🔀 **模型路由** — 可插拔的 AI Provider 架构，轻松切换底层模型

## 🚀 快速开始

### 环境要求

- Go 1.22+
- MySQL 8.0
- Chrome / Edge 浏览器（小红书发布和内容采集需要）
- Claude Code CLI（`claude` 命令可用）
- Google Gemini API Key（封面图生成，可选）

### 安装

```bash
# 1. 克隆项目
git clone https://github.com/lmj1002/auto-publisher.git
cd auto-publisher

# 2. 配置环境变量
cp .env.example .env
# 编辑 .env 填入你的配置

# 3. 创建数据库
mysql -u root -e "CREATE DATABASE IF NOT EXISTS auto_publisher DEFAULT CHARSET utf8mb4"

# 4. 安装依赖并编译
go mod tidy
go build -o auto-publisher ./cmd/server/

# 5. 启动
./auto-publisher
# Windows:
# auto-publisher.exe
```

浏览器打开 `http://localhost:8080` 即可使用。

## ⚙️ 配置

### 方式一：账号密码自动登录（推荐）

在 `config.yaml` 中配置账号密码，系统自动登录并缓存 Cookie：

```yaml
platforms:
  zhihu:
    enabled: true
    max_daily_posts: 3
    username: "${ZHIHU_USERNAME}"  # 或直接填写手机号
    password: "${ZHIHU_PASSWORD}"  # 或直接填写密码
  xiaohongshu:
    enabled: true
    max_daily_posts: 2
    username: "${XHS_USERNAME}"
    password: "${XHS_PASSWORD}"
```

在 `.env` 中设置实际值：

```bash
ZHIHU_USERNAME=your_phone_number
ZHIHU_PASSWORD=your_password
XHS_USERNAME=your_phone_number
XHS_PASSWORD=your_password
```

Cookie 自动缓存到 `data/cookies/` 目录，重启无需重新登录，7 天自动过期刷新。

### 方式二：手动 Cookie（备用）

```bash
# 从浏览器开发者工具复制 Cookie
# F12 → Application → Cookies → 复制所有 Cookie 值
ZHIHU_COOKIE=your-full-cookie-string
XHS_COOKIE=your-full-cookie-string
# 也可放在项目根目录的 zhihu.cookie / xiaohongshu.cookie 文件中
```

### 其他环境变量 (`.env`)

```bash
# 数据库
DB_HOST=127.0.0.1
DB_USER=root
DB_PASSWORD=
DB_NAME=auto_publisher

# Google Gemini API Key
GEMINI_API_KEY=your-key-here

# Claude Code 工作目录
CLAUDE_CODE_WORK_DIR=/path/to/your/project

# 日志级别（debug / 留空=info）
LOG_LEVEL=info
```

## 📖 使用流程

```
选题输入 → AI 生成初稿 → 采集类似内容参考 → 人工修改定稿 → 设置排期 → 自动发布
```

1. **创建内容** — 输入选题和核心观点，选择目标平台
2. **AI 生成** — 点击"AI 生成"，系统调用 Claude Code CLI 生成文案
3. **采集参考** — AI 生成后可触发内容采集，搜索平台上的同主题内容作为参考
4. **人工审核** — 在预览区查看、修改文案，确认无误后点"定稿"
5. **设置排期** — 选择发布时间，或点"立即发布"
6. **自动发布** — 调度器到点自动将内容发布到对应平台

### 内容采集 API

```bash
# 采集相似内容（基于AI生成内容的关键字搜索）
curl -X POST http://localhost:8080/api/collect \
  -H "Content-Type: application/json" \
  -d '{"content_id": 1, "max_results": 10}'

# 查看已采集内容
curl http://localhost:8080/api/collected?content_id=1
```

## 🏗️ 架构

```
Web 管理后台 (Go template + JS)
        │
   Gin API 服务
        │
  ┌─────┼─────────────┐
  │     │             │
  ▼     ▼             ▼
AI路由  发布层        采集层 (NEW)
  │     │             │
  ├─── Claude CLI     ├─── 知乎 HTTP API
  ├─── Gemini API     ├─── 小红书 Rod 自动化
  │     │             │
  │     ├── CookieManager (自动登录 + 缓存)
  │     │             │
  ▼     ▼             ▼
MySQL ───────── 内容 + 采集数据存储
```

## 📁 项目结构

```
auto-publisher/
├── cmd/server/           # 应用入口
├── internal/
│   ├── config/           # 配置加载（含账号密码解析）
│   ├── model/            # 数据模型（含采集内容模型）
│   ├── handler/          # HTTP 处理器（含采集 API）
│   ├── provider/         # AI Provider 层
│   ├── publisher/        # 平台发布层（含 CookieManager）
│   ├── collector/        # 内容采集层（NEW）
│   └── scheduler/        # 定时调度
├── prompts/              # Prompt 模板
├── web/templates/        # Web 页面
├── config.yaml           # 主配置
└── .env.example          # 环境变量模板
```

## 🧪 Go 开发规范

本项目遵循以下 Go 最佳实践：

| 规范 | 实践 |
|------|------|
| **日志** | `log/slog` 结构化日志，支持 `Debug/Info/Warn/Error` 四级 |
| **错误处理** | `fmt.Errorf("...: %w", err)` 错误包装，保留调用链 |
| **并发** | `sync.Mutex/RWMutex` 保护共享状态，Claude CLI 全局互斥 |
| **接口** | 小而专注（ISP），`AIProvider` / `Publisher` / `Collector` 核心抽象 |
| **配置** | 函数式选项模式（Functional Options），`${ENV_VAR}` 环境变量插值 |
| **组织** | 标准 `cmd/internal` 布局，单一职责包设计 |
| **文档** | 所有导出符号 godoc 注释 |

## 🧪 测试

```bash
go test ./... -v

# 覆盖范围（20个测试）：
# provider/   — 解析器（4）+ 路由器（4）+ Prompt管理（6）
# scheduler/  — 调度器（6）
```

## 📄 License

MIT

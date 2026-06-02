# 📝 自媒体自动化发布系统

一个基于 Go 的本地化自媒体内容生产与发布工具。AI 辅助生成小红书笔记和知乎文章，支持定时自动发布。

## ✨ 功能

- 🤖 **AI 文案生成** — 使用 Claude Code CLI 生成小红书/知乎风格文案，也可降级到 Google Gemini
- 🖼️ **AI 封面图** — 使用 Google Gemini 生成封面图，支持多种宽高比
- 📋 **内容管理** — Web 界面管理选题、编辑、审核、排期全流程
- ⏰ **定时发布** — 设置发布时间，调度器自动扫描并发布到目标平台
- 📘 **知乎发布** — Cookie 鉴权，支持发布文章和想法
- 📕 **小红书发布** — 浏览器自动化，模拟人工操作发布笔记
- 🔀 **模型路由** — 可插拔的 AI Provider 架构，轻松切换底层模型

## 🚀 快速开始

### 环境要求

- Go 1.22+
- MySQL 8.0
- Chrome 浏览器（小红书发布需要）
- Claude Code CLI（`claude` 命令可用）
- Google Gemini API Key（封面图生成，可选）

### 安装

```bash
# 1. 克隆或下载项目
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
# 或 Windows:
auto-publisher.exe
```

浏览器打开 `http://localhost:8080` 即可使用。

## ⚙️ 配置

### 环境变量 (`.env`)

```bash
# 数据库（可选，默认连接本地 root 无密码）
DB_HOST=127.0.0.1
DB_USER=root
DB_PASSWORD=
DB_NAME=auto_publisher

# Google Gemini API Key（封面图生成）
GEMINI_API_KEY=your-key-here

# Claude Code 工作目录
CLAUDE_CODE_WORK_DIR=/path/to/your/project

# 知乎 Cookie（从浏览器复制）
ZHIHU_COOKIE=your-cookie-string

# 小红书 Cookie（从浏览器复制）
XHS_COOKIE=your-cookie-string
```

> Cookie 获取方式：浏览器登录后，F12 → Application → Cookies → 复制所有 Cookie

### 主配置 (`config.yaml`)

- 服务器端口、数据目录
- AI 模型路由规则（默认/降级）
- 各平台 Prompt 模板
- 发布频率限制

## 📖 使用流程

```
选题输入 → AI 生成初稿 → 人工修改定稿 → 设置排期 → 自动发布
```

1. **创建内容** — 输入选题和核心观点，选择目标平台
2. **AI 生成** — 点击"AI 生成"，系统调用 Claude Code CLI 生成文案
3. **人工审核** — 在预览区查看、修改文案，确认无误后点"定稿"
4. **设置排期** — 选择发布时间，或点"立即发布"
5. **自动发布** — 调度器到点自动将内容发布到对应平台

## 🏗️ 架构

```
Web 管理后台 (Go template + JS)
        │
   Gin API 服务
        │
  ┌─────┼─────────┐
  │     │         │
  ▼     ▼         ▼
AI路由  发布层    调度器
  │     │         │
  ├─── Claude CLI  │         │
  ├─── Gemini API  │         │
  │     ├── 知乎HTTP API  │
  │     └── 小红书Rod自动化
  │              │
  ▼              ▼
MySQL ───── 内容存储
```

## 📁 项目结构

```
auto-publisher/
├── cmd/server/           # 应用入口
├── internal/
│   ├── config/           # 配置加载
│   ├── model/            # 数据模型
│   ├── handler/          # HTTP 处理器
│   ├── provider/         # AI Provider 层
│   ├── publisher/        # 平台发布层
│   └── scheduler/        # 定时调度
├── prompts/              # Prompt 模板
├── web/templates/        # Web 页面
├── config.yaml           # 主配置
└── .env.example          # 环境变量模板
```

## 🧪 测试

```bash
go test ./... -v
```

## 📄 License

MIT

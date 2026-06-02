# 自媒体自动化发布工作流 — 完整项目文档

> 创建时间：2026-06-02  
> 状态：✅ 全部 4 个 Phase 完成

---

## 项目概述

**目标：** 搭建一套本地自动化工作流，支持 **小红书 + 知乎** 双平台的定时图文内容发布，AI 辅助生成初稿，人工审核后发布，最终实现副业变现。

**核心设计理念：**

| 维度 | 方案 |
|------|------|
| 文字 AI | Claude Code CLI 子进程调用（背后是 DeepSeek） |
| 图片 AI | Google Gemini API（多模态，原生图片生成） |
| 数据库 | MySQL 8.0 |
| 模型管理 | 多模型路由层，可插拔切换 |
| 部署依赖 | Go 二进制 + MySQL + Chrome 浏览器 |

---

## 总体架构

```
┌─────────────────────────────────────────────────────┐
│               Web 管理后台 (Go template)              │
│       选题录入 | 内容编辑 | 排期 | 状态查看            │
└──────────────────────┬──────────────────────────────┘
                       │ HTTP
┌──────────────────────▼──────────────────────────────┐
│               Go API 服务 (Gin)                       │
│                                                      │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────┐  │
│  │ 内容 CRUD   │  │ 模型路由层    │  │ 定时调度    │  │
│  │ Handler     │  │ ModelRouter  │  │ Scheduler   │  │
│  └──────┬──────┘  └──────┬───────┘  └─────┬──────┘  │
│         │                │                 │         │
└─────────┼────────────────┼─────────────────┼─────────┘
          │                │                 │
          ▼                ▼                 ▼
┌──────────────┐  ┌─────────────────┐  ┌────────────┐
│   MySQL      │  │  AI Provider 层  │  │   Cron     │
│  (内容存储)   │  │                 │  │ (robfig/   │
│              │  │ ┌─────────────┐ │  │  cron)     │
│              │  │ │ TextProvider│ │  └────────────┘
│              │  │ │ (Claude CLI)│ │
└──────────────┘  │ ├─────────────┤ │
                  │ │ImageProvider│ │
                  │ │ (Gemini API)│ │
                  │ ├─────────────┤ │
                  │ │VideoProvider│ │
                  │ │ (预留接口)   │ │
                  │ └─────────────┘ │
                  └─────────────────┘
```

---

## 项目目录结构

```
F:\study\auto-publisher\
├── cmd/
│   └── server/
│       └── main.go              # 入口
├── internal/
│   ├── config/
│   │   └── config.go            # 配置加载（含多模型配置解析）
│   ├── model/
│   │   └── content.go           # 数据模型
│   ├── handler/
│   │   └── content.go           # HTTP Handler
│   ├── provider/                # AI Provider 层
│   │   ├── provider.go          # 接口定义 + 模型路由
│   │   ├── claude_code.go       # Claude Code CLI Provider
│   │   ├── gemini.go            # Gemini Provider（文字+图片）
│   │   └── router.go            # 路由逻辑
│   ├── publisher/               # 发布模块
│   │   ├── publisher.go         # 接口定义
│   │   ├── zhihu.go             # 知乎发布
│   │   └── xiaohongshu.go       # 小红书发布
│   └── scheduler/
│       └── scheduler.go         # 定时调度
├── web/
│   └── templates/
│       └── index.html           # 极简管理页面（Go template）
├── data/                        # 本地数据目录（gitignore）
│   ├── images/                  # 生成的图片
│   ├── prompts/                 # 临时 prompt 文件
│   └── images/                  # 生成的图片
├── prompts/                     # Prompt 模板库存放目录
│   ├── xiaohongshu_system.md    # 小红书 System Prompt
│   ├── zhihu_system.md          # 知乎 System Prompt
│   └── image_cover.md           # 封面图 Prompt 模板
├── PROJECT.md                   # 本文档
├── config.yaml                  # 配置文件
├── go.mod
└── go.sum
```

---

## 核心模块设计

### 1. 模型路由层

统一的 AI Provider 接口，按内容类型自动路由到不同模型：

```
AIProvider 接口
├── ClaudeCodeProvider  → text  → Claude Code CLI 子进程 → DeepSeek
├── GeminiImageProvider → image → Gemini API → gemini-2.0-flash-exp
├── GeminiTextProvider  → text  → Gemini API → gemini-1.5-pro (降级备选)
└── VideoProvider       → video → (预留)
```

路由规则：
- `text` → 优先 `claude-code`，不可用时降级 `gemini`
- `image` → `gemini`
- `video` → 暂无

### 2. Claude Code CLI 调用方案

```go
// Go → os/exec → claude CLI
cmd := exec.CommandContext(ctx, "claude",
    "-p", prompt,           // 非交互模式
    "--output-format", "text",
    "--max-turns", "1",     // 单轮
)
cmd.Dir = workDir
output, _ := cmd.CombinedOutput()
// 解析标记格式输出：---TITLE--- / ---BODY--- / ---TAGS---
```

### 3. Gemini 图片生成

```
POST /v1beta/models/gemini-2.0-flash-exp:generateContent
{
  "contents": [{"parts": [{"text": "生成封面图: ..."}]}],
  "generationConfig": {
    "responseModalities": ["TEXT", "IMAGE"],
    "imageConfig": {"aspectRatio": "3:4"}
  }
}
```

### 4. 发布模块

| 平台 | 方案 | 说明 |
|------|------|------|
| 知乎 | Cookie 鉴权 + HTTP 内部 API | 发布文章/想法，上传图片 |
| 小红书 | Rod 浏览器自动化 | 模拟登录→创作者中心→填写→发布 |

### 5. 内容状态流转

```
draft → ai_generated → review → approved → scheduled → published
                                                    ↓
                                                  failed → (重试) → published
```

---

## 实施路线图

### Phase 1（第1周）：跑通 Claude Code CLI 调用

**目标：手动发布 + AI 辅助，不做自动化**

- [x] Go 项目骨架（Gin + GORM + MySQL）— config.yaml, config.go, model/content.go
- [x] 内容 CRUD API（Handler + 路由）
- [x] ClaudeCodeProvider 实现（`os/exec` 调 `claude` CLI）
- [x] GeminiImageProvider 实现（图片生成 + 文字降级）
- [x] Router 模型路由器（路由规则 + fallback）
- [x] 输出解析器（解析 ---TITLE--- / ---BODY--- / ---TAGS--- 格式）
- [x] 极简 HTML 管理页面（Go template + 原生 JS）
- [x] main.go 入口 + 路由注册
- [x] 编译通过（auto-publisher.exe）

**验证标准：** 在 Web 页面输入选题，点击按钮，拿到 Claude Code 生成的文案存入 SQLite。

### Phase 2（第2周）：接入 Gemini 图片生成 + 模型路由

**目标：文字+图片一体化生成**

- [x] Gemini Provider 实现（文字 + 图片两个子 Provider）
- [x] Router 模型路由器实现（含 fallback 降级）
- [x] Prompt 模板管理（文件存储 + 热加载）
- [x] .env 环境变量管理
- [x] 单元测试覆盖（Parser / Router / PromptManager）
- [x] 端到端：输入选题 → 同时生成文案 + 封面图

### Phase 3（第3-4周）：定时调度 + 知乎发布

**目标：知乎实现定时自动发布**

- [x] 知乎 Publisher（Cookie 鉴权 + HTTP API）
- [x] 轻量级定时调度器（标准库 time.Ticker + goroutine）
- [x] 定时发布流程：扫描排期表 → 触发发布 → 记录结果
- [x] 发布失败重试 + 错误记录
- [x] 手动立即发布 API
- [x] 调度器单元测试（6 个）

### Phase 4（第5-6周）：小红书发布 + 管理后台完善

**目标：全流程闭环**

- [x] 小红书 Publisher（Rod 浏览器自动化）
- [x] 管理后台样式完善（发布按钮、排期选择器、状态指示器）
- [x] 发布数据追踪（PublishResult 记录 + 截图）
- [x] .env 环境变量管理（ZHIHU_COOKIE + XHS_COOKIE）

---

## 技术栈

| 组件 | 选型 | 理由 |
|------|------|------|
| 语言 | Go 1.22+ | 主力语言，并发模型适合多 Worker |
| Web 框架 | Gin | 轻量高性能 |
| ORM | GORM + SQLite | GORM 统一接口，SQLite 零配置 |
| AI 文字 | Claude Code CLI | 复用已有配置，模型透明切换 |
| AI 图片 | Google Gemini API | 多模态，一个 Key 搞定图文 |
| 浏览器自动化 | go-rod/rod | Go 原生 Chrome DevTools 驱动 |
| 定时任务 | robfig/cron | 轻量级 Cron 库 |
| 前端 | Go template + 内嵌 CSS | 极简，无需前端构建工具链 |

---

## 环境依赖

| 依赖 | 说明 | 状态 |
|------|------|------|
| Go 1.22+ | 编译运行 | ✅ |
| Claude Code CLI | `claude` 命令在 PATH 中 | ✅ |
| Google Gemini API Key | 免费申请: https://aistudio.google.com/ | 🔧 需申请 |
| Chrome 浏览器 | Rod 驱动需要（Phase 4） | ✅ |

---

## 注意事项

1. **先跑内容再跑系统** — 写代码前，先用 Claude Code 手动生成 5 篇内容发到平台测试方向
2. **不要做"全自动"** — AI 生成的内容必须人工审核后再发布
3. **小红书反爬严格** — 用 Rod 浏览器自动化，控制频率（每天 1-2 条）
4. **Cookie 管理** — 平台登录态会过期，需做检测 + 告警
5. **并发控制** — Claude Code CLI 同时只允许 1 个实例

---

## 配置说明

配置文件 `config.yaml` 关键部分：

```yaml
models:
  providers:
    claude-code:          # 文字生成主力
      work_dir: "F:\\study"  # Claude Code 工作目录
    gemini:               # 图片生成
      api_key: "${GEMINI_API_KEY}"  # 从环境变量读取
  routing:
    text:
      default: "claude-code"
      fallback: "gemini"    # Claude CLI 不可用时降级
    image:
      default: "gemini"
```

环境变量：
- `GEMINI_API_KEY` — Google Gemini API 密钥

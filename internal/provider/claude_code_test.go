package provider

import (
	"testing"

	"auto-publisher/internal/model"
)

func TestParseMarkedOutput_Xiaohongshu(t *testing.T) {
	raw := `---TITLE---
标题1：短剧App后台揭秘：一个后端开发的自白
标题2：我开发了短剧App的后台，才知道这些秘密
标题3：6年后端告诉你，短剧平台的技术真相
---BODY---
姐妹们！今天给大家聊点不一样的 🎬

你们刷短剧的时候，有没有想过这些视频是怎么推到你面前的？

其实背后的推荐算法超级有意思！我用最通俗的话给大家解释一下...👇

简单来说就是三个步骤：
1️⃣ 你看了什么 → 打标签
2️⃣ 找和你类似的人 → 协同过滤
3️⃣ 把你们互相喜欢的推给对方 → 推荐

是不是没想的那么玄乎？
---TAGS---
#短剧 #后端开发 #程序员日常 #技术科普 #自媒体
`

	parsed := ParseMarkedOutput(raw, model.PlatformXiaohongshu)

	if len(parsed.Titles) != 3 {
		t.Errorf("期望 3 个标题，实际 %d 个", len(parsed.Titles))
	}

	if parsed.Titles[0] != "短剧App后台揭秘：一个后端开发的自白" {
		t.Errorf("标题1 解析错误: %s", parsed.Titles[0])
	}

	if parsed.Body == "" {
		t.Error("正文不应为空")
	}

	if len(parsed.Tags) != 5 {
		t.Errorf("期望 5 个标签，实际 %d 个", len(parsed.Tags))
	}

	if parsed.Tags[0] != "#短剧" {
		t.Errorf("标签1 解析错误: %s", parsed.Tags[0])
	}
}

func TestParseMarkedOutput_Zhihu(t *testing.T) {
	raw := `---TITLE---
主标题：一个后端开发眼中的短剧行业：技术、商业与机会
备选标题：短剧平台背后的技术架构，远比你想的复杂
---BODY---
去年年底，团队接到一个需求：做一个短剧App的后台管理系统。

当时我对短剧行业的理解还停留在"不就是竖屏版的电视剧吗"。

但真正深入进去之后，我发现这个行业远比表面看起来复杂得多...

## 短剧平台的技术架构

一个典型的短剧平台，后端至少包含以下几个核心模块...

## 行业现状与机会

从技术角度看，短剧行业目前还处于野蛮生长期...

你们觉得短剧这个赛道还能火多久？欢迎在评论区聊聊你的看法。
---TOPICS---
#短剧 #技术架构 #行业分析
`

	parsed := ParseMarkedOutput(raw, model.PlatformZhihu)

	if len(parsed.Titles) != 2 {
		t.Errorf("期望 2 个标题，实际 %d 个", len(parsed.Titles))
	}

	if parsed.Titles[0] != "一个后端开发眼中的短剧行业：技术、商业与机会" {
		t.Errorf("主标题解析错误: %s", parsed.Titles[0])
	}

	if parsed.Body == "" {
		t.Error("正文不应为空")
	}

	if len(parsed.Tags) != 3 {
		t.Errorf("期望 3 个标签，实际 %d 个", len(parsed.Tags))
	}
}

func TestParseMarkedOutput_NoMarkers(t *testing.T) {
	// 无标记格式时的降级处理
	raw := "这是一篇普通的文章内容，没有任何标记格式。"

	parsed := ParseMarkedOutput(raw, model.PlatformXiaohongshu)

	if parsed.Body != raw {
		t.Error("无标记时应把全部内容当作正文")
	}

	if len(parsed.Titles) != 0 {
		t.Error("无标记时不应有标题")
	}
}

func TestParseMarkedOutput_EmptyInput(t *testing.T) {
	parsed := ParseMarkedOutput("", model.PlatformXiaohongshu)

	if parsed.Body != "" {
		t.Error("空输入时正文应为空")
	}

	if len(parsed.Titles) != 0 {
		t.Error("空输入时不应有标题")
	}
}

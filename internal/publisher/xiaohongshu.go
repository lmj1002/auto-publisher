package publisher

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"auto-publisher/internal/model"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// XHSPublisher 小红书发布器（浏览器自动化）
type XHSPublisher struct {
	cookieStr  string
	browser    *rod.Browser
	headless   bool
	screenshotDir string
}

// NewXHSPublisher 创建小红书发布器
func NewXHSPublisher(cookieSource string, screenshotDir string, headless bool) *XHSPublisher {
	cookieStr := cookieSource
	if strings.HasSuffix(cookieSource, ".txt") || strings.HasSuffix(cookieSource, ".cookie") {
		if data, err := os.ReadFile(cookieSource); err == nil {
			cookieStr = strings.TrimSpace(string(data))
		}
	}

	return &XHSPublisher{
		cookieStr:     cookieStr,
		headless:      headless,
		screenshotDir: screenshotDir,
	}
}

// Name 名称
func (p *XHSPublisher) Name() string {
	return "xiaohongshu"
}

// Platform 平台
func (p *XHSPublisher) Platform() Platform {
	return PlatformXiaohongshu
}

// IsAvailable 检查是否可用
func (p *XHSPublisher) IsAvailable(ctx context.Context) bool {
	if p.cookieStr == "" {
		return false
	}
	return p.checkBrowser(ctx) == nil
}

// checkBrowser 检查浏览器是否可用
func (p *XHSPublisher) checkBrowser(ctx context.Context) error {
	path, found := launcher.LookPath()
	if !found {
		return fmt.Errorf("未找到 Chrome/Edge 浏览器")
	}
	log.Printf("[XHS] 浏览器路径: %s", path)
	return nil
}

// Validate 校验登录态
func (p *XHSPublisher) Validate(ctx context.Context) error {
	browser, page, err := p.launchAndNavigate(ctx, "https://creator.xiaohongshu.com/")
	if err != nil {
		return err
	}
	defer browser.Close()

	// 等待页面加载，检查是否跳转到登录页
	time.Sleep(3 * time.Second)

	url := page.MustInfo().URL
	if strings.Contains(url, "login") {
		// 截图保存
		p.screenshot(page, "login_failed")
		return fmt.Errorf("小红书登录态失效，当前页面: %s", url)
	}

	log.Println("[XHS] ✅ 登录态有效")
	return nil
}

// Publish 发布内容到小红书
func (p *XHSPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	browser, page, err := p.launchAndNavigate(ctx, "https://creator.xiaohongshu.com/publish/publish")
	if err != nil {
		return p.failResult("启动浏览器失败", err, start), err
	}
	defer browser.Close()

	title := content.XHSTitle
	if title == "" {
		title = content.Topic
	}
	body := content.XHSBody

	log.Printf("[XHS] 开始发布: %s", title)

	// 1. 等待发布页面加载完成
	if err := p.waitForEditor(page); err != nil {
		p.screenshot(page, "editor_load_failed")
		return p.failResult("发布页面加载失败", err, start), err
	}

	// 2. 填写标题
	if err := p.fillTitle(page, title); err != nil {
		p.screenshot(page, "fill_title_failed")
		return p.failResult("填写标题失败", err, start), err
	}
	log.Println("[XHS] 标题已填写")

	// 3. 填写正文
	if err := p.fillBody(page, body); err != nil {
		p.screenshot(page, "fill_body_failed")
		return p.failResult("填写正文失败", err, start), err
	}
	log.Println("[XHS] 正文已填写")

	// 4. 上传图片（如果有）
	if content.XHSImages != "" {
		imagePaths := strings.Split(content.XHSImages, ",")
		if err := p.uploadImages(page, imagePaths); err != nil {
			log.Printf("[XHS] ⚠️ 图片上传失败（继续发布）: %v", err)
		}
	}

	// 5. 添加话题标签
	// 标签从正文中提取 #xxx 格式
	tags := extractTags(body)
	if len(tags) > 0 {
		if err := p.addTags(page, tags); err != nil {
			log.Printf("[XHS] ⚠️ 添加标签失败（继续发布）: %v", err)
		}
	}

	// 6. 发布前截图
	p.screenshot(page, fmt.Sprintf("before_publish_%d", content.ID))

	// 7. 点击发布按钮
	if err := p.clickPublish(page); err != nil {
		p.screenshot(page, "publish_click_failed")
		return p.failResult("点击发布失败", err, start), err
	}

	log.Println("[XHS] 发布按钮已点击")

	// 8. 等待发布结果
	time.Sleep(5 * time.Second)
	p.screenshot(page, fmt.Sprintf("after_publish_%d", content.ID))

	log.Println("[XHS] ✅ 发布完成")

	return &PublishResult{
		Success:     true,
		Platform:    "xiaohongshu",
		ContentID:   fmt.Sprintf("xhs_%d", content.ID),
		URL:         "https://www.xiaohongshu.com/explore", // 发布后无法立即获取链接
		Message:     "发布成功（请登录小红书确认）",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// launchAndNavigate 启动浏览器并导航到指定页面
func (p *XHSPublisher) launchAndNavigate(ctx context.Context, targetURL string) (*rod.Browser, *rod.Page, error) {
	l := launcher.New()
	if p.headless {
		l = l.Headless(true)
	}
	l = l.NoSandbox(true)

	// 禁用自动化检测标志
	l = l.Set("disable-blink-features", "AutomationControlled")

	url, err := l.Launch()
	if err != nil {
		return nil, nil, fmt.Errorf("启动浏览器失败: %w", err)
	}

	browser := rod.New().ControlURL(url).MustConnect()

	// 注入 Cookie
	if p.cookieStr != "" {
		page := browser.MustPage("")
		cookies := parseCookies(p.cookieStr, ".xiaohongshu.com")
		if len(cookies) > 0 {
			page.SetCookies(cookies)
		}
	}

	page := browser.MustPage(targetURL)
	// 等待页面基本加载
	page.MustWaitLoad()

	return browser, page, nil
}

// waitForEditor 等待编辑器加载完成
func (p *XHSPublisher) waitForEditor(page *rod.Page) error {
	// 等待标题输入框出现（多个可能的选择器）
	selectors := []string{
		"[placeholder*='标题']",
		"#title",
		".title-input input",
		".publish-title input",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(10 * time.Second).Element(sel)
		if err == nil && el != nil {
			return nil
		}
	}

	return fmt.Errorf("未能找到标题输入框，请检查小红书创作者页面是否改版")
}

// fillTitle 填写标题
func (p *XHSPublisher) fillTitle(page *rod.Page, title string) error {
	selectors := []string{
		"[placeholder*='标题']",
		"#title",
		".title-input input",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			el.MustClick()
			el.MustInput(title)
			return nil
		}
	}

	return fmt.Errorf("未能填写标题")
}

// fillBody 填写正文
func (p *XHSPublisher) fillBody(page *rod.Page, body string) error {
	selectors := []string{
		"[placeholder*='正文']",
		"[contenteditable='true']",
		"#content",
		".ql-editor",
		".rich-text-editor",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			el.MustClick()
			el.MustInput(body)
			return nil
		}
	}

	return fmt.Errorf("未能填写正文")
}

// uploadImages 上传图片
func (p *XHSPublisher) uploadImages(page *rod.Page, imagePaths []string) error {
	fileInputs, err := page.Elements("input[type='file']")
	if err != nil || len(fileInputs) == 0 {
		return fmt.Errorf("未找到图片上传入口")
	}

	for _, path := range imagePaths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		// 如果是相对路径，转换为绝对路径
		if !filepath.IsAbs(path) {
			absPath, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			path = absPath
		}

		fileInputs[0].MustSetFiles(path)
		time.Sleep(2 * time.Second) // 等待上传完成
	}

	return nil
}

// addTags 添加话题标签
func (p *XHSPublisher) addTags(page *rod.Page, tags []string) error {
	// 在正文末尾添加标签
	tagStr := "\n\n" + strings.Join(tags, " ")

	bodyEl, err := page.Timeout(3 * time.Second).Element("[contenteditable='true']")
	if err != nil {
		return err
	}

	return bodyEl.Input(tagStr)
}

// clickPublish 点击发布按钮
func (p *XHSPublisher) clickPublish(page *rod.Page) error {
	selectors := []string{
		".publish-btn",
		"button:has-text('发布')",
		"[class*='publish']",
		".submit-btn",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err == nil {
			return el.Click(proto.InputMouseButtonLeft, 1)
		}
	}

	return fmt.Errorf("未找到发布按钮")
}

// screenshot 截图
func (p *XHSPublisher) screenshot(page *rod.Page, name string) {
	os.MkdirAll(p.screenshotDir, 0755)
	path := filepath.Join(p.screenshotDir, fmt.Sprintf("%s_%s.png", time.Now().Format("20060102_150405"), name))
	data, err := page.Screenshot(true, &proto.PageCaptureScreenshot{})
	if err != nil {
		log.Printf("[XHS] 截图失败: %v", err)
		return
	}
	os.WriteFile(path, data, 0644)
	log.Printf("[XHS] 截图已保存: %s", path)
}

// failResult 生成失败结果
func (p *XHSPublisher) failResult(msg string, err error, start time.Time) *PublishResult {
	return &PublishResult{
		Success:     false,
		Platform:    "xiaohongshu",
		Message:     fmt.Sprintf("%s: %v", msg, err),
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}
}

// parseCookies 解析 Cookie 字符串为 Rod 格式
func parseCookies(cookieStr string, domain string) []*proto.NetworkCookieParam {
	var cookies []*proto.NetworkCookieParam

	pairs := strings.Split(cookieStr, ";")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		cookies = append(cookies, &proto.NetworkCookieParam{
			Name:   strings.TrimSpace(kv[0]),
			Value:  strings.TrimSpace(kv[1]),
			Domain: domain,
		})
	}

	return cookies
}

// extractTags 从正文中提取 #xxx 格式的标签
func extractTags(body string) []string {
	var tags []string
	words := strings.Fields(body)
	for _, word := range words {
		if strings.HasPrefix(word, "#") && len(word) > 1 {
			tags = append(tags, word)
		}
	}
	return tags
}

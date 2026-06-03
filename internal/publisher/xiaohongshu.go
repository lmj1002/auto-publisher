package publisher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"auto-publisher/internal/model"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// XHSPublisher publishes content to Xiaohongshu (Little Red Book) via browser automation.
// It supports both account/password auto-login and manual cookie injection.
type XHSPublisher struct {
	cookieMgr     *CookieManager
	browser       *rod.Browser
	headless      bool
	screenshotDir string
	username      string
	password      string
}

// XHSPublisherOption is a functional option for configuring XHSPublisher.
type XHSPublisherOption func(*XHSPublisher)

// WithXHSHeadless sets headless mode for the browser.
func WithXHSHeadless(headless bool) XHSPublisherOption {
	return func(p *XHSPublisher) {
		p.headless = headless
	}
}

// WithXHSCredentials sets account credentials for auto-login.
func WithXHSCredentials(username, password string) XHSPublisherOption {
	return func(p *XHSPublisher) {
		p.username = username
		p.password = password
	}
}

// WithXHSManualCookie sets a manual cookie string as fallback.
func WithXHSManualCookie(cookie string) XHSPublisherOption {
	return func(p *XHSPublisher) {
		p.cookieMgr = NewCookieManager("xiaohongshu", "", WithManualCookie(cookie))
	}
}

// NewXHSPublisher creates a new Xiaohongshu publisher.
// cookieSource: cookie string or path to .cookie/.txt file (legacy fallback).
// screenshotDir: directory for debug screenshots.
// headless: run browser in headless mode.
func NewXHSPublisher(cookieSource string, screenshotDir string, headless bool) *XHSPublisher {
	cookieStr := cookieSource
	if strings.HasSuffix(cookieSource, ".txt") || strings.HasSuffix(cookieSource, ".cookie") {
		if data, err := os.ReadFile(cookieSource); err == nil {
			cookieStr = strings.TrimSpace(string(data))
		}
	}

	return &XHSPublisher{
		cookieMgr:     NewCookieManager("xiaohongshu", "", WithManualCookie(cookieStr)),
		headless:      headless,
		screenshotDir: screenshotDir,
	}
}

// NewXHSPublisherWithLogin creates a Xiaohongshu publisher with account/password auto-login.
// cookieDir is where cached cookies are stored.
func NewXHSPublisherWithLogin(cookieDir, screenshotDir, username, password string, headless bool) *XHSPublisher {
	p := &XHSPublisher{
		headless:      headless,
		screenshotDir: screenshotDir,
		username:      username,
		password:      password,
	}

	loginFunc := func(ctx context.Context) (string, error) {
		return p.performLogin(ctx)
	}

	manualCookie := os.Getenv("XHS_COOKIE")
	if manualCookie == "" {
		if data, err := os.ReadFile("xiaohongshu.cookie"); err == nil {
			manualCookie = strings.TrimSpace(string(data))
		}
	}

	p.cookieMgr = NewCookieManager("xiaohongshu", cookieDir,
		WithLoginFunc(loginFunc),
		WithManualCookie(manualCookie),
	)
	return p
}

// Name returns the publisher name.
func (p *XHSPublisher) Name() string {
	return "xiaohongshu"
}

// Platform returns the target platform identifier.
func (p *XHSPublisher) Platform() Platform {
	return PlatformXiaohongshu
}

// IsAvailable checks whether the publisher is ready to use.
func (p *XHSPublisher) IsAvailable(ctx context.Context) bool {
	if !p.cookieMgr.IsAvailable() {
		return false
	}
	return p.checkBrowser(ctx) == nil
}

// checkBrowser verifies that a Chrome/Edge browser executable is available.
func (p *XHSPublisher) checkBrowser(ctx context.Context) error {
	path, found := launcher.LookPath()
	if !found {
		return fmt.Errorf("no Chrome/Edge browser found")
	}
	slog.Info("browser found", "platform", "xiaohongshu", "path", path)
	return nil
}

// Validate checks whether the current login session is still valid.
func (p *XHSPublisher) Validate(ctx context.Context) error {
	cookie, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return fmt.Errorf("get cookie: %w", err)
	}

	browser, page, err := p.launchAndNavigate(ctx, "https://creator.xiaohongshu.com/", cookie)
	if err != nil {
		return err
	}
	defer browser.Close()

	time.Sleep(3 * time.Second)

	url := page.MustInfo().URL
	if strings.Contains(url, "login") {
		p.screenshot(page, "login_failed")
		// Invalidate cached cookie so next attempt re-logins
		p.cookieMgr.Invalidate()
		return fmt.Errorf("xiaohongshu login expired, current page: %s", url)
	}

	slog.Info("login session valid", "platform", "xiaohongshu")
	return nil
}

// Publish publishes content to Xiaohongshu via browser automation.
func (p *XHSPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	cookie, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return p.failResult("get cookie", err, start), err
	}

	browser, page, err := p.launchAndNavigate(ctx, "https://creator.xiaohongshu.com/publish/publish", cookie)
	if err != nil {
		return p.failResult("launch browser", err, start), err
	}
	defer browser.Close()

	title := content.XHSTitle
	if title == "" {
		title = content.Topic
	}
	body := content.XHSBody

	slog.Info("starting publish", "platform", "xiaohongshu", "title", title)

	// 1. Wait for editor to load
	if err := p.waitForEditor(page); err != nil {
		p.screenshot(page, "editor_load_failed")
		return p.failResult("editor page load", err, start), err
	}

	// 2. Fill title
	if err := p.fillTitle(page, title); err != nil {
		p.screenshot(page, "fill_title_failed")
		return p.failResult("fill title", err, start), err
	}
	slog.Debug("title filled", "platform", "xiaohongshu")

	// 3. Fill body
	if err := p.fillBody(page, body); err != nil {
		p.screenshot(page, "fill_body_failed")
		return p.failResult("fill body", err, start), err
	}
	slog.Debug("body filled", "platform", "xiaohongshu")

	// 4. Upload images (if any)
	if content.XHSImages != "" {
		imagePaths := strings.Split(content.XHSImages, ",")
		if err := p.uploadImages(page, imagePaths); err != nil {
			slog.Warn("image upload failed, continuing", "platform", "xiaohongshu", "error", err)
		}
	}

	// 5. Add hashtag topics
	tags := extractTags(body)
	if len(tags) > 0 {
		if err := p.addTags(page, tags); err != nil {
			slog.Warn("add tags failed, continuing", "platform", "xiaohongshu", "error", err)
		}
	}

	// 6. Screenshot before publish
	p.screenshot(page, fmt.Sprintf("before_publish_%d", content.ID))

	// 7. Click publish button
	if err := p.clickPublish(page); err != nil {
		p.screenshot(page, "publish_click_failed")
		return p.failResult("click publish", err, start), err
	}

	slog.Info("publish button clicked", "platform", "xiaohongshu")

	// 8. Wait for publish result
	time.Sleep(5 * time.Second)
	p.screenshot(page, fmt.Sprintf("after_publish_%d", content.ID))

	slog.Info("publish completed", "platform", "xiaohongshu", "content_id", content.ID,
		"duration", time.Since(start))

	return &PublishResult{
		Success:     true,
		Platform:    "xiaohongshu",
		ContentID:   fmt.Sprintf("xhs_%d", content.ID),
		URL:         "https://www.xiaohongshu.com/explore",
		Message:     "publish success (verify on app)",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// performLogin automates login to Xiaohongshu using account/password.
// Returns the cookie string on success.
func (p *XHSPublisher) performLogin(ctx context.Context) (string, error) {
	slog.Info("starting xiaohongshu auto-login")

	l := launcher.New()
	if p.headless {
		l = l.Headless(true)
	}
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")

	url, err := l.Launch()
	if err != nil {
		return "", fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url).MustConnect()
	defer func() {
		// Don't close browser on success — cookies need to stay alive
		// The browser will be closed by the caller or on next operation
	}()

	page := browser.MustPage("https://creator.xiaohongshu.com/login")
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)

	// Switch to password login (Xiaohongshu defaults to QR code scanning)
	p.clickPasswordLoginOption(page)

	// Fill credentials
	if err := p.fillLoginForm(page); err != nil {
		browser.Close()
		return "", fmt.Errorf("fill login form: %w", err)
	}

	// Click login button
	if err := p.clickLoginButton(page); err != nil {
		browser.Close()
		return "", fmt.Errorf("click login button: %w", err)
	}

	// Wait for login to complete
	time.Sleep(5 * time.Second)

	// Check if login succeeded (no longer on login page)
	url = page.MustInfo().URL
	if strings.Contains(url, "login") {
		// Might need CAPTCHA — save screenshot for manual inspection
		p.screenshot(page, "login_captcha")
		browser.Close()
		return "", fmt.Errorf("login may require CAPTCHA, check screenshot. URL: %s", url)
	}

	// Extract cookies
	cookies, err := browser.GetCookies()
	if err != nil {
		browser.Close()
		return "", fmt.Errorf("get cookies after login: %w", err)
	}

	var cookiePairs []string
	for _, c := range cookies {
		if strings.Contains(c.Domain, "xiaohongshu.com") || strings.Contains(c.Domain, "xhscdn.com") {
			cookiePairs = append(cookiePairs, fmt.Sprintf("%s=%s", c.Name, c.Value))
		}
	}

	cookieStr := strings.Join(cookiePairs, "; ")
	if cookieStr == "" {
		browser.Close()
		return "", fmt.Errorf("no cookies extracted after login")
	}

	slog.Info("xiaohongshu auto-login successful",
		"cookie_count", len(cookiePairs))
	browser.Close()
	return cookieStr, nil
}

// clickPasswordLoginOption finds and clicks the "password login" tab/link.
func (p *XHSPublisher) clickPasswordLoginOption(page *rod.Page) {
	selectors := []string{
		"text=密码登录",
		"text=账号密码登录",
		"[class*='password']",
		".login-type-switch",
	}
	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			el.Click(proto.InputMouseButtonLeft, 1)
			time.Sleep(1 * time.Second)
			return
		}
	}
	slog.Debug("password login tab not found, may already be on password form")
}

// fillLoginForm fills the username and password fields.
func (p *XHSPublisher) fillLoginForm(page *rod.Page) error {
	// Fill username/phone
	usernameSelectors := []string{
		"input[type='text']",
		"input[name='phone']",
		"input[name='username']",
		"input[placeholder*='手机']",
		"input[placeholder*='账号']",
	}
	filled := false
	for _, sel := range usernameSelectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			el.MustClick()
			el.MustInput(p.username)
			filled = true
			break
		}
	}
	if !filled {
		return fmt.Errorf("could not find username input field")
	}

	// Fill password
	passwordSelectors := []string{
		"input[type='password']",
		"input[name='password']",
		"input[placeholder*='密码']",
	}
	filled = false
	for _, sel := range passwordSelectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			el.MustClick()
			el.MustInput(p.password)
			filled = true
			break
		}
	}
	if !filled {
		return fmt.Errorf("could not find password input field")
	}

	return nil
}

// clickLoginButton clicks the login submit button.
func (p *XHSPublisher) clickLoginButton(page *rod.Page) error {
	selectors := []string{
		"button:has-text('登录')",
		"button:has-text('登 录')",
		"[class*='login-btn']",
		"button[type='submit']",
	}
	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			return el.Click(proto.InputMouseButtonLeft, 1)
		}
	}
	return fmt.Errorf("could not find login button")
}

// launchAndNavigate starts the browser with cookie injection and navigates to targetURL.
func (p *XHSPublisher) launchAndNavigate(ctx context.Context, targetURL, cookieStr string) (*rod.Browser, *rod.Page, error) {
	l := launcher.New()
	if p.headless {
		l = l.Headless(true)
	}
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")

	url, err := l.Launch()
	if err != nil {
		return nil, nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url).MustConnect()

	// Inject cookies before navigation
	if cookieStr != "" {
		page := browser.MustPage("")
		cookies := parseCookies(cookieStr, ".xiaohongshu.com")
		if len(cookies) > 0 {
			if err := page.SetCookies(cookies); err != nil {
				slog.Warn("set cookies failed", "error", err)
			}
		}
	}

	page := browser.MustPage(targetURL)
	page.MustWaitLoad()

	return browser, page, nil
}

// waitForEditor waits for the publish editor page to fully load.
func (p *XHSPublisher) waitForEditor(page *rod.Page) error {
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
	return fmt.Errorf("editor not found — page may have been redesigned")
}

// fillTitle fills the title input field.
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
	return fmt.Errorf("could not fill title")
}

// fillBody fills the body/editor field.
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
	return fmt.Errorf("could not fill body")
}

// uploadImages uploads image files to the publish form.
func (p *XHSPublisher) uploadImages(page *rod.Page, imagePaths []string) error {
	fileInputs, err := page.Elements("input[type='file']")
	if err != nil || len(fileInputs) == 0 {
		return fmt.Errorf("no image upload input found")
	}

	for _, path := range imagePaths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			absPath, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			path = absPath
		}
		fileInputs[0].MustSetFiles(path)
		time.Sleep(2 * time.Second) // wait for upload
	}

	return nil
}

// addTags appends hashtag topics to the editor body.
func (p *XHSPublisher) addTags(page *rod.Page, tags []string) error {
	tagStr := "\n\n" + strings.Join(tags, " ")

	bodyEl, err := page.Timeout(3 * time.Second).Element("[contenteditable='true']")
	if err != nil {
		return err
	}
	return bodyEl.Input(tagStr)
}

// clickPublish clicks the publish/submit button.
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
	return fmt.Errorf("publish button not found")
}

// screenshot captures a full-page screenshot for debugging.
func (p *XHSPublisher) screenshot(page *rod.Page, name string) {
	if err := os.MkdirAll(p.screenshotDir, 0755); err != nil {
		slog.Warn("create screenshot dir failed", "error", err)
		return
	}
	path := filepath.Join(p.screenshotDir, fmt.Sprintf("%s_%s.png",
		time.Now().Format("20060102_150405"), name))
	data, err := page.Screenshot(true, &proto.PageCaptureScreenshot{})
	if err != nil {
		slog.Warn("screenshot failed", "error", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		slog.Warn("save screenshot failed", "path", path, "error", err)
		return
	}
	slog.Debug("screenshot saved", "path", path)
}

// failResult creates a failed PublishResult.
func (p *XHSPublisher) failResult(stage string, err error, start time.Time) *PublishResult {
	return &PublishResult{
		Success:     false,
		Platform:    "xiaohongshu",
		Message:     fmt.Sprintf("%s: %v", stage, err),
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}
}

// parseCookies parses a semicolon-separated cookie string into Rod format.
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

// extractTags extracts hashtag-style tags from body text.
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

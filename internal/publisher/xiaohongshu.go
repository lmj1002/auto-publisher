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
// Supports hybrid login: password → SMS → manual QR fallback.
// Cookies are cached to disk for 7-day reuse with persistent Chrome profile.
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

// newBrowser creates a new browser instance with persistent profile and anti-detection flags.
// Both performLogin and launchAndNavigate should use this to avoid code duplication.
func (p *XHSPublisher) newBrowser() (*rod.Browser, error) {
	l := launcher.New()
	if p.headless {
		l = l.Headless(true)
	}

	// Persistent Chrome profile — Xiaohongshu treats us as a "known device",
	// which reduces CAPTCHA frequency and the chance of being forced to re-login.
	userDataDir := filepath.Join(p.screenshotDir, "..", "chrome-profile", "xiaohongshu")
	l = l.UserDataDir(userDataDir)

	// Comprehensive anti-detection flags
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")
	l = l.Set("disable-infobars")
	l = l.Set("window-size", "1920,1080")
	l = l.Set("window-position", "0,0")
	l = l.Set("disable-features", "TranslateUI,OptimizationHints,MediaRouter,DialMediaRouteProvider,CalculateNativeWinOcclusion")
	l = l.Set("exclude-switches", "enable-automation")
	l = l.Set("disable-component-update")
	l = l.Set("disable-domain-reliability")
	l = l.Set("disable-sync")
	l = l.Set("disable-default-apps")
	l = l.Set("disable-background-networking")
	l = l.Set("disable-client-side-phishing-detection")
	l = l.Set("disable-crash-reporter")
	l = l.Set("disable-dev-shm-usage")
	l = l.Set("no-first-run")
	l = l.Set("no-default-browser-check")
	l = l.Set("disable-notifications")
	l = l.Set("disable-popup-blocking")
	l = l.Set("lang", "zh-CN")

	// Spoof accept-language via command-line (helps with localization consistency)
	l = l.Set("accept-lang", "zh-CN,zh;q=0.9,en;q=0.8")

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	slog.Debug("browser launched", "platform", "xiaohongshu", "headless", p.headless)
	return browser, nil
}

// applyStealth injects the comprehensive stealth.min.js anti-detection library.
// This hides browser automation fingerprints (webdriver, plugins, WebGL, etc.)
// to reduce the chance of Xiaohongshu detecting our automated browser.
func (p *XHSPublisher) applyStealth(page *rod.Page) {
	applyStealthFull(page)
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

	// Wait for page to settle, then check if redirected to login
	if err := page.WaitStable(2 * time.Second); err != nil {
		slog.Warn("wait stable timeout in validate", "error", err)
	}

	info, err := page.Info()
	if err != nil {
		p.screenshot(page, "validate_page_error")
		return fmt.Errorf("xiaohongshu validate page error: %w", err)
	}
	if strings.Contains(info.URL, "login") {
		p.screenshot(page, "login_required")
		// Invalidate cached cookie so next attempt re-logins
		p.cookieMgr.Invalidate()
		return fmt.Errorf("xiaohongshu login expired, current page: %s", info.URL)
	}

	slog.Info("login session valid", "platform", "xiaohongshu")
	return nil
}

// Publish publishes content to Xiaohongshu via browser automation.
func (p *XHSPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	if err := validateXHSContent(content); err != nil {
		return p.failResult("validate content", err, start), err
	}

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

	// 8. Verify publish result
	success, errMsg := p.waitForPublishResult(page, 30*time.Second)
	p.screenshot(page, fmt.Sprintf("after_publish_%d", content.ID))

	if success {
		slog.Info("publish verified success", "platform", "xiaohongshu", "content_id", content.ID,
			"duration", time.Since(start))
		return &PublishResult{
			Success:     true,
			Platform:    "xiaohongshu",
			ContentID:   fmt.Sprintf("xhs_%d", content.ID),
			URL:         "https://www.xiaohongshu.com/explore",
			Message:     "publish success",
			PublishedAt: time.Now(),
			Duration:    time.Since(start),
		}, nil
	}

	slog.Error("publish verification failed", "platform", "xiaohongshu", "error", errMsg)
	return &PublishResult{
		Success:  false,
		Platform: "xiaohongshu",
		Message:  errMsg,
		Duration: time.Since(start),
	}, fmt.Errorf("xiaohongshu publish failed: %s", errMsg)
}

// ============================================================================
// Login Flow: auto password/SMS login with cookie-primary fallback
// ============================================================================

// performLogin attempts to log into Xiaohongshu automatically.
// Cookie-primary approach: if a manual cookie or disk-cached cookie exists,
// CookieManager returns it before calling this function. So this function only
// runs when no valid cookie is available — meaning it must perform a fresh login.
func (p *XHSPublisher) performLogin(ctx context.Context) (string, error) {
	// No credentials? Skip directly to manual mode
	if p.username == "" || p.password == "" {
		return p.runManualLoginFallback(ctx)
	}

	slog.Info("xiaohongshu: starting auto-login", "username", p.username)

	browser, err := p.newBrowser()
	if err != nil {
		return "", err
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		browser.Close()
		return "", fmt.Errorf("create page: %w", err)
	}
	p.applyStealth(page)

	// Navigate to XHS creator login
	if err := page.Navigate("https://creator.xiaohongshu.com/login"); err != nil {
		browser.Close()
		return "", fmt.Errorf("navigate to login: %w", err)
	}
	page.MustWaitLoad()

	// XHS login page is a heavy React SPA — wait for JS to fully render
	slog.Debug("waiting for XHS login page to render...")
	time.Sleep(5 * time.Second)
	p.screenshot(page, "login_initial")

	// Try auto password login
	if cookie, err := p.doPasswordLogin(page); err == nil {
		browser.Close()
		return cookie, nil
	} else {
		slog.Warn("password login failed", "error", err)
		p.screenshot(page, "password_login_failed")
	}

	// Auto-login failed — give the user a chance to do manual QR/SMS login
	// Re-navigate to fresh login page for clean state
	page.MustNavigate("https://creator.xiaohongshu.com/login")
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)

	return p.doManualLogin(browser, page)
}

// doPasswordLogin attempts phone + password login on the already-loaded login page.
func (p *XHSPublisher) doPasswordLogin(page *rod.Page) (string, error) {
	// Step 1: Switch from default QR view to phone/password login tab
	slog.Debug("switching to phone/password login tab...")
	if !p.clickTabByText(page, []string{"手机号登录", "密码登录", "账号密码登录", "验证码登录"}) {
		// Tab might not exist — maybe we're already on the right form?
		// Check if password field is already visible
		_, err := page.Timeout(3 * time.Second).Element("input[type='password']")
		if err != nil {
			return "", fmt.Errorf("cannot find login form: password login tab not found and no password field visible")
		}
		slog.Debug("password field found without tab switch — form already visible")
	}
	time.Sleep(2 * time.Second) // wait for tab transition animation
	p.screenshot(page, "after_tab_switch")

	// Step 2: Find and fill the phone/account input
	phoneEl, err := p.waitForVisibleInput(page, []string{
		"input[placeholder*='手机号']",
		"input[placeholder*='手机']",
		"input[placeholder*='请输入手机号']",
		"input[type='tel']",
		"input[name='phone']",
	}, 8*time.Second)
	if err != nil {
		return "", fmt.Errorf("phone field not found: %w", err)
	}
	phoneEl.MustClick()
	phoneEl.MustInput(p.username)
	time.Sleep(500 * time.Millisecond)
	slog.Debug("phone filled")

	// Step 3: Find and fill the password input
	passEl, err := p.waitForVisibleInput(page, []string{
		"input[type='password']",
		"input[placeholder*='密码']",
		"input[name='password']",
	}, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("password field not found or not visible: %w", err)
	}
	passEl.MustClick()
	passEl.MustInput(p.password)
	time.Sleep(500 * time.Millisecond)
	slog.Debug("password filled")
	p.screenshot(page, "form_filled")

	// Step 4: Check for agreement checkbox
	p.tryClickAgreement(page)

	// Step 5: Click login button
	loginBtn, err := p.findClickableButton(page, []string{
		"button:has-text('登录')",
		"button:has-text('登 录')",
		"[class*='login-btn'] button",
		"button[type='submit']",
		"form button",
	}, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("login button not found: %w", err)
	}
	loginBtn.MustClick()
	slog.Debug("login button clicked")

	// Step 6: Wait for login result
	return p.waitForLoginSuccess(page, 20*time.Second)
}

// clickTabByText tries to click a tab/link by its visible text.
// Returns true if a matching clickable element was found and clicked.
func (p *XHSPublisher) clickTabByText(page *rod.Page, texts []string) bool {
	for _, text := range texts {
		// Try multiple DOM patterns for each text
		patterns := []string{
			fmt.Sprintf("text=%s", text),
			fmt.Sprintf("span:has-text('%s')", text),
			fmt.Sprintf("div:has-text('%s')", text),
			fmt.Sprintf("a:has-text('%s')", text),
			fmt.Sprintf("[class*='tab']:has-text('%s')", text),
			fmt.Sprintf("li:has-text('%s')", text),
		}
		for _, pat := range patterns {
			el, err := page.Timeout(2 * time.Second).Element(pat)
			if err == nil && el != nil {
				if clickErr := el.Click(proto.InputMouseButtonLeft, 1); clickErr == nil {
					slog.Debug("clicked tab", "text", text, "pattern", pat)
					return true
				}
			}
		}
	}
	return false
}

// waitForVisibleInput waits for an input element matching any of the given selectors
// to become visible on the page. Returns the first visible match.
func (p *XHSPublisher) waitForVisibleInput(page *rod.Page, selectors []string, timeout time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, sel := range selectors {
			el, err := page.Timeout(1 * time.Second).Element(sel)
			if err != nil {
				continue
			}
			visible, err := el.Visible()
			if err == nil && visible {
				return el, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("no visible input found for selectors: %v (timeout: %v)", selectors, timeout)
}

// findClickableButton finds a visible, non-disabled button matching the selectors.
func (p *XHSPublisher) findClickableButton(page *rod.Page, selectors []string, timeout time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, sel := range selectors {
			el, err := page.Timeout(1 * time.Second).Element(sel)
			if err != nil {
				continue
			}
			visible, _ := el.Visible()
			if !visible {
				continue
			}
			disabled, _ := el.Attribute("disabled")
			if disabled != nil && *disabled != "" && *disabled != "false" {
				continue
			}
			return el, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("no clickable button found for selectors: %v", selectors)
}

// tryClickAgreement tries to click a user agreement checkbox.
func (p *XHSPublisher) tryClickAgreement(page *rod.Page) {
	selectors := []string{
		"input[type='checkbox']",
		"[class*='agree'] input",
		"[class*='checkbox']",
	}
	for _, sel := range selectors {
		el, err := page.Timeout(1 * time.Second).Element(sel)
		if err != nil {
			continue
		}
		if checked, _ := el.Attribute("checked"); checked == nil {
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			time.Sleep(300 * time.Millisecond)
			slog.Debug("agreement checked")
			return
		}
	}
}

// waitForLoginSuccess polls the page after login submit for success indicators.
func (p *XHSPublisher) waitForLoginSuccess(page *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)

		info, err := page.Info()
		if err != nil {
			continue
		}

		// Success: redirected away from login
		if !strings.Contains(info.URL, "login") {
			return p.extractCookies(page)
		}

		// Check for error toasts
		errMsg := p.findErrorToast(page)
		if errMsg != "" {
			return "", fmt.Errorf("login rejected: %s", errMsg)
		}

		// Check for CAPTCHA
		if has, _ := p.hasCaptcha(page); has {
			p.screenshot(page, "captcha_detected")
			return "", fmt.Errorf("CAPTCHA required — cannot auto-login")
		}
	}

	// Try extracting cookies anyway — sometimes login succeeds but URL didn't change
	if cookie, err := p.extractCookies(page); err == nil && cookie != "" {
		return cookie, nil
	}

	return "", fmt.Errorf("login timed out — no success or error detected within %v", timeout)
}

// findErrorToast looks for visible error messages on the page.
func (p *XHSPublisher) findErrorToast(page *rod.Page) string {
	for _, sel := range []string{"[class*='error']", "[class*='toast']", "[class*='message']", ".el-message"} {
		el, err := page.Timeout(300 * time.Millisecond).Element(sel)
		if err == nil {
			if text, err := el.Text(); err == nil {
				text = strings.TrimSpace(text)
				for _, kw := range []string{"错误", "失败", "不正确", "无效", "不存在", "频繁", "冻结"} {
					if strings.Contains(text, kw) {
						return text
					}
				}
			}
		}
	}
	return ""
}

// hasCaptcha checks whether a CAPTCHA element is present on the page.
func (p *XHSPublisher) hasCaptcha(page *rod.Page) (bool, error) {
	for _, sel := range []string{
		"[class*='captcha']", "[class*='verify']", "[id*='captcha']",
		"[id*='nc_']", ".geetest", ".yidun", "#nocaptcha", "[class*='scaptcha']",
		"iframe[src*='captcha']",
	} {
		el, err := page.Timeout(500 * time.Millisecond).Element(sel)
		if err == nil && el != nil {
			if visible, _ := el.Visible(); visible {
				return true, nil
			}
		}
	}
	return false, nil
}

// extractCookies extracts xiaohongshu-related cookies from the browser.
func (p *XHSPublisher) extractCookies(page *rod.Page) (string, error) {
	cookies, err := page.Browser().GetCookies()
	if err != nil {
		return "", fmt.Errorf("get cookies: %w", err)
	}
	var pairs []string
	for _, c := range cookies {
		if strings.Contains(c.Domain, "xiaohongshu.com") || strings.Contains(c.Domain, "xhscdn.com") {
			pairs = append(pairs, fmt.Sprintf("%s=%s", c.Name, c.Value))
		}
	}
	cookieStr := strings.Join(pairs, "; ")
	if cookieStr == "" {
		return "", fmt.Errorf("no XHS cookies found")
	}
	slog.Info("cookies extracted", "count", len(pairs))
	p.screenshot(page, "login_success")
	return cookieStr, nil
}

// runManualLoginFallback creates a fresh browser and runs manual login.
// Used when no credentials are configured.
func (p *XHSPublisher) runManualLoginFallback(ctx context.Context) (string, error) {
	browser, err := p.newBrowser()
	if err != nil {
		return "", err
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		browser.Close()
		return "", fmt.Errorf("create page: %w", err)
	}
	p.applyStealth(page)
	if err := page.Navigate("https://creator.xiaohongshu.com/login"); err != nil {
		browser.Close()
		return "", fmt.Errorf("navigate to login: %w", err)
	}
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)
	return p.doManualLogin(browser, page)
}

// doManualLogin waits for the user to complete login manually (QR scan or SMS).
func (p *XHSPublisher) doManualLogin(browser *rod.Browser, page *rod.Page) (string, error) {
	p.screenshot(page, "manual_login_qr")

	slog.Warn("================================================================")
	slog.Warn("🔴 XIAOHONGSHU MANUAL LOGIN REQUIRED")
	slog.Warn("   Auto-login failed — completing login manually in the opened browser.")
	slog.Warn("   After login succeeds, the system will save your cookies for 7 days.")
	slog.Warn("   Options: 1) Scan QR with XHS App  2) Use SMS verification")
	slog.Warn("================================================================")

	cookie, err := p.waitForManualLogin(page, 5*time.Minute)
	browser.Close()
	if err != nil {
		return "", fmt.Errorf("manual login failed: %w", err)
	}
	slog.Info("manual login successful, cookies cached for 7 days")
	return cookie, nil
}

// waitForManualLogin polls until the user completes login manually.
func (p *XHSPublisher) waitForManualLogin(page *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	lastScreenshot := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if time.Since(lastScreenshot) > 30*time.Second {
			p.screenshot(page, "manual_login_waiting")
			lastScreenshot = time.Now()
		}
		info, err := page.Info()
		if err != nil {
			continue
		}
		if !strings.Contains(info.URL, "login") {
			cookies, err := page.Browser().GetCookies()
			if err != nil {
				return "", fmt.Errorf("get cookies: %w", err)
			}
			var pairs []string
			for _, c := range cookies {
				if strings.Contains(c.Domain, "xiaohongshu.com") || strings.Contains(c.Domain, "xhscdn.com") {
					pairs = append(pairs, fmt.Sprintf("%s=%s", c.Name, c.Value))
				}
			}
			cookieStr := strings.Join(pairs, "; ")
			if cookieStr == "" {
				return "", fmt.Errorf("no cookies after manual login")
			}
			p.screenshot(page, "manual_login_success")
			return cookieStr, nil
		}
	}
	p.screenshot(page, "manual_login_timeout")
	return "", fmt.Errorf("manual login timed out after %v", timeout)
}

// ============================================================================
// Browser Launch & Navigation
// ============================================================================

// launchAndNavigate starts the browser with cookie injection, stealth, and navigates to targetURL.
func (p *XHSPublisher) launchAndNavigate(ctx context.Context, targetURL, cookieStr string) (*rod.Browser, *rod.Page, error) {
	browser, err := p.newBrowser()
	if err != nil {
		return nil, nil, err
	}

	// Create a blank page, inject stealth, then set cookies BEFORE navigation
	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		browser.Close()
		return nil, nil, fmt.Errorf("create page: %w", err)
	}
	p.applyStealth(page)

	// Inject cookies on the blank page (avoids creating an orphan page)
	if cookieStr != "" {
		cookies := parseCookies(cookieStr, ".xiaohongshu.com")
		if len(cookies) > 0 {
			if err := page.SetCookies(cookies); err != nil {
				slog.Warn("set cookies failed", "error", err)
			} else {
				slog.Debug("cookies injected", "count", len(cookies))
			}
		}
	}

	// Now navigate to target (cookies are already set on this page's domain)
	if err := page.Navigate(targetURL); err != nil {
		browser.Close()
		return nil, nil, fmt.Errorf("navigate to %s: %w", targetURL, err)
	}
	if err := page.WaitLoad(); err != nil {
		browser.Close()
		return nil, nil, fmt.Errorf("wait page load: %w", err)
	}

	// Check if we got redirected to login (cookie expired)
	info, err := page.Info()
	if err == nil {
		time.Sleep(1 * time.Second)
		info, _ = page.Info() // re-read after settle
		if info != nil && strings.Contains(info.URL, "login") {
			p.cookieMgr.Invalidate()
			browser.Close()
			return nil, nil, fmt.Errorf("login required after navigation (cookie expired), current page: %s", info.URL)
		}
	}

	return browser, page, nil
}

// ============================================================================
// Editor Interaction
// ============================================================================

// waitForEditor waits for the publish editor page to fully load.
func (p *XHSPublisher) waitForEditor(page *rod.Page) error {
	// Give the page time for React/Vue to render the editor
	time.Sleep(2 * time.Second)

	selectors := []string{
		// XHS creator studio specific selectors
		"input[placeholder*='填写标题']",
		"input[placeholder*='标题']",
		"#title",
		// Content area
		"[contenteditable='true']",
		".ql-editor",
		"[data-placeholder*='正文']",
		"[data-placeholder*='描述']",
		"p[data-placeholder*='输入正文描述']",
		// Generic editor container
		"#creator-editor-container",
		"textarea",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(8 * time.Second).Element(sel)
		if err == nil && el != nil {
			slog.Debug("editor found", "selector", sel)
			return nil
		}
	}

	// Last resort: take a screenshot and check if we're on the right page
	p.screenshot(page, "editor_not_found")
	return fmt.Errorf("editor not found — page may have been redesigned or requires login")
}

// fillTitle fills the title input field.
func (p *XHSPublisher) fillTitle(page *rod.Page, title string) error {
	selectors := []string{
		"input[placeholder*='填写标题']",
		"input[placeholder*='标题']",
		"input.r-title-input",
		"#title",
		"textarea[placeholder*='标题']",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err == nil {
			if err := setElementText(el, title); err != nil {
				// Fallback to simpler approach
				if clickErr := el.Click(proto.InputMouseButtonLeft, 1); clickErr == nil {
					if inputErr := el.Input(title); inputErr == nil {
						slog.Debug("title set via fallback", "selector", sel)
						return nil
					}
				}
				slog.Debug("set title failed, trying next", "selector", sel, "error", err)
				continue
			}
			slog.Debug("title set", "selector", sel)
			return nil
		}
	}
	return fmt.Errorf("could not fill title — no matching input found")
}

// fillBody fills the body/editor field.
func (p *XHSPublisher) fillBody(page *rod.Page, body string) error {
	selectors := []string{
		// XHS creator uses a contenteditable div for the body
		"p[data-placeholder*='输入正文描述']",
		"[data-placeholder*='正文']",
		"[data-placeholder*='描述']",
		"#creator-editor-container [contenteditable='true']",
		"[contenteditable='true']",
		".ql-editor",
		".rich-text-editor",
		"textarea",
		"#content",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err == nil {
			if err := setElementText(el, body); err != nil {
				slog.Debug("set body failed, trying next", "selector", sel, "error", err)
				continue
			}
			slog.Debug("body set", "selector", sel)
			return nil
		}
	}
	return fmt.Errorf("could not fill body — no matching editor found")
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
		if err := fileInputs[0].SetFiles([]string{path}); err != nil {
			return fmt.Errorf("set image file %q: %w", path, err)
		}
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
	return appendElementText(bodyEl, tagStr)
}

// clickPublish clicks the publish/submit button.
func (p *XHSPublisher) clickPublish(page *rod.Page) error {
	// First try text-based button detection (most reliable)
	if el, err := findElementByText(page, "button,.publish-btn,.submit-btn,[class*='publish'],[class*='submit']", []string{"发布", "提交", "发表"}); err == nil {
		slog.Debug("publish button found by text")
		return el.Click(proto.InputMouseButtonLeft, 1)
	}

	// Then try CSS selectors
	selectors := []string{
		".publish-btn",
		"[class*='publish']",
		".submit-btn",
		"button[type='submit']",
		"button:has-text('发布')",
		"button:has-text('发表')",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			continue
		}
		if disabled, _ := el.Attribute("disabled"); disabled != nil {
			slog.Debug("publish button is disabled, trying next", "selector", sel)
			continue
		}
		slog.Debug("publish button found by selector", "selector", sel)
		return el.Click(proto.InputMouseButtonLeft, 1)
	}

	return fmt.Errorf("publish button not found")
}

// waitForPublishResult waits for the publish result indicator on the page.
// Returns (success, errorMessage).
func (p *XHSPublisher) waitForPublishResult(page *rod.Page, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		// Check for redirect to published list (most reliable signal)
		info, err := page.Info()
		if err == nil {
			if strings.Contains(info.URL, "published") || strings.Contains(info.URL, "publish/success") ||
				strings.Contains(info.URL, "note") || strings.Contains(info.URL, "explore") {
				slog.Info("publish success: redirected away from editor", "url", info.URL)
				return true, ""
			}
		}

		// Check success indicators
		if el, err := findElementByText(page, "body *", []string{
			"发布成功", "已发布", "发布完成", "发表成功", "笔记已发布",
		}); err == nil && el != nil {
			text, _ := el.Text()
			slog.Info("publish success indicator found", "text", text)
			return true, ""
		}

		// Check success CSS classes
		for _, sel := range []string{"[class*='success']", ".publish-success", "[class*='toast-success']"} {
			el, err := page.Timeout(200 * time.Millisecond).Element(sel)
			if err == nil && el != nil {
				text, _ := el.Text()
				if text != "" {
					slog.Info("publish success element found", "text", text)
					return true, ""
				}
			}
		}

		// Check error indicators
		if el, err := findElementByText(page, "body *", []string{
			"失败", "错误", "异常", "违规", "审核不通过",
		}); err == nil && el != nil {
			text, _ := el.Text()
			if text != "" {
				slog.Warn("publish error indicator found", "text", text)
				return false, text
			}
		}

		for _, sel := range []string{"[class*='error']", "[class*='toast-error']", ".el-message--error"} {
			el, err := page.Timeout(200 * time.Millisecond).Element(sel)
			if err == nil && el != nil {
				text, _ := el.Text()
				if text != "" {
					slog.Warn("publish error element found", "text", text)
					return false, text
				}
			}
		}
	}

	slog.Warn("publish result verification timed out", "timeout", timeout)
	return false, "publish result unknown (timeout, check screenshot)"
}

// ============================================================================
// Helpers
// ============================================================================

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

func validateXHSContent(content *model.Content) error {
	if content == nil {
		return fmt.Errorf("content is nil")
	}
	title := strings.TrimSpace(content.XHSTitle)
	if title == "" {
		title = strings.TrimSpace(content.Topic)
	}
	if title == "" {
		return fmt.Errorf("missing title")
	}
	if strings.TrimSpace(content.XHSBody) == "" {
		return fmt.Errorf("missing body")
	}
	return nil
}

func findElementByText(page *rod.Page, selector string, needles []string) (*rod.Element, error) {
	elements, err := page.Timeout(2 * time.Second).Elements(selector)
	if err != nil {
		return nil, err
	}
	for _, el := range elements {
		text, err := el.Text()
		if err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				return el, nil
			}
		}
	}
	return nil, fmt.Errorf("no element matching text %q", strings.Join(needles, "/"))
}

func setElementText(el *rod.Element, text string) error {
	_, err := el.Eval(`text => {
		if (this.isContentEditable) {
			this.focus();
			this.innerText = text;
		} else {
			this.focus();
			this.value = text;
		}
		this.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text }));
		this.dispatchEvent(new Event('change', { bubbles: true }));
		this.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true }));
		return true;
	}`, text)
	return err
}

func appendElementText(el *rod.Element, text string) error {
	_, err := el.Eval(`text => {
		if (this.isContentEditable) {
			this.focus();
			this.innerText = (this.innerText || '') + text;
		} else {
			this.focus();
			this.value = (this.value || '') + text;
		}
		this.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text }));
		this.dispatchEvent(new Event('change', { bubbles: true }));
		this.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true }));
		return true;
	}`, text)
	return err
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

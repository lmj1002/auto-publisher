package publisher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"auto-publisher/internal/model"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// zhihuClientID is the OAuth client_id used by Zhihu's web application.
const zhihuClientID = "c3cef7c66a1843f8b3a9e6a1e3160e20"

// ZhihuPublisher publishes content to Zhihu (知乎) via HTTP API with cookie authentication.
// Supports: browser-based login (rod, opens Chrome window) → HTTP API auto-login → manual cookie injection.
type ZhihuPublisher struct {
	client        *http.Client
	cookieMgr     *CookieManager
	userAgent     string
	username      string
	password      string
	screenshotDir string
	headless      bool
}

// ZhihuAPI endpoints used by the publisher.
var zhihuAPIs = map[string]string{
	"check_login":   "https://www.zhihu.com/api/v4/me",
	"create_draft":  "https://zhuanlan.zhihu.com/api/articles",
	"upload_image":  "https://zhuanlan.zhihu.com/api/images",
	"login":         "https://www.zhihu.com/api/v3/oauth/sign_in",
	"login_captcha": "https://www.zhihu.com/api/v3/oauth/captcha?lang=en",
}

// NewZhihuPublisher creates a Zhihu publisher with manual cookie string.
// cookieSource: cookie string or path to .cookie/.txt file.
// cookieDir: directory for cached cookie persistence.
func NewZhihuPublisher(cookieSource string, cookieDir string) *ZhihuPublisher {
	cookieStr := cookieSource
	if strings.HasSuffix(cookieSource, ".txt") || strings.HasSuffix(cookieSource, ".cookie") {
		if data, err := os.ReadFile(cookieSource); err == nil {
			cookieStr = strings.TrimSpace(string(data))
		}
	}

	return &ZhihuPublisher{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		cookieMgr: NewCookieManager("zhihu", cookieDir, WithManualCookie(cookieStr)),
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
}

// NewZhihuPublisherWithLogin creates a Zhihu publisher with account/password auto-login.
// cookieDir: cached cookie storage. screenshotDir: debug screenshots. headless: hide browser on login.
// If auto-login fails, falls back to browser-based manual login (opens Chrome window).
func NewZhihuPublisherWithLogin(cookieDir, screenshotDir, username, password string, headless bool) *ZhihuPublisher {
	p := &ZhihuPublisher{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		username:      username,
		password:      password,
		screenshotDir: screenshotDir,
		headless:      headless,
	}

	loginFunc := func(ctx context.Context) (string, error) {
		return p.performLogin(ctx)
	}

	// Check for manual cookie as fallback (takes priority over auto-login when present)
	manualCookie := os.Getenv("ZHIHU_COOKIE")
	if manualCookie == "" {
		if data, err := os.ReadFile("zhihu.cookie"); err == nil {
			manualCookie = strings.TrimSpace(string(data))
		}
	}

	if manualCookie != "" {
		slog.Info("zhihu: manual cookie found, will use as primary auth source",
			"cookie_len", len(manualCookie))
	}

	p.cookieMgr = NewCookieManager("zhihu", cookieDir,
		WithLoginFunc(loginFunc),
		WithManualCookie(manualCookie),
	)
	return p
}

// Name returns the publisher name.
func (p *ZhihuPublisher) Name() string {
	return "zhihu"
}

// Platform returns the target platform identifier.
func (p *ZhihuPublisher) Platform() Platform {
	return PlatformZhihu
}

// IsAvailable checks whether the publisher is ready to use.
func (p *ZhihuPublisher) IsAvailable(ctx context.Context) bool {
	if !p.cookieMgr.IsAvailable() {
		return false
	}
	return p.Validate(ctx) == nil
}

// Validate checks whether the current login session is still valid.
func (p *ZhihuPublisher) Validate(ctx context.Context) error {
	cookie, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return fmt.Errorf("get cookie: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zhihuAPIs["check_login"], nil)
	if err != nil {
		return fmt.Errorf("create validate request: %w", err)
	}
	p.setHeaders(req, cookie)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("zhihu API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		p.cookieMgr.Invalidate()
		return fmt.Errorf("zhihu login expired (HTTP %d): %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		// Non-auth error — don't invalidate cookie (transient network issue, rate limit, etc.)
		return fmt.Errorf("zhihu API error (HTTP %d): %s", resp.StatusCode, truncateStr(string(body), 200))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("parse login status: %w", err)
	}

	if errMsg, ok := result["error"]; ok {
		p.cookieMgr.Invalidate()
		return fmt.Errorf("zhihu login expired: %v", errMsg)
	}

	slog.Info("login session valid", "platform", "zhihu")
	return nil
}

// Publish publishes content to Zhihu.
// Strategy: Try HTTP API first → if signature/CAPTCHA blocks it → fall back to browser automation.
func (p *ZhihuPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	if err := validateZhihuContent(content); err != nil {
		return p.failResult("validate content", err, start), err
	}

	// Try HTTP API first
	cookie, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return p.failResult("get cookie", err, start), err
	}

	// Try HTTP API publish
	result, apiErr := p.publishViaHTTP(ctx, content, cookie, start)

	// If HTTP API worked or failed with a non-signature error (e.g. content validation), return
	if apiErr == nil || !isZhihuSignatureError(apiErr) {
		return result, apiErr
	}

	// HTTP API blocked by signature → fall back to browser automation
	slog.Warn("zhihu HTTP API blocked by signature verification, falling back to browser", "error", apiErr)
	return p.publishViaBrowser(ctx, content, start)
}

// isZhihuSignatureError returns true when the API error looks like a signature/CSRF block.
func isZhihuSignatureError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Missing argument") ||
		strings.Contains(msg, "10001") ||
		strings.Contains(msg, "请求参数异常") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "403")
}

// publishViaHTTP publishes using the HTTP API (fast, no browser needed).
func (p *ZhihuPublisher) publishViaHTTP(ctx context.Context, content *model.Content, cookie string, start time.Time) (*PublishResult, error) {
	switch content.Type {
	case model.TypeArticle:
		return p.publishArticle(ctx, content, start)
	case model.TypeIdea:
		return p.publishIdea(ctx, content, start)
	default:
		return p.publishArticle(ctx, content, start)
	}
}

// publishViaBrowser publishes using browser automation (bypasses zhihu signature checks).
// Uses headless browser — no window needed, runs invisibly in the background.
func (p *ZhihuPublisher) publishViaBrowser(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	slog.Info("zhihu: publishing via browser automation (headless)")

	if p.screenshotDir == "" {
		p.screenshotDir = filepath.Join("data", "screenshots")
	}

	l := launcher.New()
	l = l.Headless(true) // Always headless for publishing — no window needed
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")
	l = l.Set("exclude-switches", "enable-automation")
	l = l.Set("window-size", "1280,900")
	l = l.Set("lang", "zh-CN")
	l = l.Set("disable-gpu")
	userDataDir := filepath.Join(p.screenshotDir, "..", "chrome-profile", "zhihu-publish")
	l = l.UserDataDir(userDataDir)

	url, err := l.Launch()
	if err != nil {
		return p.failResult("launch browser", err, start), err
	}

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return p.failResult("connect browser", err, start), err
	}
	defer browser.Close()

	cookieStr, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return p.failResult("get cookie", err, start), err
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return p.failResult("create page", err, start), err
	}

	// Set cookies before navigation
	if cookieStr != "" {
		cookies := parseCookies(cookieStr, ".zhihu.com")
		if len(cookies) > 0 {
			if err := page.SetCookies(cookies); err != nil {
				slog.Warn("zhihu set cookies failed", "error", err)
			}
		}
	}

	// Navigate to zhihu homepage — creator.written.com times out, so we use the homepage
	if err := page.Navigate("https://www.zhihu.com"); err != nil {
		return p.failResult("navigate to zhihu", err, start), err
	}
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)
	p.zhihuScreenshot(page, "homepage")

	// Check if we're actually logged in
	info, _ := page.Info()
	if info != nil && (strings.Contains(info.URL, "signin") || strings.Contains(info.URL, "login")) {
		err = fmt.Errorf("redirected to login page: %s", info.URL)
		return p.failResult("not logged in", err, start), err
	}

	// Step 1: Click the "写想法" / "发想法" button to open the pin creation modal
	if !p.clickZhihuWritePin(page) {
		p.zhihuScreenshot(page, "no_write_button")
		return p.failResult("find write pin button", fmt.Errorf("no '写想法' button found on homepage"), start), err
	}
	time.Sleep(2 * time.Second) // Wait for the modal/dialog to open
	p.zhihuScreenshot(page, "pin_modal")

	// Step 2: Fill the editor inside the modal
	title := content.ZHTitle
	if title == "" {
		title = content.Topic
	}
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
	}
	fullText := body
	if title != "" && title != body {
		fullText = title + "\n\n" + body
	}
	if err := p.fillZhihuEditor(page, fullText); err != nil {
		p.zhihuScreenshot(page, "editor_fill_failed")
		return p.failResult("fill editor", err, start), err
	}
	time.Sleep(1 * time.Second)
	p.zhihuScreenshot(page, "content_filled")

	// Step 3: Wait for publish button to become enabled, then click
	if err := p.waitAndClickZhihuPublish(page); err != nil {
		p.zhihuScreenshot(page, "publish_failed")
		return p.failResult("click publish", err, start), err
	}
	time.Sleep(3 * time.Second)
	p.zhihuScreenshot(page, "after_publish")

	info, _ = page.Info()
	slog.Info("zhihu browser publish completed", "url", info.URL)
	return &PublishResult{
		Success:     true,
		Platform:    "zhihu",
		ContentID:   "browser-pin",
		URL:         info.URL,
		Message:     "published via browser",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// clickZhihuWritePin clicks the "发想法" button on the zhihu homepage.
func (p *ZhihuPublisher) clickZhihuWritePin(page *rod.Page) bool {
	// Try CSS selectors
	selectors := []string{
		"button:has-text('发想法')",
		"button:has-text('写想法')",
		"[class*='WriteButton']",
		"[class*='write-pin']",
		"[class*='PublishBar'] button",
	}
	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil && el != nil {
			visible, _ := el.Visible()
			if visible {
				el.MustClick()
				slog.Debug("clicked write pin button", "selector", sel)
				return true
			}
		}
	}

	// Fallback: JS to find and click "发想法" / "写想法"
	result, err := page.Eval(`() => {
		const all = document.querySelectorAll('*');
		for (const el of all) {
			const text = el.textContent.trim();
			if (text === '发想法' || text === '写想法') {
				if (el.offsetParent !== null) {
					el.click();
					return 'clicked:' + text;
				}
			}
		}
		return 'not_found';
	}`)
	if err == nil && fmt.Sprintf("%v", result) != "not_found" {
		slog.Debug("clicked write pin button via JS", "result", result)
		return true
	}

	return false
}

// fillZhihuEditor fills the Zhihu editor with text.
// Looks specifically for contenteditable inside recently-opened modals/dialogs.
func (p *ZhihuPublisher) fillZhihuEditor(page *rod.Page, text string) error {
	// Find the BODY editor (not the title field) and fill it.
	// After clicking "发想法", Zhihu opens a modal with:
	//   1. A title textarea (placeholder="标题")
	//   2. A body contenteditable/textarea (no placeholder)
	// We need to fill the BODY (not title) for the "发布" button to become enabled.
	result, err := page.Eval(`(text) => {
		const editors = document.querySelectorAll('[contenteditable="true"], textarea');
		let bodyEl = null;

		// Find the body editor: visible, NOT the title field
		for (const el of editors) {
			if (el.offsetParent === null) continue;
			const ph = (el.getAttribute('placeholder') || '').toLowerCase();
			if (ph.includes('title') || ph.includes('标题') || ph.includes('输入标题')) {
				continue; // skip title field
			}
			bodyEl = el;
		}

		// If not found by placeholder, use the last visible editor
		if (!bodyEl) {
			for (let i = editors.length - 1; i >= 0; i--) {
				if (editors[i].offsetParent !== null) {
					bodyEl = editors[i];
					break;
				}
			}
		}

		if (!bodyEl) return 'no-editor';

		bodyEl.focus();

		// Draft.js editor needs execCommand to trigger React state update
		if (bodyEl.isContentEditable && bodyEl.classList.contains('public-DraftEditor-content')) {
			// Use execCommand for Draft.js — this triggers the proper React onChange
			document.execCommand('selectAll', false, null);
			document.execCommand('insertText', false, text);
		} else if (bodyEl.isContentEditable) {
			bodyEl.innerText = text;
		} else {
			bodyEl.value = text;
		}

		// Fire comprehensive events
		bodyEl.dispatchEvent(new InputEvent('beforeInput', {bubbles: true, composed: true, inputType: 'insertText', data: text}));
		bodyEl.dispatchEvent(new InputEvent('input', {bubbles: true, composed: true, inputType: 'insertText', data: text}));
		bodyEl.dispatchEvent(new Event('change', {bubbles: true}));

		return 'filled-body:' + (bodyEl.className || bodyEl.tagName);
	}`, text)

	if err != nil {
		return fmt.Errorf("editor JS eval failed: %w", err)
	}

	resultStr := fmt.Sprintf("%v", result)
	if strings.HasPrefix(resultStr, "no-editor") {
		return fmt.Errorf("no body editor found")
	}

	slog.Debug("zhihu editor filled", "result", resultStr)
	return nil
}

// waitAndClickZhihuPublish waits for the publish button to become enabled, then clicks it.
// After filling the editor, Zhihu's React needs a moment to update the button state.
func (p *ZhihuPublisher) waitAndClickZhihuPublish(page *rod.Page) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Use JS to find a non-disabled "发布" button
		result, err := page.Eval(`() => {
			const buttons = document.querySelectorAll('button');
			for (const btn of buttons) {
				if (!btn.textContent.includes('发布')) continue;
				if (btn.disabled || btn.offsetParent === null) continue;
				btn.click();
				return 'clicked';
			}
			return 'disabled';
		}`)
		if err == nil && fmt.Sprintf("%v", result) == "clicked" {
			slog.Debug("zhihu publish button clicked (enabled)")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Try one last time with the old method (may click a disabled button)
	return p.clickZhihuPublish(page)
}

// clickZhihuPublish finds and clicks the publish button on the zhihu page.
func (p *ZhihuPublisher) clickZhihuPublish(page *rod.Page) error {
	// Try CSS selectors first
	selectors := []string{
		"button:has-text('发布')",
		"button:has-text('发表')",
		"button[class*='publish']",
		"button[class*='submit']",
		"[class*='PublishButton'] button",
		"button[class*='Button']:has-text('发布')",
		"button:has-text('确定')",
	}

	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			continue
		}
		visible, _ := el.Visible()
		if !visible {
			continue
		}
		disabled, _ := el.Attribute("disabled")
		if disabled != nil && *disabled != "" {
			continue
		}
		el.MustClick()
		slog.Debug("zhihu publish button clicked", "selector", sel)
		return nil
	}

	// Fallback: use JS to find and click publish button (prefer inside modal, highest z-index)
	result, err := page.Eval(`() => {
		// Find all buttons with "发布" text, prefer those in high-z-index containers
		const buttons = document.querySelectorAll('button');
		let bestBtn = null, bestZ = -1;
		for (const btn of buttons) {
			if (!btn.textContent.includes('发布') && !btn.textContent.includes('发表')) continue;
			if (btn.disabled || btn.offsetParent === null) continue;
			let z = 0, p = btn;
			while (p) {
				try { const s = window.getComputedStyle(p); const zi = parseInt(s.zIndex); if (!isNaN(zi) && zi > z) z = zi; } catch(e) {}
				p = p.parentElement;
			}
			if (z > bestZ) { bestZ = z; bestBtn = btn; }
		}
		if (bestBtn) {
			bestBtn.click();
			return 'clicked:' + bestBtn.textContent.trim() + ' z:' + bestZ;
		}
		return 'not_found';
	}`)
	if err != nil {
		return fmt.Errorf("publish button JS click failed: %w", err)
	}
	if fmt.Sprintf("%v", result) == "not_found" {
		return fmt.Errorf("no publish button found via JS either")
	}
	slog.Debug("zhihu publish button clicked via JS", "result", result)
	return nil
}

// performLogin runs Zhihu login. Strategy:
//  1. HTTP API auto-login (password + RSA encryption)
//  2. If API fails (CAPTCHA, code:10001, etc.) → open browser for manual login
//
// Returns cookie string on success.
func (p *ZhihuPublisher) performLogin(ctx context.Context) (string, error) {
	// Step 1: Try HTTP API auto-login
	cookie, err := p.performHTTPLogin(ctx)
	if err == nil {
		return cookie, nil
	}
	slog.Warn("zhihu HTTP auto-login failed, falling back to browser login", "error", err)

	// Step 2: Fall back to browser-based manual login
	return p.performBrowserLogin(ctx)
}

// performHTTPLogin attempts Zhihu account/password login via HTTP API.
// Flow: GET signin page → extract _xsrf → fetch RSA public key →
// check captcha → encrypt password → POST login → collect cookies.
// Returns the cookie string on success.
func (p *ZhihuPublisher) performHTTPLogin(ctx context.Context) (string, error) {
	slog.Info("starting zhihu HTTP auto-login")

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	// Step 1: GET signin page to obtain cookies, _xsrf token, and JS bundle URL
	signinURL := "https://www.zhihu.com/signin"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, signinURL, nil)
	req.Header.Set("User-Agent", p.userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch signin page: %w", err)
	}
	htmlBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Extract _xsrf from cookies and HTML
	var xsrfToken string
	var initialCookies []string
	for _, c := range resp.Cookies() {
		initialCookies = append(initialCookies, fmt.Sprintf("%s=%s", c.Name, c.Value))
		if c.Name == "_xsrf" {
			xsrfToken = c.Value
		}
	}

	// If _xsrf not in cookies, try extracting from HTML body
	if xsrfToken == "" {
		xsrfToken = extractXSRFFromHTML(string(htmlBody))
	}
	if xsrfToken == "" {
		return "", fmt.Errorf("could not extract _xsrf token from signin page (checked cookies and HTML)")
	}
	slog.Debug("got xsrf token", "platform", "zhihu")

	// Step 2: Check CAPTCHA requirement
	captchaURL := zhihuAPIs["login_captcha"]
	captchaReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, captchaURL, nil)
	captchaReq.Header.Set("User-Agent", p.userAgent)
	captchaReq.Header.Set("Cookie", strings.Join(initialCookies, "; "))
	captchaResp, err := client.Do(captchaReq)
	if err == nil {
		captchaBody, _ := io.ReadAll(captchaResp.Body)
		captchaResp.Body.Close()
		var captchaResult map[string]interface{}
		if json.Unmarshal(captchaBody, &captchaResult) == nil {
			if show, ok := captchaResult["show_captcha"]; ok && show == true {
				slog.Warn("================================================================")
				slog.Warn("ZHIHU LOGIN BLOCKED — CAPTCHA REQUIRED")
				slog.Warn("Automatic login cannot bypass Zhihu's CAPTCHA.")
				slog.Warn("")
				slog.Warn("HOW TO FIX — Obtain cookies from your browser:")
				slog.Warn("  1. Open Chrome and log into zhihu.com normally")
				slog.Warn("  2. Press F12 to open DevTools")
				slog.Warn("  3. Go to Application → Cookies → https://www.zhihu.com")
				slog.Warn("  4. Copy ALL cookies as a semicolon-separated string")
				slog.Warn("  5. Save them to: data/cookies/zhihu.cookie")
				slog.Warn("     OR set environment variable: ZHIHU_COOKIE=<your_cookies>")
				slog.Warn("  6. Restart the application")
				slog.Warn("================================================================")
				return "", fmt.Errorf("zhihu requires CAPTCHA; auto-login blocked — please manually set ZHIHU_COOKIE or save cookies to data/cookies/zhihu.cookie")
			}
		}
	}

	// Step 3: Fetch RSA public key from Zhihu's signin JS bundle
	publicKeyPEM, err := fetchZhihuPublicKey(ctx, client, string(htmlBody))
	if err != nil {
		slog.Warn("could not fetch zhihu RSA public key",
			"error", err,
			"hint", "Zhihu frontend may have changed. If login fails, use cookie mode: set ZHIHU_COOKIE env var or save data/cookies/zhihu.cookie")
		// Try without encryption — some API versions accept plaintext
		// But this is unlikely to work with modern Zhihu
		return p.performLoginWithoutEncryption(ctx, client, initialCookies, xsrfToken)
	}

	publicKey, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return "", fmt.Errorf("parse RSA public key: %w", err)
	}

	// Step 4: Encrypt password with RSA PKCS#1 v1.5
	encryptedPassword, err := rsaEncryptPKCS1v15(publicKey, p.password)
	if err != nil {
		return "", fmt.Errorf("encrypt password: %w", err)
	}
	slog.Debug("password encrypted with RSA", "platform", "zhihu")

	// Step 5: POST login credentials
	loginPayload := map[string]interface{}{
		"client_id":  zhihuClientID,
		"username":   p.username,
		"password":   encryptedPassword,
		"captcha":    "",
		"source":     "com.zhihu.web",
		"ref_source": "homepage",
		"utm_source": "",
		"timestamp":  time.Now().UnixMilli(),
		"grant_type": "password",
	}

	return p.doLoginPost(ctx, client, loginPayload, initialCookies, xsrfToken)
}

// performLoginWithoutEncryption tries login with plaintext password as fallback.
func (p *ZhihuPublisher) performLoginWithoutEncryption(ctx context.Context, client *http.Client, initialCookies []string, xsrfToken string) (string, error) {
	loginPayload := map[string]interface{}{
		"client_id":  zhihuClientID,
		"username":   p.username,
		"password":   p.password,
		"captcha":    "",
		"source":     "com.zhihu.web",
		"ref_source": "homepage",
		"utm_source": "",
		"timestamp":  time.Now().UnixMilli(),
		"grant_type": "password",
	}
	return p.doLoginPost(ctx, client, loginPayload, initialCookies, xsrfToken)
}

// doLoginPost sends the login POST request and collects cookies from the response.
func (p *ZhihuPublisher) doLoginPost(ctx context.Context, client *http.Client, loginPayload map[string]interface{}, initialCookies []string, xsrfToken string) (string, error) {
	payloadBytes, _ := json.Marshal(loginPayload)
	loginReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, zhihuAPIs["login"],
		bytes.NewReader(payloadBytes))
	loginReq.Header.Set("User-Agent", p.userAgent)
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Xsrftoken", xsrfToken)
	loginReq.Header.Set("Origin", "https://www.zhihu.com")
	loginReq.Header.Set("Referer", "https://www.zhihu.com/signin")
	loginReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	loginReq.Header.Set("Cookie", strings.Join(initialCookies, "; "))

	resp, err := client.Do(loginReq)
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var loginResult map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &loginResult); err != nil {
		return "", fmt.Errorf("parse login response: %w (raw: %s)", err, truncateStr(string(bodyBytes), 300))
	}

	// Check for API-level errors (code 10001 = parameter error, code 10002 = rate limit, etc.)
	if errCode, ok := loginResult["error"]; ok {
		errMap, ok := errCode.(map[string]interface{})
		if ok {
			code := fmt.Sprintf("%v", errMap["code"])
			msg := fmt.Sprintf("%v", errMap["message"])
			// code:10001 means "request parameters abnormal, please upgrade client"
			// This usually means: client_id changed, API endpoint moved, or signature headers needed
			if code == "10001" {
				slog.Warn("================================================================")
				slog.Warn("ZHIHU AUTO-LOGIN FAILED — API REJECTED REQUEST (code 10001)")
				slog.Warn("Zhihu's login API requires updated client parameters.")
				slog.Warn("The auto-login flow cannot complete without reverse-engineering")
				slog.Warn("Zhihu's current webpack bundle for signature generation.")
				slog.Warn("")
				slog.Warn("SOLUTION: Use cookie-based auth instead:")
				slog.Warn("  1. Open Chrome, log into zhihu.com")
				slog.Warn("  2. F12 → Application → Cookies → zhihu.com")
				slog.Warn("  3. Copy ALL cookies to: data/cookies/zhihu.cookie")
				slog.Warn("     OR set env: ZHIHU_COOKIE=<all_cookies>")
				slog.Warn("  4. Restart the app — cookies work for ~7 days")
				slog.Warn("================================================================")
				return "", fmt.Errorf("zhihu login API rejected request (code: %s, msg: %s) — use cookie-based auth instead: set ZHIHU_COOKIE env var or save cookies to data/cookies/zhihu.cookie", code, msg)
			}
			return "", fmt.Errorf("zhihu login failed: [%s] %s (response: %s)", code, msg, truncateStr(string(bodyBytes), 300))
		}
		// Plain error string
		errMsg := fmt.Sprintf("%v", errCode)
		if strings.Contains(errMsg, "captcha") || strings.Contains(errMsg, "验证码") {
			return "", fmt.Errorf("zhihu login requires CAPTCHA: %s — use cookie mode: set ZHIHU_COOKIE env var", errMsg)
		}
		return "", fmt.Errorf("zhihu login failed: %s (response: %s)", errMsg, truncateStr(string(bodyBytes), 300))
	}

	// Collect all cookies from login response
	var allCookies []string
	allCookies = append(allCookies, initialCookies...)
	for _, c := range resp.Cookies() {
		found := false
		for i, existing := range allCookies {
			if strings.HasPrefix(existing, c.Name+"=") {
				allCookies[i] = fmt.Sprintf("%s=%s", c.Name, c.Value)
				found = true
				break
			}
		}
		if !found {
			allCookies = append(allCookies, fmt.Sprintf("%s=%s", c.Name, c.Value))
		}
	}

	cookieStr := strings.Join(allCookies, "; ")
	if cookieStr == "" {
		return "", fmt.Errorf("no cookies extracted after login")
	}

	slog.Info("zhihu auto-login successful", "cookie_count", len(allCookies))
	return cookieStr, nil
}

// extractXSRFFromHTML extracts the _xsrf token from the signin page HTML body.
func extractXSRFFromHTML(html string) string {
	// Pattern: \"_xsrf\":\"xxxxx\" or _xsrf=xxxxx or name=\"_xsrf\" value=\"xxxxx\"
	patterns := []string{
		`"_xsrf"\s*:\s*"([^"]+)"`,
		`_xsrf\s*=\s*([a-zA-Z0-9]+)`,
		`name="_xsrf"\s+value="([^"]+)"`,
		`<input[^>]*name="_xsrf"[^>]*value="([^"]+)"`,
	}
	for _, pat := range patterns {
		re := regexp.MustCompile(pat)
		if matches := re.FindStringSubmatch(html); len(matches) >= 2 {
			return matches[1]
		}
	}
	return ""
}

// fetchZhihuPublicKey fetches the RSA public key from Zhihu's signin JS bundle.
// Zhihu embeds its RSA public key in the webpack JS bundles loaded on the signin page.
// The bundle URL pattern and key extraction logic may need updating as Zhihu changes its frontend.
func fetchZhihuPublicKey(ctx context.Context, client *http.Client, htmlBody string) (string, error) {
	// Find the main JS bundle URL from the signin page HTML
	// Zhihu serves JS bundles from static.zhihu.com/heifetz/
	jsPatterns := []string{
		`https://static\.zhihu\.com/heifetz/[^"]+sign[^"]*\.js`,
		`https://static\.zhihu\.com/heifetz/main\.[a-f0-9]+\.js`,
		`https://static\.zhihu\.com/heifetz/[^"]+login[^"]*\.js`,
		`https://static\.zhihu\.com/heifetz/runtime[^"]*\.js`,
		`https://static\.zhihu\.com/heifetz/[^"]+\.js`, // broad fallback
	}

	var jsURL string
	for _, pat := range jsPatterns {
		re := regexp.MustCompile(pat)
		if m := re.FindString(htmlBody); m != "" {
			jsURL = m
			break
		}
	}
	if jsURL == "" {
		return "", fmt.Errorf("could not find zhihu JS bundle URL in signin page")
	}

	slog.Debug("fetching zhihu JS bundle for public key", "url", jsURL)
	jsReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, jsURL, nil)
	jsReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	jsResp, err := client.Do(jsReq)
	if err != nil {
		return "", fmt.Errorf("fetch JS bundle: %w", err)
	}
	defer jsResp.Body.Close()
	jsBody, _ := io.ReadAll(jsResp.Body)
	jsContent := string(jsBody)

	// Extract public key from JS — common patterns in zhihu's webpack bundle
	// Zhihu stores its RSA public key in PEM format within the JS
	keyPatterns := []string{
		// PEM format with escaped newlines (most common)
		`"-----BEGIN PUBLIC KEY-----\\n([^"]+)\\n-----END PUBLIC KEY-----"`,
		// Unescaped PEM format
		`-----BEGIN PUBLIC KEY-----\s*([A-Za-z0-9+/=\s]+)-----END PUBLIC KEY-----`,
		// JSON key assignment
		`"publicKey"\s*:\s*"([^"]+)"`,
		`publicKey\s*=\s*"([^"]+)"`,
		// Base64-encoded RSA key reference
		`"rsaKey"\s*:\s*"([^"]+)"`,
		// Encapsulated in a function parameter
		`PUBLIC_KEY\s*=\s*"([^"]+)"`,
	}

	for _, pat := range keyPatterns {
		re := regexp.MustCompile(pat)
		if m := re.FindStringSubmatch(jsContent); len(m) >= 2 {
			key := m[1]
			// Normalize: if the key contains \\n, it's the escaped version
			if strings.Contains(key, "\\n") {
				key = strings.ReplaceAll(key, "\\n", "\n")
				key = "-----BEGIN PUBLIC KEY-----\n" + key + "\n-----END PUBLIC KEY-----"
			} else if !strings.Contains(key, "BEGIN PUBLIC KEY") {
				// It might be a raw base64 key — wrap it
				key = "-----BEGIN PUBLIC KEY-----\n" + key + "\n-----END PUBLIC KEY-----"
			}
			slog.Debug("extracted zhihu RSA public key from JS bundle", "pattern", pat)
			return key, nil
		}
	}

	return "", fmt.Errorf("could not extract RSA public key from zhihu JS bundle; the bundle format may have changed")
}

// parseRSAPublicKey parses a PEM-encoded RSA public key.
func parseRSAPublicKey(pemKey string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		// Try PKCS1 format
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}
	return rsaPub, nil
}

// rsaEncryptPKCS1v15 encrypts plaintext with RSA PKCS#1 v1.5 padding.
func rsaEncryptPKCS1v15(pub *rsa.PublicKey, plaintext string) (string, error) {
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// publishArticle publishes a long-form article to Zhihu.
func (p *ZhihuPublisher) publishArticle(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	title := content.ZHTitle
	if title == "" {
		title = content.Topic
	}
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
		slog.Warn("zhihu body empty, falling back to xhs body", "content_id", content.ID)
	}

	// Append ZHTopics as hashtags at the end of the article body
	if topicTags := strings.TrimSpace(content.ZHTopics); topicTags != "" {
		body += "\n\n" + topicTags
	}

	// Use first XHS image as titleImage (article cover) if available
	titleImage := ""
	if images := strings.TrimSpace(content.XHSImages); images != "" {
		firstImage := strings.Split(images, ",")[0]
		titleImage = strings.TrimSpace(firstImage)
	}

	// Create draft
	draftPayload := map[string]interface{}{
		"title":      title,
		"content":    body,
		"titleImage": titleImage,
	}
	draftID, err := p.createDraft(ctx, draftPayload)
	if err != nil {
		return p.failResult("create draft", err, start), err
	}
	slog.Info("draft created", "platform", "zhihu", "draft_id", draftID)

	// Publish draft
	if err := p.publishDraft(ctx, draftID); err != nil {
		return p.failResult("publish draft", err, start), err
	}

	articleURL := fmt.Sprintf("https://zhuanlan.zhihu.com/p/%s", draftID)
	slog.Info("article published", "platform", "zhihu", "url", articleURL)

	return &PublishResult{
		Success:     true,
		Platform:    "zhihu",
		ContentID:   draftID,
		URL:         articleURL,
		Message:     "publish success",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// publishIdea publishes a short idea/pin to Zhihu.
// Zhihu pins API expects: {"content":[{"type":"text","content":"..."}],"topics":[]}
func (p *ZhihuPublisher) publishIdea(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
		slog.Warn("zhihu body empty, falling back to xhs body", "content_id", content.ID)
	}

	// Zhihu pins API uses a rich-text content format
	payload := map[string]interface{}{
		"content": []map[string]string{
			{
				"type":    "text",
				"content": body,
			},
		},
		"topics": []string{},
	}

	respBody, err := p.doAPI(ctx, http.MethodPost, "https://www.zhihu.com/api/v4/pins", payload)
	if err != nil {
		return p.failResult("publish idea", err, start), err
	}

	pinID := extractJSONField(respBody, "id")
	slog.Info("idea published", "platform", "zhihu", "pin_id", pinID)

	return &PublishResult{
		Success:     true,
		Platform:    "zhihu",
		ContentID:   pinID,
		URL:         fmt.Sprintf("https://www.zhihu.com/pin/%s", pinID),
		Message:     "publish success",
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}, nil
}

// createDraft creates a draft article on Zhihu.
func (p *ZhihuPublisher) createDraft(ctx context.Context, payload map[string]interface{}) (string, error) {
	respBody, err := p.doAPI(ctx, http.MethodPost, zhihuAPIs["create_draft"], payload)
	if err != nil {
		return "", err
	}

	id := extractJSONField(respBody, "id")
	if id == "" {
		return "", fmt.Errorf("could not extract article ID from response: %s", string(respBody))
	}
	return id, nil
}

// publishDraft publishes a draft article by ID.
func (p *ZhihuPublisher) publishDraft(ctx context.Context, draftID string) error {
	apiURL := fmt.Sprintf("%s/%s/publish", zhihuAPIs["create_draft"], draftID)
	_, err := p.doAPI(ctx, http.MethodPut, apiURL, map[string]interface{}{
		"status": "published",
	})
	return err
}

// doAPI performs an authenticated HTTP request to the Zhihu API.
func (p *ZhihuPublisher) doAPI(ctx context.Context, method, apiURL string, payload interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	cookie, err := p.cookieMgr.GetCookie(ctx)
	if err != nil {
		return nil, fmt.Errorf("get cookie for API call: %w", err)
	}
	p.setHeaders(req, cookie)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Check if it's an auth issue
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			p.cookieMgr.Invalidate()
		}
		return nil, fmt.Errorf("zhihu API error (HTTP %d): %s", resp.StatusCode,
			truncateStr(string(respBytes), 200))
	}

	return respBytes, nil
}

// setHeaders sets common HTTP headers for Zhihu API requests.
// NOTE: We intentionally do NOT set X-Requested-With — Zhihu uses this as a bot signal.
// We also avoid the old XMLHttpRequest pattern since modern Zhihu uses the x-zse-* header family.
func (p *ZhihuPublisher) setHeaders(req *http.Request, cookie string) {
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	// DO NOT set X-Requested-With — triggers anti-bot detection on Zhihu
	req.Header.Set("Origin", "https://zhuanlan.zhihu.com")
	req.Header.Set("Referer", "https://zhuanlan.zhihu.com/")
	// Standard headers that a real browser would send
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
}

// failResult creates a failed PublishResult.
func (p *ZhihuPublisher) failResult(stage string, err error, start time.Time) *PublishResult {
	return &PublishResult{
		Success:     false,
		Platform:    "zhihu",
		Message:     fmt.Sprintf("%s: %v", stage, err),
		PublishedAt: time.Now(),
		Duration:    time.Since(start),
	}
}

// ============================================================================
// Browser-based Login (fallback when HTTP API login fails)
// ============================================================================

// performBrowserLogin opens a Chrome browser window for the user to log into
// Zhihu manually. After login, it extracts cookies automatically.
// The browser window appears on the user's desktop — they just need to log in.
func (p *ZhihuPublisher) performBrowserLogin(ctx context.Context) (string, error) {
	slog.Info("================================================================")
	slog.Info("🔵 ZHIHU BROWSER LOGIN")
	slog.Info("   A Chrome window will open at https://www.zhihu.com/signin")
	slog.Info("   Please log in with your account. Cookies are saved for 7 days.")
	slog.Info("================================================================")

	l := launcher.New()
	if p.headless {
		l = l.Headless(true)
	}
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")
	l = l.Set("exclude-switches", "enable-automation")
	l = l.Set("window-size", "1280,900")
	l = l.Set("lang", "zh-CN")

	// Use persistent user data so subsequent logins reuse cookies
	userDataDir := filepath.Join(p.screenshotDir, "..", "chrome-profile", "zhihu")
	l = l.UserDataDir(userDataDir)

	url, err := l.Launch()
	if err != nil {
		return "", fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return "", fmt.Errorf("connect browser: %w", err)
	}
	defer browser.Close()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return "", fmt.Errorf("create page: %w", err)
	}

	if err := page.Navigate("https://www.zhihu.com/signin"); err != nil {
		return "", fmt.Errorf("navigate to zhihu: %w", err)
	}
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)

	// Wait for user to complete login (up to 5 minutes)
	cookieStr, err := p.waitForZhihuBrowserLogin(page, 5*time.Minute)
	if err != nil {
		p.zhihuScreenshot(page, "browser_login_timeout")
		return "", fmt.Errorf("browser login failed: %w", err)
	}

	p.zhihuScreenshot(page, "browser_login_success")
	slog.Info("zhihu browser login successful, cookies saved")
	return cookieStr, nil
}

// waitForZhihuBrowserLogin polls until the user completes login in the browser.
func (p *ZhihuPublisher) waitForZhihuBrowserLogin(page *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	lastMsg := time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		if time.Since(lastMsg) > 30*time.Second {
			slog.Info("zhihu: waiting for login in browser...", "timeout_remaining", time.Until(deadline).Round(time.Second))
			lastMsg = time.Now()
			p.zhihuScreenshot(page, "browser_login_waiting")
		}

		info, err := page.Info()
		if err != nil {
			continue
		}

		// Success: navigated away from signin
		if !strings.Contains(info.URL, "signin") && !strings.Contains(info.URL, "login") {
			return p.zhihuExtractCookies(page)
		}

		// Also check: did we land on the zhihu homepage or feed?
		if strings.Contains(info.URL, "zhihu.com") && !strings.Contains(info.URL, "signin") {
			// May have logged in via QR — check cookies
			cookies, err := page.Browser().GetCookies()
			if err == nil {
				for _, c := range cookies {
					if c.Name == "z_c0" && c.Value != "" {
						return p.zhihuExtractCookies(page)
					}
				}
			}
		}
	}

	return "", fmt.Errorf("browser login timed out after %v", timeout)
}

// zhihuExtractCookies extracts Zhihu cookies from the browser.
func (p *ZhihuPublisher) zhihuExtractCookies(page *rod.Page) (string, error) {
	cookies, err := page.Browser().GetCookies()
	if err != nil {
		return "", fmt.Errorf("get cookies: %w", err)
	}

	var pairs []string
	for _, c := range cookies {
		if strings.Contains(c.Domain, "zhihu.com") {
			pairs = append(pairs, fmt.Sprintf("%s=%s", c.Name, c.Value))
		}
	}
	cookieStr := strings.Join(pairs, "; ")
	if cookieStr == "" {
		return "", fmt.Errorf("no zhihu cookies found")
	}

	// Verify essential cookies exist
	hasZC0 := strings.Contains(cookieStr, "z_c0=")
	if !hasZC0 {
		slog.Warn("zhihu: z_c0 cookie missing — login may be incomplete")
	}

	slog.Info("zhihu cookies extracted", "cookie_count", len(pairs), "has_z_c0", hasZC0)
	return cookieStr, nil
}

// zhihuScreenshot saves a debug screenshot (same pattern as XHS publisher).
func (p *ZhihuPublisher) zhihuScreenshot(page *rod.Page, name string) {
	if p.screenshotDir == "" {
		return
	}
	if err := os.MkdirAll(p.screenshotDir, 0755); err != nil {
		return
	}
	path := filepath.Join(p.screenshotDir, fmt.Sprintf("%s_zhihu_%s.png",
		time.Now().Format("20060102_150405"), name))
	data, err := page.Screenshot(true, &proto.PageCaptureScreenshot{})
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
	slog.Debug("zhihu screenshot saved", "path", path)
}

// validateZhihuContent checks that a Zhihu post has enough content to publish.
func validateZhihuContent(content *model.Content) error {
	if content == nil {
		return fmt.Errorf("content is nil")
	}
	title := strings.TrimSpace(content.ZHTitle)
	if title == "" {
		title = strings.TrimSpace(content.Topic)
	}
	body := strings.TrimSpace(content.ZHBody)
	if body == "" {
		body = strings.TrimSpace(content.XHSBody)
	}
	if title == "" {
		return fmt.Errorf("missing title")
	}
	if body == "" {
		return fmt.Errorf("missing body")
	}
	return nil
}

// extractJSONField extracts a field value from a JSON byte slice.
func extractJSONField(data []byte, key string) string {
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return ""
	}
	if val, ok := result[key]; ok {
		return fmt.Sprintf("%v", val)
	}
	return ""
}

// truncateStr truncates a string to maxLen characters.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

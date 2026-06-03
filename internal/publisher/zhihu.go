package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"auto-publisher/internal/model"
)

// ZhihuPublisher publishes content to Zhihu (知乎) via HTTP API with cookie authentication.
// Supports both account/password auto-login and manual cookie injection.
type ZhihuPublisher struct {
	client    *http.Client
	cookieMgr *CookieManager
	userAgent string
	username  string
	password  string
}

// ZhihuAPI endpoints used by the publisher.
var zhihuAPIs = map[string]string{
	"check_login":  "https://www.zhihu.com/api/v4/me",
	"create_draft": "https://zhuanlan.zhihu.com/api/articles",
	"upload_image": "https://zhuanlan.zhihu.com/api/images",
	"login":        "https://www.zhihu.com/api/v3/oauth/sign_in",
	"login_captcha": "https://www.zhihu.com/api/v3/oauth/captcha?lang=en",
}

// NewZhihuPublisher creates a Zhihu publisher with manual cookie string.
// cookieSource: cookie string or path to .cookie/.txt file.
func NewZhihuPublisher(cookieSource string) *ZhihuPublisher {
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
		cookieMgr: NewCookieManager("zhihu", "", WithManualCookie(cookieStr)),
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
	}
}

// NewZhihuPublisherWithLogin creates a Zhihu publisher with account/password auto-login.
// cookieDir is where cached cookies are stored.
func NewZhihuPublisherWithLogin(cookieDir, username, password string) *ZhihuPublisher {
	p := &ZhihuPublisher{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		username:  username,
		password:  password,
	}

	loginFunc := func(ctx context.Context) (string, error) {
		return p.performLogin(ctx)
	}

	// Check for manual cookie as fallback
	manualCookie := os.Getenv("ZHIHU_COOKIE")
	if manualCookie == "" {
		if data, err := os.ReadFile("zhihu.cookie"); err == nil {
			manualCookie = strings.TrimSpace(string(data))
		}
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		p.cookieMgr.Invalidate()
		return fmt.Errorf("zhihu login expired (HTTP %d): %s", resp.StatusCode, truncateStr(string(body), 200))
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
func (p *ZhihuPublisher) Publish(ctx context.Context, content *model.Content) (*PublishResult, error) {
	start := time.Now()

	// Validate login session first
	if err := p.Validate(ctx); err != nil {
		return p.failResult("login validation", err, start), err
	}

	// Route by content type
	switch content.Type {
	case model.TypeArticle:
		return p.publishArticle(ctx, content, start)
	case model.TypeIdea:
		return p.publishIdea(ctx, content, start)
	default:
		return p.publishArticle(ctx, content, start)
	}
}

// performLogin performs Zhihu account/password login via HTTP API.
// Returns the cookie string on success.
func (p *ZhihuPublisher) performLogin(ctx context.Context) (string, error) {
	slog.Info("starting zhihu auto-login")

	// Step 1: Get login page to obtain cookies and XSRF token
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.zhihu.com/signin", nil)
	req.Header.Set("User-Agent", p.userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch signin page: %w", err)
	}
	resp.Body.Close()

	// Extract cookies and XSRF token
	var xsrfToken string
	var initialCookies []string
	for _, c := range resp.Cookies() {
		initialCookies = append(initialCookies, fmt.Sprintf("%s=%s", c.Name, c.Value))
		if c.Name == "_xsrf" {
			xsrfToken = c.Value
		}
	}

	if xsrfToken == "" {
		return "", fmt.Errorf("could not extract _xsrf token from signin page")
	}
	slog.Debug("got xsrf token", "platform", "zhihu")

	// Step 2: POST login credentials
	loginPayload := map[string]string{
		"username":  p.username,
		"password":  p.password,
		"captcha":   "",
		"timestamp": fmt.Sprintf("%d", time.Now().UnixMilli()),
		"source":    "com.zhihu.web",
		"ref_source": "homepage",
		"utm_source": "",
	}

	payloadBytes, _ := json.Marshal(loginPayload)
	loginReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, zhihuAPIs["login"],
		bytes.NewReader(payloadBytes))
	loginReq.Header.Set("User-Agent", p.userAgent)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Header.Set("X-Xsrftoken", xsrfToken)
	loginReq.Header.Set("Origin", "https://www.zhihu.com")
	loginReq.Header.Set("Referer", "https://www.zhihu.com/signin")
	loginReq.Header.Set("Cookie", strings.Join(initialCookies, "; "))

	resp, err = client.Do(loginReq)
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var loginResult map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &loginResult); err != nil {
		return "", fmt.Errorf("parse login response: %w", err)
	}

	// Check for CAPTCHA requirement
	if errCode, ok := loginResult["error"]; ok {
		errMsg := fmt.Sprintf("%v", errCode)
		if strings.Contains(errMsg, "captcha") {
			return "", fmt.Errorf("zhihu login requires CAPTCHA: %s", errMsg)
		}
		return "", fmt.Errorf("zhihu login failed: %s", errMsg)
	}

	// Step 3: Collect all cookies from login response
	var allCookies []string
	allCookies = append(allCookies, initialCookies...)
	for _, c := range resp.Cookies() {
		// Update existing cookies with new values
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

// publishArticle publishes a long-form article to Zhihu.
func (p *ZhihuPublisher) publishArticle(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	title := content.ZHTitle
	if title == "" {
		title = content.Topic
	}
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
	}

	// Create draft
	draftPayload := map[string]interface{}{
		"title":      title,
		"content":    body,
		"titleImage": "",
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
func (p *ZhihuPublisher) publishIdea(ctx context.Context, content *model.Content, start time.Time) (*PublishResult, error) {
	body := content.ZHBody
	if body == "" {
		body = content.XHSBody
	}

	payload := map[string]interface{}{
		"content": body,
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
func (p *ZhihuPublisher) setHeaders(req *http.Request, cookie string) {
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", "https://zhuanlan.zhihu.com")
	req.Header.Set("Referer", "https://zhuanlan.zhihu.com/")
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

// Helper for URL encoding (used in login payload).
func urlEncode(v url.Values) string {
	return v.Encode()
}

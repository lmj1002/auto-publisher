package collector

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

// XHSCollector searches for similar content on Xiaohongshu via browser automation.
// Since Xiaohongshu does not provide a public search API, it automates the web interface.
type XHSCollector struct {
	cookieStr     string
	headless      bool
	screenshotDir string
	scorer        SimilarityScorer
}

// NewXHSCollector creates a new Xiaohongshu content collector.
// cookieStr is an optional login cookie string for authenticated search.
func NewXHSCollector(cookieStr string, headless bool) *XHSCollector {
	return &XHSCollector{
		cookieStr:     cookieStr,
		headless:      headless,
		screenshotDir: filepath.Join(".", "data", "screenshots", "collector"),
		scorer:        NewKeywordScorer(),
	}
}

// Name returns the collector name.
func (c *XHSCollector) Name() string {
	return "xiaohongshu-collector"
}

// Platform returns the target platform.
func (c *XHSCollector) Platform() model.Platform {
	return model.PlatformXiaohongshu
}

// IsAvailable checks if a browser is available.
func (c *XHSCollector) IsAvailable(ctx context.Context) bool {
	_, found := launcher.LookPath()
	if !found {
		slog.Warn("no browser found for xiaohongshu collector")
		return false
	}
	return true
}

// Search finds Xiaohongshu content matching the given keywords via browser automation.
func (c *XHSCollector) Search(ctx context.Context, keywords []string, maxResults int) ([]model.CollectedContent, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	query := strings.Join(keywords, " ")
	slog.Info("searching xiaohongshu", "query", query, "max_results", maxResults)

	l := launcher.New()
	if c.headless {
		l = l.Headless(true)
	}
	l = l.NoSandbox(true)
	l = l.Set("disable-blink-features", "AutomationControlled")

	launcherURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(launcherURL).MustConnect()
	defer browser.Close()

	// Inject cookies if available
	if c.cookieStr != "" {
		page := browser.MustPage("")
		cookieParams := c.parseCookieParams(c.cookieStr)
		if len(cookieParams) > 0 {
			page.SetCookies(cookieParams)
		}
	}

	// Navigate to Xiaohongshu search page
	searchURL := fmt.Sprintf("https://www.xiaohongshu.com/search_result?keyword=%s&type=51",
		urlEncode(query))
	page := browser.MustPage(searchURL)
	page.MustWaitLoad()
	time.Sleep(3 * time.Second)

	// Scroll to load more results
	for i := 0; i < 3 && len(c.collectCards(page)) < maxResults; i++ {
		page.MustEval(`() => window.scrollTo(0, document.body.scrollHeight)`)
		time.Sleep(2 * time.Second)
	}

	// Collect note cards
	cards := c.collectCards(page)
	var results []model.CollectedContent
	for _, card := range cards {
		if len(results) >= maxResults {
			break
		}
		result := c.parseCard(card, keywords)
		if result != nil && result.RelevanceScore >= 0.1 {
			results = append(results, *result)
		}
	}

	// Save debug screenshot
	if err := os.MkdirAll(c.screenshotDir, 0755); err == nil {
		path := filepath.Join(c.screenshotDir,
			fmt.Sprintf("xhs_search_%s.png", time.Now().Format("20060102_150405")))
		if data, err := page.Screenshot(true, nil); err == nil {
			os.WriteFile(path, data, 0644)
		}
	}

	slog.Info("xiaohongshu search complete", "found", len(results), "query", query)
	return results, nil
}

// collectCards extracts note card elements from the search results page.
func (c *XHSCollector) collectCards(page *rod.Page) []*rod.Element {
	selectors := []string{
		"[class*='note-item']",
		"[class*='feeds-card']",
		"section.note-item",
		"[class*='search-result'] a[href*='/explore/']",
	}
	for _, sel := range selectors {
		els, err := page.Elements(sel)
		if err == nil && len(els) > 0 {
			return els
		}
	}
	return nil
}

// parseCard extracts content information from a single note card element.
func (c *XHSCollector) parseCard(card *rod.Element, keywords []string) *model.CollectedContent {
	// Extract title
	title := ""
	titleEls, err := card.Elements("[class*='title'], .note-title, span.title")
	if err == nil {
		for _, el := range titleEls {
			title = el.MustText()
			if title != "" {
				break
			}
		}
	}

	// Extract note link
	linkEl, err := card.Element("a[href*='/explore/']")
	if err != nil {
		return nil
	}
	hrefPtr := linkEl.MustAttribute("href")
	if hrefPtr == nil {
		return nil
	}
	href := *hrefPtr
	if !strings.HasPrefix(href, "http") {
		href = "https://www.xiaohongshu.com" + href
	}

	// Extract author
	author := ""
	authorEls, err := card.Elements("[class*='author'], .nickname, span.name")
	if err == nil {
		for _, el := range authorEls {
			author = el.MustText()
			if author != "" {
				break
			}
		}
	}

	// Extract likes count
	likes := 0
	likeEls, err := card.Elements("[class*='like'] span, [class*='count']")
	if err == nil {
		for _, el := range likeEls {
			text := el.MustText()
			if count, ok := parseCount(text); ok {
				likes = count
				break
			}
		}
	}

	// Compute relevance score
	score := c.scorer.Score(keywords, title, "")

	return &model.CollectedContent{
		Platform:       model.PlatformXiaohongshu,
		SourceURL:      href,
		Title:          title,
		Author:         author,
		Keywords:       strings.Join(keywords, ","),
		RelevanceScore: score,
		Likes:          likes,
		SyncedAt:       time.Now(),
	}
}

// parseCookieParams converts a cookie string to rod's NetworkCookieParam format.
func (c *XHSCollector) parseCookieParams(cookieStr string) []*proto.NetworkCookieParam {
	var params []*proto.NetworkCookieParam
	pairs := strings.Split(cookieStr, ";")
	for _, pair := range pairs {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) == 2 {
			params = append(params, &proto.NetworkCookieParam{
				Name:   strings.TrimSpace(kv[0]),
				Value:  strings.TrimSpace(kv[1]),
				Domain: ".xiaohongshu.com",
			})
		}
	}
	return params
}

// urlEncode performs simple URL encoding for query strings.
func urlEncode(s string) string {
	s = strings.ReplaceAll(s, " ", "%20")
	s = strings.ReplaceAll(s, "#", "%23")
	s = strings.ReplaceAll(s, "&", "%26")
	return s
}

// parseCount parses a count string like "1.2万" or "345" into an integer.
func parseCount(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Handle "万" (10k) notation
	if strings.HasSuffix(s, "万") {
		s = strings.TrimSuffix(s, "万")
		var val float64
		if _, err := fmt.Sscanf(s, "%f", &val); err == nil {
			return int(val * 10000), true
		}
	}
	// Plain number
	var val int
	if _, err := fmt.Sscanf(s, "%d", &val); err == nil {
		return val, true
	}
	return 0, false
}

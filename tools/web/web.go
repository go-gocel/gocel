// Package web provides web search and page fetch tools for gocel agents.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// Provider supplies web search and page fetch tools backed by an HTTP client.
//
// Provider 提供基于 HTTP 客户端的网页搜索与页面抓取工具。
type Provider struct{ client *http.Client }

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// New creates a web provider with a 30-second client timeout.
//
// New 创建带 30 秒客户端超时的 web provider。
func New() *Provider {
	return &Provider{client: &http.Client{Timeout: 30 * time.Second}}
}

// ListTools returns the provider's tools.
//
// ListTools 返回该 provider 提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{&webSearchTool{p}, &webFetchTool{p}}
}

type webSearchTool struct{ provider *Provider }

// Name returns the tool name "web_search".
//
// Name 返回工具名 "web_search"。
func (t *webSearchTool) Name() string { return "web_search" }
// Description describes the web_search tool.
//
// Description 描述 web_search 工具：使用 Bing 搜索网页并返回标题、URL 与摘要。
func (t *webSearchTool) Description() string {
	return "Search the web using Bing. Returns title, URL, snippet."
}
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *webSearchTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *webSearchTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "gocel"} }

// Run performs the web search and returns the JSON-encoded results.
//
// Run 执行网页搜索并返回 JSON 编码的结果。
func (t *webSearchTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args struct{ Query string }
	json.Unmarshal([]byte(argsJSON), &args)
	results, err := t.provider.search(ctx, args.Query)
	if err != nil {
		return toolutil.FormatError(err), err
	}
	data, _ := json.Marshal(results)
	return string(data), nil
}

type webFetchTool struct{ provider *Provider }

// Name returns the tool name "web_fetch".
//
// Name 返回工具名 "web_fetch"。
func (t *webFetchTool) Name() string        { return "web_fetch" }
// Description describes the web_fetch tool.
//
// Description 描述 web_fetch 工具：抓取网页文本内容。
func (t *webFetchTool) Description() string { return "Fetch web page text content." }
// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *webFetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":       map[string]any{"type": "string"},
			"max_chars": map[string]any{"type": "integer"},
		},
		"required": []string{"url"},
	}
}
// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *webFetchTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "gocel"} }

// Run fetches the page at args.URL and returns its text content, truncated
// to args.MaxChars (default 8000).
//
// Run 抓取 args.URL 指向的页面并返回其文本内容，按 args.MaxChars（默认 8000）截断。
func (t *webFetchTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	json.Unmarshal([]byte(argsJSON), &args)
	if args.MaxChars <= 0 {
		args.MaxChars = 8000
	}
	return t.provider.fetch(ctx, args.URL, args.MaxChars)
}

func (p *Provider) search(ctx context.Context, query string) ([]searchResult, error) {
	u := fmt.Sprintf("https://www.bing.com/search?q=%s", url.QueryEscape(query))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// HTTP failures must surface — a 429/block page must not masquerade
	// as "no results".
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web_search: server returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("web_search: read: %w", err)
	}
	return parseBing(string(body)), nil
}

func parseBing(html string) []searchResult {
	var r []searchResult
	for i := 0; i < 10; i++ {
		idx := strings.Index(strings.ToLower(html), "<h2>")
		if idx < 0 {
			break
		}
		html = html[idx+4:]
		linkIdx := strings.Index(strings.ToLower(html), "<a href=\"")
		if linkIdx < 0 {
			continue
		}
		html = html[linkIdx+9:]
		urlEnd := strings.Index(html, "\"")
		if urlEnd < 0 {
			continue
		}
		linkURL := html[:urlEnd]
		html = html[urlEnd+1:]
		titleEnd := strings.Index(strings.ToLower(html), "</a>")
		if titleEnd < 0 {
			continue
		}
		title := stripHTML(html[:titleEnd])
		html = html[titleEnd+4:]
		if title != "" && linkURL != "" {
			r = append(r, searchResult{Title: title, URL: linkURL})
		}
	}
	return r
}

func (p *Provider) fetch(ctx context.Context, rawURL string, maxChars int) (string, error) {
	// SSRF guard: http/https only, no loopback/private/link-local hosts,
	// redirects re-validated (the old code accepted any URL — file://,
	// cloud metadata, internal services — and followed 10 redirects).
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("web_fetch: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("web_fetch: scheme %q not allowed (http/https only)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("web_fetch: missing host")
	}
	if err := checkSSRFHost(u.Hostname()); err != nil {
		return "", err
	}

	client := *p.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("web_fetch: too many redirects")
		}
		return checkSSRFHost(req.URL.Hostname())
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// HTTP failures must surface — a 404/block page must not come back as
	// scraped "success" text.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("web_fetch: server returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxChars*4)))
	if err != nil {
		return "", fmt.Errorf("web_fetch: read: %w", err)
	}
	text := htmlToText(string(body))
	if len(text) > maxChars {
		text = text[:maxChars]
	}
	return text, nil
}

// checkSSRFHost rejects loopback, link-local, and private addresses — the
// SSRF surface (cloud metadata, internal services). Hostnames are resolved
// and every resulting IP is checked.
func checkSSRFHost(host string) error {
	host = strings.TrimSuffix(host, ".")
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("web_fetch: address %s is not allowed", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("web_fetch: cannot resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("web_fetch: host %q resolves to blocked address %s", host, ip)
		}
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate()
}

func htmlToText(html string) string {
	for _, tag := range []string{"script", "style", "nav", "footer", "header"} {
		html = stripTag(html, tag)
	}
	text := stripHTML(html)
	var clean []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			clean = append(clean, l)
		}
	}
	return strings.Join(clean, "\n")
}

func stripTag(html, tag string) string {
	ot := "<" + tag
	ct := "</" + tag + ">"
	for {
		s := strings.Index(strings.ToLower(html), ot)
		if s < 0 {
			break
		}
		e := strings.Index(html[s:], ">")
		if e < 0 {
			break
		}
		e2 := strings.Index(strings.ToLower(html[s+e:]), ct)
		if e2 < 0 {
			break
		}
		html = html[:s] + html[s+e+e2+len(ct):]
	}
	return html
}

func stripHTML(html string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(html); i++ {
		if html[i] == '<' {
			in = true
			continue
		}
		if html[i] == '>' {
			in = false
			continue
		}
		if !in {
			b.WriteByte(html[i])
		}
	}
	t := b.String()
	t = strings.ReplaceAll(t, "&amp;", "&")
	t = strings.ReplaceAll(t, "&lt;", "<")
	t = strings.ReplaceAll(t, "&gt;", ">")
	t = strings.ReplaceAll(t, "&quot;", "\"")
	return t
}

// AllTools returns all tools in this package.
//
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}

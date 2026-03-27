/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ---------------------------------------------------------------------------
// make_http_request
// ---------------------------------------------------------------------------

type MakeHTTPRequestTool struct{}

func (t *MakeHTTPRequestTool) Name() string { return "make_http_request" }
func (t *MakeHTTPRequestTool) Description() string {
	return "Make an HTTP request to an external URL."
}

func (t *MakeHTTPRequestTool) Guide() string {
	return `### HTTP Requests (make_http_request)
- External HTTP calls (GET, POST, PUT, DELETE, PATCH). 10s timeout.
- parse_feed=true for RSS/Atom feeds -> structured JSON.
- strip_html=true to extract clean text from web pages.
- For internal API calls in page JS, use fetch() — not this tool.
- To insert/query your own database during build, use manage_data — not this tool.`
}
func (t *MakeHTTPRequestTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url":        map[string]interface{}{"type": "string", "description": "The URL to request"},
			"method":     map[string]interface{}{"type": "string", "description": "HTTP method (GET, HEAD, POST, PUT, DELETE, PATCH). Use HEAD to check URL validity without downloading the body.", "enum": []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH"}},
			"headers":    map[string]interface{}{"type": "object", "description": "Request headers as key-value pairs"},
			"body":       map[string]interface{}{"type": "string", "description": "Request body (for POST, PUT, PATCH)"},
			"strip_html": map[string]interface{}{"type": "boolean", "description": "Strip HTML tags and return clean text content. Useful for reading web pages."},
			"parse_feed": map[string]interface{}{"type": "boolean", "description": "Parse response as RSS/Atom feed. Returns structured JSON: {title, link, description, items: [{title, link, pub_date, description}]}"},
		},
		"required": []string{"url"},
	}
}

func (t *MakeHTTPRequestTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return &Result{Success: false, Error: "url is required"}, nil
	}

	// Resolve relative URLs (e.g. /api/drawings) against the site's own address.
	// These are always local site URLs, so skip SSRF validation for them.
	isLocal := strings.HasPrefix(url, "/")
	if isLocal {
		url = resolveLocalURL(ctx, url)
	}

	// Detect requests to the site's own domain and route them locally.
	// The LLM sometimes uses the full domain URL instead of a relative path.
	if !isLocal {
		if ownDomain := getSiteDomain(ctx); ownDomain != "" {
			if parsed, err := neturl.Parse(url); err == nil && strings.EqualFold(parsed.Hostname(), ownDomain) {
				isLocal = true
				url = "http://localhost:5000" + parsed.RequestURI()
			}
		}
	}

	// Block SSRF: reject non-http(s) schemes and private/internal IPs for
	// absolute URLs (which come from LLM output and are untrusted).
	if !isLocal {
		if err := validateExternalURL(url); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("blocked URL: %v", err)}, nil
		}
	}

	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if body, ok := args["body"].(string); ok && body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
	}

	// Set headers.
	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	client := ssrfSafeClient(isLocal)
	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("executing request: %v", err)}, nil
	}
	defer resp.Body.Close()

	// Read response body, capped at 100KB.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// RSS/Atom feed parsing.
	if parseFeed, ok := args["parse_feed"].(bool); ok && parseFeed {
		feed, err := parseRSSOrAtomFeed(respBody)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("feed parse error: %v", err)}, nil
		}
		return &Result{Success: true, Data: map[string]interface{}{
			"status_code": resp.StatusCode,
			"feed":        feed,
		}}, nil
	}

	bodyStr := string(respBody)
	if stripHTML, ok := args["strip_html"].(bool); ok && stripHTML {
		bodyStr = extractText(respBody)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"body":        bodyStr,
		"headers":     flattenHeaders(resp.Header),
	}}, nil
}

// validateExternalURL blocks SSRF by rejecting dangerous schemes and private IPs.
func validateExternalURL(rawURL string) error {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (only http/https)", scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty hostname")
	}
	// Resolve the hostname to IPs and check each one.
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("unparseable resolved IP %q", ipStr)
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("resolved to private/internal IP %s", ipStr)
		}
	}
	return nil
}

// isPrivateIP returns true for loopback, link-local, and RFC-1918 addresses.
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// ssrfSafeClient returns an http.Client hardened against SSRF for external URLs.
// It re-validates redirect destinations and checks resolved IPs at connect time
// to prevent DNS rebinding attacks.
func ssrfSafeClient(isRelative bool) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if isRelative {
		return client // local URLs don't need SSRF protection
	}

	// Re-validate every redirect destination against SSRF rules.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return validateExternalURL(req.URL.String())
	}

	// Validate resolved IPs at connect time to prevent DNS rebinding.
	client.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip != nil && isPrivateIP(ip) {
					return nil, fmt.Errorf("blocked: resolved to private IP %s", ipStr)
				}
			}
			// Dial the first safe IP directly to pin the resolution.
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
	}

	return client
}

// isLocalDomain returns true for localhost, *.localhost, and other local-only domains.
func isLocalDomain(domain string) bool {
	d := strings.ToLower(domain)
	return d == "localhost" || strings.HasSuffix(d, ".localhost")
}

// getSiteDomain returns the site's configured domain, or "" if none.
func getSiteDomain(ctx *ToolContext) string {
	if ctx.GlobalDB != nil && ctx.SiteID > 0 {
		var domain sql.NullString
		ctx.GlobalDB.QueryRow("SELECT domain FROM sites WHERE id = ?", ctx.SiteID).Scan(&domain)
		if domain.Valid && domain.String != "" {
			return domain.String
		}
	}
	return ""
}

// resolveLocalURL turns a relative path like "/api/drawings" into a full URL
// using the site's domain or falling back to localhost:5000.
func resolveLocalURL(ctx *ToolContext, path string) string {
	if domain := getSiteDomain(ctx); domain != "" && !isLocalDomain(domain) {
		return "https://" + domain + path
	}
	return "http://localhost:5000" + path
}

// flattenHeaders converts http.Header to a simple map.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// extractText strips HTML tags and returns clean text content.
func extractText(htmlBytes []byte) string {
	tokenizer := html.NewTokenizer(strings.NewReader(string(htmlBytes)))
	var b strings.Builder
	skip := 0
	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			// Collapse whitespace and trim.
			result := strings.Join(strings.Fields(b.String()), " ")
			if len(result) > 50000 {
				result = result[:50000]
			}
			return result
		case html.StartTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if tag == "script" || tag == "style" || tag == "noscript" {
				skip++
			}
		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if tag == "script" || tag == "style" || tag == "noscript" {
				if skip > 0 {
					skip--
				}
			}
		case html.TextToken:
			if skip == 0 {
				b.WriteString(tokenizer.Token().Data)
				b.WriteByte(' ')
			}
		}
	}
}

// ---------------------------------------------------------------------------
// RSS / Atom feed parsing
// ---------------------------------------------------------------------------

type feedResult struct {
	Title       string     `json:"title"`
	Link        string     `json:"link"`
	Description string     `json:"description"`
	Items       []feedItem `json:"items"`
}

type feedItem struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	Description string `json:"description"`
	PubDate     string `json:"pub_date,omitempty"`
}

// RSS 2.0 XML structures.
type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}
type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}
type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// Atom XML structures.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}
type atomEntry struct {
	Title   string     `xml:"title"`
	Links   []atomLink `xml:"link"`
	Summary string     `xml:"summary"`
	Content string     `xml:"content"`
	Updated string     `xml:"updated"`
}

const maxFeedItems = 50
const maxFeedDescLen = 500

func parseRSSOrAtomFeed(data []byte) (*feedResult, error) {
	// Try RSS 2.0 first.
	var rss rssRoot
	if err := xml.Unmarshal(data, &rss); err == nil && rss.Channel.Title != "" {
		result := &feedResult{
			Title:       rss.Channel.Title,
			Link:        rss.Channel.Link,
			Description: truncate(rss.Channel.Description, maxFeedDescLen),
		}
		for i, item := range rss.Channel.Items {
			if i >= maxFeedItems {
				break
			}
			result.Items = append(result.Items, feedItem{
				Title:       item.Title,
				Link:        item.Link,
				Description: truncate(stripHTMLTags(item.Description), maxFeedDescLen),
				PubDate:     item.PubDate,
			})
		}
		return result, nil
	}

	// Try Atom.
	var atom atomFeed
	if err := xml.Unmarshal(data, &atom); err == nil && atom.Title != "" {
		link := ""
		for _, l := range atom.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		result := &feedResult{
			Title: atom.Title,
			Link:  link,
		}
		for i, entry := range atom.Entries {
			if i >= maxFeedItems {
				break
			}
			eLink := ""
			for _, l := range entry.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					eLink = l.Href
					break
				}
			}
			desc := entry.Summary
			if desc == "" {
				desc = entry.Content
			}
			result.Items = append(result.Items, feedItem{
				Title:       entry.Title,
				Link:        eLink,
				Description: truncate(stripHTMLTags(desc), maxFeedDescLen),
				PubDate:     entry.Updated,
			})
		}
		return result, nil
	}

	return nil, fmt.Errorf("response is not a valid RSS 2.0 or Atom feed")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// stripHTMLTags removes HTML markup from a string (for feed descriptions).
func stripHTMLTags(s string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			return strings.TrimSpace(b.String())
		}
		if tt == html.TextToken {
			b.WriteString(tokenizer.Token().Data)
		}
	}
}

func (t *MakeHTTPRequestTool) Summarize(result string) string {
	r, data, _, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if data == nil {
		return summarizeTruncate(result, 300)
	}
	status, _ := data["status_code"].(float64)
	body, _ := data["body"].(string)
	return fmt.Sprintf(`{"success":true,"summary":"HTTP %d (%d chars)"}`, int(status), len(body))
}

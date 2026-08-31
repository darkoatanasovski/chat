// Package opengraph fetches and scrapes OpenGraph metadata for a URL. It
// backs cmd/ogservice — a small standalone HTTP service (its own deploy unit,
// see deploy/docker-compose.yml's og-service) that cmd/api calls over HTTP
// for the "url_enrichment" channel capability instead of scraping pages
// itself (see cmd/api/link_preview.go). Splitting this out means the
// scraping/cache logic can be scaled, deployed, and rate-limited
// independently of the API's own request path, and a slow or misbehaving
// third-party host only ever affects this one service.
//
// No third-party HTML parser is used, in keeping with this codebase's
// deliberately minimal dependency set (see go.mod) — a handful of targeted
// regexes over the first maxFetchBytes of the response is good enough for
// "best-effort" OpenGraph metadata, the same standard url_enrichment has
// held itself to since it was first built inline in cmd/api.
package opengraph

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Data is one URL's scraped OpenGraph metadata. All fields except URL and
// FetchedAt are best-effort and may be empty if the page didn't have them
// (or didn't fetch at all — see Fetch's docs on what counts as an error
// versus a legitimately empty result).
type Data struct {
	URL         string    `json:"url"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	ImageURL    string    `json:"image_url,omitempty"`
	SiteName    string    `json:"site_name,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// Options bounds a single Fetch call. Zero values fall back to sane
// defaults (see Fetch) so callers that don't care can pass a zero Options.
type Options struct {
	// Timeout bounds the outbound request end-to-end (connect + read).
	// Callers should also pass a context with at least this much budget;
	// Fetch derives its own timeout from ctx via context.WithTimeout, so
	// whichever is shorter wins.
	Timeout time.Duration
	// MaxBytes caps how much of the response body Fetch reads before
	// giving up looking for tags — most OpenGraph/title tags appear in the
	// first few KB of well-formed HTML <head>, and this bounds memory and
	// time against a host that serves an enormous or infinite response.
	MaxBytes int64
	// UserAgent identifies this service to the fetched host. Empty falls
	// back to a default identifying this project.
	UserAgent string
}

const (
	defaultTimeout   = 5 * time.Second
	defaultMaxBytes  = 64 * 1024
	defaultUserAgent = "chat-opengraph-service/1.0 (+https://github.com/darkoatanasovski/chat)"
)

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = defaultMaxBytes
	}
	if o.UserAgent == "" {
		o.UserAgent = defaultUserAgent
	}
	return o
}

var (
	ogTitlePattern    = newMetaPropertyPattern("og:title")
	ogDescPattern     = newMetaPropertyPattern("og:description")
	ogImagePattern    = newMetaPropertyPattern("og:image")
	ogSiteNamePattern = newMetaPropertyPattern("og:site_name")
	nameDescPattern   = newMetaNamePattern("description")
	titleTagPattern   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlTagPattern    = regexp.MustCompile(`<[^>]*>`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// newMetaPropertyPattern matches <meta property="X" content="..."> in
// either attribute order (property before content, or content before
// property) — real-world pages aren't consistent about it.
func newMetaPropertyPattern(property string) *regexp.Regexp {
	p := regexp.QuoteMeta(property)
	return regexp.MustCompile(
		`(?is)<meta\s+(?:[^>]*?property=["']` + p + `["'][^>]*?content=["']([^"']*)["']` +
			`|[^>]*?content=["']([^"']*)["'][^>]*?property=["']` + p + `["'])`,
	)
}

func newMetaNamePattern(name string) *regexp.Regexp {
	n := regexp.QuoteMeta(name)
	return regexp.MustCompile(
		`(?is)<meta\s+(?:[^>]*?name=["']` + n + `["'][^>]*?content=["']([^"']*)["']` +
			`|[^>]*?content=["']([^"']*)["'][^>]*?name=["']` + n + `["'])`,
	)
}

func matchTag(pattern *regexp.Regexp, html string) string {
	m := pattern.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	// Two alternative capture groups (attribute order) — exactly one is
	// ever non-empty for a given match.
	if m[1] != "" {
		return cleanText(m[1])
	}
	return cleanText(m[2])
}

// Fetch GETs url and scrapes whatever OpenGraph metadata it can find.
//
// A transport-level failure (DNS, connection refused, timeout) or a
// non-2xx response returns a non-nil error — the caller (cmd/ogservice)
// treats that as "couldn't fetch," not "fetched fine, nothing there," and
// does not cache it. A successful fetch with no recognizable metadata at
// all returns a non-nil *Data with just URL and FetchedAt set, and a nil
// error — that IS worth caching for the same TTL, so a page that
// legitimately has no OpenGraph tags doesn't get re-fetched on every
// request for it within the hour.
func Fetch(ctx context.Context, url string, opts Options) (*Data, error) {
	opts = opts.withDefaults()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("opengraph: build request: %w", err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opengraph: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("opengraph: fetch: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes))
	if err != nil {
		return nil, fmt.Errorf("opengraph: read body: %w", err)
	}
	html := string(body)

	data := &Data{URL: url, FetchedAt: time.Now().UTC()}
	data.Title = matchTag(ogTitlePattern, html)
	if data.Title == "" {
		if m := titleTagPattern.FindStringSubmatch(html); len(m) == 2 {
			data.Title = cleanText(m[1])
		}
	}
	data.Description = matchTag(ogDescPattern, html)
	if data.Description == "" {
		data.Description = matchTag(nameDescPattern, html)
	}
	data.ImageURL = matchTag(ogImagePattern, html)
	data.SiteName = matchTag(ogSiteNamePattern, html)

	return data, nil
}

// cleanText strips any nested tags a match happened to pick up and trims/
// collapses whitespace — a lightweight substitute for the html.UnescapeString
// + real-parser treatment a dedicated HTML library would give this.
func cleanText(s string) string {
	s = htmlTagPattern.ReplaceAllString(s, "")
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(s, " "))
}

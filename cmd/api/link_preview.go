package main

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/messages"
)

// firstURLPattern is a deliberately simple best-effort URL extractor — good
// enough to find "the first http(s) link in a chat message," not a general
// URL grammar. url_enrichment is documented everywhere as best-effort
// (migrations/shard/0011's doc comment); a body with an unusual/malformed
// URL just doesn't get a preview, the same as a fetch that fails or times
// out.
var firstURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

// linkPreviewFetchTimeout bounds the outbound HTTP request enrichLinkPreview
// makes — generous relative to touchPresence's 3s (a real network fetch to
// an arbitrary third-party host, not a single local Postgres write) but
// still short enough that a slow/unresponsive host can't pile up goroutines
// under sustained send volume.
const linkPreviewFetchTimeout = 5 * time.Second

// maxLinkPreviewFetchBytes caps how much of a fetched page enrichLinkPreview
// reads before giving up looking for <title>/meta description — most of
// them appear in the first few KB of well-formed HTML, and this bounds
// memory/time against a host that serves an enormous or infinite response.
const maxLinkPreviewFetchBytes = 64 * 1024

var (
	titleTagPattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	metaDescPattern = regexp.MustCompile(`(?is)<meta\s+[^>]*name=["']description["'][^>]*content=["']([^"']*)["']`)
	htmlTagPattern  = regexp.MustCompile(`<[^>]*>`)
)

// enrichLinkPreview is a best-effort, fire-and-forget goroutine that fetches
// metadata for the first URL found in a freshly-sent message's body and
// stores it on messages.link_preview — the "url_enrichment" capability
// (migrations/shard/0011_channel_capabilities.sql). Mirrors touchPresence's
// shape (background context, bounded timeout, log-and-swallow on failure)
// but with a longer budget since this makes a real outbound HTTP request
// rather than a single local write. Never blocks or fails the send itself:
// handleSendMessage calls this only after Send has already committed and
// the response has been prepared.
func (a *App) enrichLinkPreview(pool *pgxpool.Pool, channelID, messageID uuid.UUID, body string) {
	url := firstURLPattern.FindString(body)
	if url == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), linkPreviewFetchTimeout)
		defer cancel()
		preview, err := fetchLinkPreview(ctx, url)
		if err != nil {
			a.log.Warn("link preview: fetch", "error", err, "url", url)
			return
		}
		if preview == nil {
			// Fetched fine, but the page had neither a <title> nor a
			// description meta tag worth storing — leave link_preview
			// null rather than writing an all-empty object.
			return
		}
		if err := a.messagesRepo.SetLinkPreview(ctx, pool, channelID, messageID, preview); err != nil {
			a.log.Warn("link preview: store", "error", err, "message_id", messageID)
		}
	}()
}

// fetchLinkPreview does the actual GET-and-scrape. No third-party HTML
// parser is used (this codebase's dependency set is deliberately minimal —
// see go.mod): a couple of targeted regexes over the first
// maxLinkPreviewFetchBytes is good enough for "best-effort," the same
// standard every other part of this feature holds itself to.
func fetchLinkPreview(ctx context.Context, url string) (*messages.LinkPreview, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chat-link-preview/1.0 (+https://github.com/darkoatanasovski/chat)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLinkPreviewFetchBytes))
	if err != nil {
		return nil, err
	}
	html := string(data)

	preview := &messages.LinkPreview{URL: url}
	if m := titleTagPattern.FindStringSubmatch(html); len(m) == 2 {
		preview.Title = cleanHTMLText(m[1])
	}
	if m := metaDescPattern.FindStringSubmatch(html); len(m) == 2 {
		preview.Description = cleanHTMLText(m[1])
	}
	if preview.Title == "" && preview.Description == "" {
		return nil, nil
	}
	return preview, nil
}

// cleanHTMLText strips any nested tags a title/meta-content match happened
// to pick up and trims surrounding whitespace — a lightweight substitute
// for the html.UnescapeString + real-parser treatment a dedicated HTML
// library would give this, in keeping with fetchLinkPreview's regex-based,
// best-effort approach.
func cleanHTMLText(s string) string {
	s = htmlTagPattern.ReplaceAllString(s, "")
	return collapseWhitespace(s)
}

var whitespacePattern = regexp.MustCompile(`\s+`)

func collapseWhitespace(s string) string {
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(s, " "))
}

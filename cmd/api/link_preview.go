package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// linkPreviewFetchTimeout bounds the whole round trip to the og-service
// (see cfg.OGServiceURL) — generous relative to touchPresence's 3s (a real
// network call, not a single local Postgres write) but still short enough
// that a slow/unresponsive og-service can't pile up goroutines under
// sustained send volume. og-service applies its own, shorter timeout to
// the actual third-party fetch; this is the outer budget for that call
// plus the HTTP round trip to reach it.
const linkPreviewFetchTimeout = 5 * time.Second

// enrichLinkPreview is a best-effort, fire-and-forget goroutine that asks
// the standalone og-service (cmd/ogservice) for metadata on the first URL
// found in a freshly-sent message's body and stores the result on
// messages.link_preview — the "url_enrichment" capability
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
		preview, err := fetchLinkPreview(ctx, a.cfg.OGServiceURL, url)
		if err != nil {
			a.log.Warn("link preview: fetch", "error", err, "url", url)
			return
		}
		if preview == nil {
			// og-service fetched the page fine but found neither a title
			// nor a description worth storing — leave link_preview null
			// rather than writing an all-empty object.
			return
		}
		if err := a.messagesRepo.SetLinkPreview(ctx, pool, channelID, messageID, preview); err != nil {
			a.log.Warn("link preview: store", "error", err, "message_id", messageID)
		}
	}()
}

// fetchLinkPreview asks ogServiceBaseURL's GET /og?url= for metadata on
// url and translates its response into a *messages.LinkPreview. All actual
// fetching/scraping happens in og-service (internal/opengraph) — this is
// just the HTTP client side of that call, kept in cmd/api so
// enrichLinkPreview's fire-and-forget/timeout/log-and-swallow shape stays
// exactly where the rest of that pattern (touchPresence, and formerly this
// same function's own inline scraping) already lives.
func fetchLinkPreview(ctx context.Context, ogServiceBaseURL, target string) (*messages.LinkPreview, error) {
	reqURL := ogServiceBaseURL + "/og?url=" + url.QueryEscape(target)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadGateway {
		// og-service reached out and the fetch itself failed (bad host,
		// non-2xx, timed out on its end) — same as this function
		// previously returning (nil, nil) for a failed direct fetch: not
		// worth surfacing as an application error, just no preview.
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("og-service: unexpected status %d", resp.StatusCode)
	}

	var data struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ImageURL    string `json:"image_url"`
		SiteName    string `json:"site_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("og-service: decode response: %w", err)
	}

	if data.Title == "" && data.Description == "" && data.ImageURL == "" && data.SiteName == "" {
		return nil, nil
	}
	return &messages.LinkPreview{
		URL:         target,
		Title:       data.Title,
		Description: data.Description,
		ImageURL:    data.ImageURL,
		SiteName:    data.SiteName,
	}, nil
}

// Package translations calls a third-party machine-translation provider to
// translate a message body on demand (POST
// /channels/{id}/messages/{message_id}/translate, gated by the app's
// "translations" channel capability — see internal/apps.ChannelCapabilities)
// and tracks the cost/usage that request incurs.
//
// The provider is Azure (Microsoft) Translator Text API v3.0: as of this
// package's writing it was both the cheapest of the major NMT providers
// ($10 per 1M characters, vs. Google Cloud Translation's $20/1M and DeepL
// API Pro's $25/1M, plus a permanent 2M-characters/month free tier) and the
// fastest (sub-100ms median response, vs. DeepL's ~1s and Google's
// in-between), which matters here because a translation request sits
// directly in an end-user-facing HTTP response, not a background job.
//
// This package is split the same way internal/opengraph's Fetch and
// cmd/api's link-preview caching are split: Client only ever talks to
// Azure and knows nothing about Postgres; Repo (cache.go) is the
// shard-local cache of a translation result, keyed like every other
// message sub-resource (internal/reactions, internal/polls) by
// (channel_id, message_id); UsageRepo (usage.go) is the control-plane
// ledger a translation request writes to regardless of whether it was a
// cache hit, so an org's translation spend can be read back without a
// scatter-gather over every physical shard the way message counts are
// (cmd/api's dashboardMessagesFor) — translation requests are user-driven
// and comparatively rare, not a hot per-message write, so this is the same
// "billing/usage state lives in the control plane" choice
// cmd/api/handlers_billing.go already makes for Dodo.
package translations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotConfigured is returned by Translate when no provider key is set —
// the caller (cmd/api's handleTranslateMessage) reports this as a 503, the
// same "empty key is a valid state, not a startup failure" treatment
// cmd/api/handlers_billing.go gives an unconfigured Dodo integration.
var ErrNotConfigured = errors.New("translations: provider is not configured")

// CostMicrosPerChar is Azure Translator's standard-tier price ($10 per
// 1,000,000 characters) expressed as USD micros (1,000,000 micros = $1) per
// character, so per-request cost can be tracked as an exact integer rather
// than a floating-point dollar amount. This is an estimate for usage
// tracking/display purposes only (see internal/translations/usage.go) —
// this platform does not itself charge a card per translation; see
// UsageRepo's doc comment.
const CostMicrosPerChar = 10

// Config configures Client. Empty Key is valid — NewClient still returns a
// usable *Client, and every Translate call on it returns ErrNotConfigured
// immediately without making a network call.
type Config struct {
	Key      string
	Region   string
	Endpoint string
	// Timeout bounds a single Translate call end-to-end. Zero falls back
	// to defaultTimeout.
	Timeout time.Duration
}

const (
	defaultTimeout    = 5 * time.Second
	defaultEndpoint   = "https://api.cognitive.microsofttranslator.com"
	apiVersion        = "3.0"
	maxTranslateChars = 10000
)

type Client struct {
	cfg    Config
	client *http.Client
}

func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

// Configured reports whether a provider key is set — cmd/api checks this
// up front so a translate request against an unconfigured deployment fails
// fast with a clear 503 instead of an opaque provider error.
func (c *Client) Configured() bool {
	return c.cfg.Key != ""
}

// Result is one successful translation.
type Result struct {
	TranslatedText string
	// DetectedSourceLang is Azure's own language detection when the caller
	// didn't pass an explicit source language — always populated by Azure
	// regardless, so this is always set.
	DetectedSourceLang string
}

type translateRequestItem struct {
	Text string `json:"Text"`
}

type translateResponseItem struct {
	DetectedLanguage struct {
		Language string  `json:"language"`
		Score    float64 `json:"score"`
	} `json:"detectedLanguage"`
	Translations []struct {
		Text string `json:"text"`
		To   string `json:"to"`
	} `json:"translations"`
}

type azureErrorBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Translate sends text to Azure Translator and returns the translation to
// targetLang. sourceLang is optional ("" lets Azure auto-detect it, which
// is also reflected back in Result.DetectedSourceLang).
//
// text is capped at maxTranslateChars before this ever reaches the network
// — the caller (cmd/api's handleTranslateMessage) is expected to have
// already bounded it via the app's max_message_length, but this is a
// second, provider-facing bound so a pathological input can't turn into an
// unexpectedly large bill.
func (c *Client) Translate(ctx context.Context, text, targetLang, sourceLang string) (Result, error) {
	if !c.Configured() {
		return Result{}, ErrNotConfigured
	}
	if len(text) > maxTranslateChars {
		return Result{}, fmt.Errorf("translations: text exceeds %d characters", maxTranslateChars)
	}

	body, err := json.Marshal([]translateRequestItem{{Text: text}})
	if err != nil {
		return Result{}, fmt.Errorf("translations: encode request: %w", err)
	}

	url := fmt.Sprintf("%s/translate?api-version=%s&to=%s", c.cfg.Endpoint, apiVersion, targetLang)
	if sourceLang != "" {
		url += "&from=" + sourceLang
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("translations: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Ocp-Apim-Subscription-Key", c.cfg.Key)
	if c.cfg.Region != "" {
		req.Header.Set("Ocp-Apim-Subscription-Region", c.cfg.Region)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("translations: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("translations: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var azErr azureErrorBody
		if json.Unmarshal(respBody, &azErr) == nil && azErr.Error.Message != "" {
			return Result{}, fmt.Errorf("translations: provider error (%d): %s", resp.StatusCode, azErr.Error.Message)
		}
		return Result{}, fmt.Errorf("translations: provider returned status %d", resp.StatusCode)
	}

	var items []translateResponseItem
	if err := json.Unmarshal(respBody, &items); err != nil {
		return Result{}, fmt.Errorf("translations: decode response: %w", err)
	}
	if len(items) == 0 || len(items[0].Translations) == 0 {
		return Result{}, fmt.Errorf("translations: empty response from provider")
	}

	return Result{
		TranslatedText:     items[0].Translations[0].Text,
		DetectedSourceLang: items[0].DetectedLanguage.Language,
	}, nil
}

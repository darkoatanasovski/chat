package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// verifyTurnstile validates a Cloudflare Turnstile token server-side
// (https://challenges.cloudflare.com/turnstile/v0/siteverify). Enabled only
// when TURNSTILE_SECRET is set — see handleDashboardSignup. Any error or a
// non-success verdict returns false (fail closed on a configured check).
func verifyTurnstile(ctx context.Context, secret, token, remoteIP string) bool {
	if token == "" {
		return false
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	return out.Success
}

// clientIP extracts the caller's IP for Turnstile's remoteip check, honoring
// CF-Connecting-IP (set by Cloudflare) then X-Forwarded-For.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return ""
}

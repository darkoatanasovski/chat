package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

// forwardToHomeRegion relays the original request to the api instance
// authoritative for this channel (INSTRUCTIONS.md §5/§27): a channel has
// exactly one writer region, so a request landing anywhere else is proxied
// there rather than writing locally. The peer runs the same handler and
// applies the write directly since it *is* the home region.
func (a *App) forwardToHomeRegion(w http.ResponseWriter, r *http.Request, homeRegion string, body []byte) {
	peerURL, ok := a.cfg.PeerAPIURL[homeRegion]
	if !ok || peerURL == "" {
		writeError(w, http.StatusInternalServerError, "no route to home region "+homeRegion)
		return
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, peerURL+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build forwarded request")
		return
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-From-Region", a.cfg.Region)

	resp, err := a.peerClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("home region %s unreachable", homeRegion))
		return
	}
	defer resp.Body.Close()

	a.metrics.CrossRegionLatency.WithLabelValues(a.cfg.Region, homeRegion).Observe(time.Since(start).Seconds())

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

package api

import (
	"net/http"
	"strconv"
)

// handlePlacementLookup is the control plane's read-through source for the edge
// router (Cloudflare Worker / cmd/router): given an api_key or app_id, it
// returns that app's {region, shard} placement so the edge can populate its KV
// cache on a miss. Gated by X-Internal-Key when INTERNAL_AUTH_KEY is set.
//
// GET /internal/placement?api_key=... | ?app_id=...
func (a *App) handlePlacementLookup(w http.ResponseWriter, r *http.Request) {
	if a.cfg.InternalAuthKey != "" && r.Header.Get("X-Internal-Key") != a.cfg.InternalAuthKey {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var region, shard *string
	if key := r.URL.Query().Get("api_key"); key != "" {
		row := a.configPool.QueryRow(r.Context(), `
			SELECT a.region, a.shard
			FROM app_credentials c JOIN apps a ON a.app_id = c.app_id
			WHERE c.key = $1 AND c.revoked_at IS NULL`, key)
		if err := row.Scan(&region, &shard); err != nil {
			writeError(w, http.StatusNotFound, "unknown api_key")
			return
		}
	} else if appIDStr := r.URL.Query().Get("app_id"); appIDStr != "" {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid app_id")
			return
		}
		row := a.configPool.QueryRow(r.Context(), `SELECT region, shard FROM apps WHERE app_id = $1`, appID)
		if err := row.Scan(&region, &shard); err != nil {
			writeError(w, http.StatusNotFound, "unknown app_id")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "api_key or app_id required")
		return
	}

	if region == nil || shard == nil || *region == "" || *shard == "" {
		writeError(w, http.StatusNotFound, "app not placed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"region": *region, "shard": *shard})
}

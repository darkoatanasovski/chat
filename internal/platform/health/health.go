// Package health exposes a /healthz endpoint that runs a set of dependency
// checks (Postgres, Redis, Kafka) with a bounded timeout, so orchestration
// (or a human) can tell a service apart from its dependencies being down.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Checker func(ctx context.Context) error

func Handler(checks map[string]Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		results := make(map[string]string, len(checks))
		ok := true
		for name, check := range checks {
			if err := check(ctx); err != nil {
				results[name] = err.Error()
				ok = false
				continue
			}
			results[name] = "ok"
		}

		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "checks": results})
	}
}

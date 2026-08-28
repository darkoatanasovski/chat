// Package debug mounts Go's runtime profiler (net/http/pprof) on a
// service's own mux rather than the process-global http.DefaultServeMux —
// registering pprof only via its side-effecting import would silently do
// nothing for a service that never serves DefaultServeMux, and would be
// easy to mistake for "pprof is available" when it isn't.
//
// Every service already exposes an unauthenticated /metrics on
// cfg.MetricsAddr, reachable only from inside the deployment's own network
// (see deploy/docker-compose.yml — MetricsAddr is not published to the host
// except for the worker instances' own scrape ports). Mounting pprof
// alongside /metrics keeps it on that same trust boundary rather than
// opening a new one.
package debug

import (
	"net/http"
	"net/http/pprof"
)

// Mount registers the standard pprof endpoints under /debug/pprof/ on mux:
// profile (30s CPU sample by default), heap/goroutine/block/mutex/allocs
// (via the index), cmdline, symbol, and trace. Use like:
//
//	go tool pprof http://<metrics-addr>/debug/pprof/profile
//	go tool pprof http://<metrics-addr>/debug/pprof/heap
func Mount(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

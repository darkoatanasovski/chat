// Command chat is the single platform binary. One build, one image; the
// role it runs is chosen by the first argument:
//
//	chat api      REST data plane for one cell (was cmd/api)
//	chat control  global org/dashboard/billing control plane
//	chat ws       WebSocket edge for one cell (was cmd/gateway)
//	chat worker   transactional-outbox publisher for one cell (was cmd/worker)
//	chat router    apikey -> cell edge router (see cmd/router)
//	chat og       standalone OpenGraph link-preview scraper (was cmd/ogservice)
//
// This is the "monolith app with a few apps" shape from
// docs/adr/0006-cell-based-tenant-routing.md: all roles share the same
// internal/ packages and the same config.Load(); only the entrypoint differs.
package main

import (
	"fmt"
	"os"

	"github.com/darkoatanasovski/chat/cmd/api"
	"github.com/darkoatanasovski/chat/cmd/ogservice"
	"github.com/darkoatanasovski/chat/cmd/router"
	"github.com/darkoatanasovski/chat/cmd/worker"
	"github.com/darkoatanasovski/chat/cmd/ws"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	role := os.Args[1]
	// Drop the role argument so each Run() sees a conventional argv (the
	// program name followed by any role-specific flags), not the role verb.
	os.Args = append(os.Args[:1], os.Args[2:]...)

	switch role {
	case "api":
		api.Run()
	case "control":
		api.RunControl()
	case "ws":
		ws.Run()
	case "worker":
		worker.Run()
	case "router":
		router.Run()
	case "og":
		ogservice.Run()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "chat: unknown role %q\n\n", role)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `chat — the platform binary

Usage:
  chat <role> [flags]

Roles:
  api      REST data plane for one cell
  control  global org/dashboard/billing control plane
  ws       WebSocket edge for one cell
  worker   transactional-outbox publisher for one cell
  router   apikey -> cell edge router
  og       OpenGraph link-preview scraper

Configuration is via environment variables (see internal/platform/config).
`)
}

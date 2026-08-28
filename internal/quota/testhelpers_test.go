package quota

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testRedis returns a client for the local Valkey instance used by the
// docker-compose dev stack (deploy/docker-compose.yml), skipping the test if
// it isn't reachable rather than failing the whole suite in an environment
// where the stack isn't up.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("valkey not reachable at %s (start the stack: make up): %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

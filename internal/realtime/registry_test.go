package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRegistry_RegisterWritesSetAndHashWithTTL(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()
	userID := uuid.New()
	connID := uuid.NewString()
	t.Cleanup(func() { _ = reg.Unregister(ctx, userID, connID) })

	if err := reg.Register(ctx, userID, connID, "eu", "eu-gateway"); err != nil {
		t.Fatalf("register: %v", err)
	}

	members, err := client.SMembers(ctx, userSetKey(userID)).Result()
	if err != nil {
		t.Fatalf("smembers: %v", err)
	}
	if len(members) != 1 || members[0] != connID {
		t.Fatalf("expected user's connection set to contain %q, got %v", connID, members)
	}

	fields, err := client.HGetAll(ctx, connectionKey(userID, connID)).Result()
	if err != nil {
		t.Fatalf("hgetall: %v", err)
	}
	if fields["region"] != "eu" || fields["gateway_id"] != "eu-gateway" {
		t.Fatalf("unexpected connection hash fields: %+v", fields)
	}

	ttl, err := client.TTL(ctx, connectionKey(userID, connID)).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("expected a positive TTL on the connection entry so a crashed gateway's entries expire, got %v", ttl)
	}
}

func TestRegistry_MultipleConnectionsPerUser(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()
	userID := uuid.New()
	connA := uuid.NewString()
	connB := uuid.NewString()
	t.Cleanup(func() {
		_ = reg.Unregister(ctx, userID, connA)
		_ = reg.Unregister(ctx, userID, connB)
	})

	if err := reg.Register(ctx, userID, connA, "eu", "eu-gateway"); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := reg.Register(ctx, userID, connB, "us", "us-gateway"); err != nil {
		t.Fatalf("register B: %v", err)
	}

	members, err := client.SMembers(ctx, userSetKey(userID)).Result()
	if err != nil {
		t.Fatalf("smembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 connections registered for a multi-device user, got %d: %v", len(members), members)
	}

	if err := reg.Unregister(ctx, userID, connA); err != nil {
		t.Fatalf("unregister A: %v", err)
	}
	members, err = client.SMembers(ctx, userSetKey(userID)).Result()
	if err != nil {
		t.Fatalf("smembers after unregister: %v", err)
	}
	if len(members) != 1 || members[0] != connB {
		t.Fatalf("expected only connB to remain after unregistering connA, got %v", members)
	}
	if exists, _ := client.Exists(ctx, connectionKey(userID, connA)).Result(); exists != 0 {
		t.Fatalf("expected connA's hash entry to be deleted on unregister")
	}
}

func TestRegistry_Heartbeat_RefreshesTTL(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()
	userID := uuid.New()
	connID := uuid.NewString()
	t.Cleanup(func() { _ = reg.Unregister(ctx, userID, connID) })

	if err := reg.Register(ctx, userID, connID, "eu", "eu-gateway"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Artificially shrink the TTL so a heartbeat's refresh is observable.
	if err := client.Expire(ctx, connectionKey(userID, connID), 1*time.Second).Err(); err != nil {
		t.Fatalf("shrink ttl: %v", err)
	}

	if err := reg.Heartbeat(ctx, userID, connID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	ttl, err := client.TTL(ctx, connectionKey(userID, connID)).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 1 {
		t.Fatalf("expected heartbeat to extend the TTL well past 1s, got %v", ttl)
	}
}

func TestRegistry_Unregister_UnknownConnectionIsNoop(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()
	if err := reg.Unregister(ctx, uuid.New(), uuid.NewString()); err != nil {
		t.Fatalf("unregistering a connection that was never registered should not error: %v", err)
	}
}

func TestRegistry_GatewaysForUsers_Empty(t *testing.T) {
	reg := NewRegistry(testRedis(t))
	out, err := reg.GatewaysForUsers(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected an empty result for no input users, got %v", out)
	}
}

func TestRegistry_GatewaysForUsers_MixOfConnectedAndNot(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()

	connected := uuid.New()
	notConnected := uuid.New()
	connID := uuid.NewString()
	if err := reg.Register(ctx, connected, connID, "eu", "eu-gw-1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Unregister(ctx, connected, connID) })

	out, err := reg.GatewaysForUsers(ctx, []uuid.UUID{connected, notConnected})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out[connected]; len(got) != 1 || got[0] != "eu-gw-1" {
		t.Fatalf("expected connected user mapped to [eu-gw-1], got %v", got)
	}
	if _, ok := out[notConnected]; ok {
		t.Fatalf("expected no entry for a user with no live connection, got %v", out[notConnected])
	}
}

func TestRegistry_GatewaysForUsers_MultipleDevicesOnDifferentGateways(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()

	userID := uuid.New()
	connA, connB := uuid.NewString(), uuid.NewString()
	if err := reg.Register(ctx, userID, connA, "eu", "eu-gw-1"); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := reg.Register(ctx, userID, connB, "eu", "eu-gw-2"); err != nil {
		t.Fatalf("register B: %v", err)
	}
	t.Cleanup(func() {
		_ = reg.Unregister(ctx, userID, connA)
		_ = reg.Unregister(ctx, userID, connB)
	})

	out, err := reg.GatewaysForUsers(ctx, []uuid.UUID{userID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gateways := out[userID]
	if len(gateways) != 2 {
		t.Fatalf("expected a device on each of 2 gateways, got %v", gateways)
	}
	seen := map[string]bool{}
	for _, g := range gateways {
		seen[g] = true
	}
	if !seen["eu-gw-1"] || !seen["eu-gw-2"] {
		t.Fatalf("expected both eu-gw-1 and eu-gw-2, got %v", gateways)
	}
}

func TestRegistry_GatewaysForUsers_DedupesSameGateway(t *testing.T) {
	client := testRedis(t)
	reg := NewRegistry(client)
	ctx := context.Background()

	userID := uuid.New()
	connA, connB := uuid.NewString(), uuid.NewString()
	// Two tabs/devices, same gateway instance — should collapse to one
	// entry so Fanout sends this gateway one push, not two.
	if err := reg.Register(ctx, userID, connA, "eu", "eu-gw-1"); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := reg.Register(ctx, userID, connB, "eu", "eu-gw-1"); err != nil {
		t.Fatalf("register B: %v", err)
	}
	t.Cleanup(func() {
		_ = reg.Unregister(ctx, userID, connA)
		_ = reg.Unregister(ctx, userID, connB)
	})

	out, err := reg.GatewaysForUsers(ctx, []uuid.UUID{userID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out[userID]; len(got) != 1 || got[0] != "eu-gw-1" {
		t.Fatalf("expected deduped [eu-gw-1], got %v", got)
	}
}

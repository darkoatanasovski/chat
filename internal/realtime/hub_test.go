package realtime

import (
	"testing"

	"github.com/google/uuid"
)

func TestHub_RegisterAndHasLocalUser(t *testing.T) {
	hub := NewHub(nil)
	userA := uuid.New()
	userB := uuid.New()

	if hub.HasLocalUser(userA) {
		t.Fatalf("expected no local user before registration")
	}

	hub.Register(userA, "eu")

	if !hub.HasLocalUser(userA) {
		t.Fatalf("expected userA to be recognized as local after registration")
	}
	if hub.HasLocalUser(userB) {
		t.Fatalf("expected userB to still be non-local")
	}
}

func TestHub_DeliverToUser_FansOutToEveryDevice(t *testing.T) {
	hub := NewHub(nil)
	userID := uuid.New()

	// A user with multiple devices/tabs open gets multiple connections
	// (INSTRUCTIONS.md §21) — every one of them must receive a delivery.
	connA := hub.Register(userID, "eu")
	connB := hub.Register(userID, "eu")
	connC := hub.Register(userID, "eu")

	hub.DeliverToUser(userID, []byte("payload"))

	for name, c := range map[string]*Connection{"A": connA, "B": connB, "C": connC} {
		select {
		case got := <-c.send:
			if string(got) != "payload" {
				t.Fatalf("connection %s: got %q, want %q", name, got, "payload")
			}
		default:
			t.Fatalf("connection %s: expected a delivered payload, buffer was empty", name)
		}
	}
}

func TestHub_DeliverToUser_UnknownUserIsNoop(t *testing.T) {
	hub := NewHub(nil)
	// Delivering to a user with no local connections must not panic or error.
	hub.DeliverToUser(uuid.New(), []byte("payload"))
}

func TestHub_Unregister_RemovesConnectionAndClosesSend(t *testing.T) {
	hub := NewHub(nil)
	userID := uuid.New()
	conn := hub.Register(userID, "eu")

	hub.Unregister(conn)

	if hub.HasLocalUser(userID) {
		t.Fatalf("expected user to no longer be local after its only connection unregisters")
	}
	if _, open := <-conn.send; open {
		t.Fatalf("expected the connection's send channel to be closed after unregister")
	}
}

func TestHub_Unregister_OneOfMultipleConnectionsKeepsUserLocal(t *testing.T) {
	hub := NewHub(nil)
	userID := uuid.New()
	connA := hub.Register(userID, "eu")
	hub.Register(userID, "eu")

	hub.Unregister(connA)

	if !hub.HasLocalUser(userID) {
		t.Fatalf("expected user to still be local: one of two connections remains")
	}
	if hub.ActiveConnections() != 1 {
		t.Fatalf("expected 1 active connection remaining, got %d", hub.ActiveConnections())
	}
}

func TestHub_DeliverToUser_EvictsOnBackpressure(t *testing.T) {
	var evictedReason string
	evictedCh := make(chan struct{}, 1)
	hub := NewHub(func(c *Connection, reason string) {
		evictedReason = reason
		evictedCh <- struct{}{}
	})
	userID := uuid.New()
	conn := hub.Register(userID, "eu")

	// outboundBufferSize is 256; fill it, then one more delivery must evict
	// rather than block (INSTRUCTIONS.md §29 — a slow consumer is
	// disconnected, never allowed to grow the queue unbounded).
	for range outboundBufferSize {
		hub.DeliverToUser(userID, []byte("x"))
	}
	if hub.ActiveConnections() != 1 {
		t.Fatalf("connection should still be registered while under its buffer limit")
	}

	hub.DeliverToUser(userID, []byte("overflow"))

	select {
	case <-evictedCh:
	default:
		t.Fatalf("expected the eviction callback to fire once the buffer overflows")
	}
	if evictedReason != "backpressure" {
		t.Fatalf("expected eviction reason %q, got %q", "backpressure", evictedReason)
	}
	if hub.ActiveConnections() != 0 {
		t.Fatalf("expected the overflowing connection to be unregistered, got %d active", hub.ActiveConnections())
	}
	// Drain the buffered messages first — a closed channel only reports
	// open=false once its buffer is empty.
	drained := 0
	for range conn.send {
		drained++
	}
	if drained != outboundBufferSize {
		t.Fatalf("expected %d buffered messages before closure, drained %d", outboundBufferSize, drained)
	}
}

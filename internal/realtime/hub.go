// Package realtime holds everything a gateway process needs to accept
// WebSocket connections and fan out durable events to them. Nothing here is
// itself durable (INSTRUCTIONS.md §18): the Hub only tracks connections
// local to this process; Redis (registry.go, membership_cache.go) holds the
// cross-process, ephemeral/distributed state.
package realtime

import (
	"sync"

	"github.com/google/uuid"
)

// outboundBufferSize bounds every connection's outbound queue
// (INSTRUCTIONS.md §29). A connection that can't keep up gets disconnected
// rather than allowed to grow this queue without bound.
const outboundBufferSize = 256

type Connection struct {
	ID     string
	UserID uuid.UUID
	Region string

	send chan []byte
	once sync.Once
}

func newConnection(userID uuid.UUID, region string) *Connection {
	return &Connection{
		ID:     uuid.NewString(),
		UserID: userID,
		Region: region,
		send:   make(chan []byte, outboundBufferSize),
	}
}

// Enqueue attempts a non-blocking send. It reports false if the connection's
// outbound buffer is full, signalling the caller to disconnect this
// connection (the client will resynchronize via history APIs on reconnect).
func (c *Connection) Enqueue(payload []byte) bool {
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (c *Connection) closeSend() {
	c.once.Do(func() { close(c.send) })
}

// Hub tracks WebSocket connections local to this gateway process, indexed by
// user so fanout can find "does this process have any of channel X's
// members connected right now?" without a lock per lookup being too coarse.
type Hub struct {
	mu      sync.RWMutex
	byUser  map[uuid.UUID]map[string]*Connection // userID -> connID -> conn
	onEvict func(*Connection, string)
}

func NewHub(onEvict func(conn *Connection, reason string)) *Hub {
	return &Hub{
		byUser:  make(map[uuid.UUID]map[string]*Connection),
		onEvict: onEvict,
	}
}

func (h *Hub) Register(userID uuid.UUID, region string) *Connection {
	c := newConnection(userID, region)
	h.mu.Lock()
	if h.byUser[userID] == nil {
		h.byUser[userID] = make(map[string]*Connection)
	}
	h.byUser[userID][c.ID] = c
	h.mu.Unlock()
	return c
}

func (h *Hub) Unregister(c *Connection) {
	h.mu.Lock()
	if conns, ok := h.byUser[c.UserID]; ok {
		delete(conns, c.ID)
		if len(conns) == 0 {
			delete(h.byUser, c.UserID)
		}
	}
	h.mu.Unlock()
	c.closeSend()
}

// DeliverToUser pushes payload to every locally-connected socket for
// userID. Connections whose buffer is full are disconnected rather than
// blocked on (§29).
func (h *Hub) DeliverToUser(userID uuid.UUID, payload []byte) {
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.byUser[userID]))
	for _, c := range h.byUser[userID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	for _, c := range conns {
		if !c.Enqueue(payload) {
			h.Unregister(c)
			if h.onEvict != nil {
				h.onEvict(c, "backpressure")
			}
		}
	}
}

// HasLocalUser reports whether userID has a connection on this gateway
// instance — the fanout consumer uses this to decide, per member, whether
// to deliver directly (Hub) or route through Registry + Publisher to
// whichever instance actually holds that member's connection.
func (h *Hub) HasLocalUser(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byUser[userID]) > 0
}

func (h *Hub) ActiveConnections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, conns := range h.byUser {
		total += len(conns)
	}
	return total
}

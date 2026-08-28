package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/metrics"
)

const (
	writeWait    = 10 * time.Second
	pongWait     = 60 * time.Second
	pingInterval = 30 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Local multi-region demo: every gateway origin is trusted.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ConnectHandler upgrades to a WebSocket after verifying the bearer token
// carried on the query string (browsers cannot set arbitrary headers on the
// WebSocket handshake). It never trusts a client-asserted user_id/region —
// both come only from the verified token (INSTRUCTIONS.md §43).
type ConnectHandler struct {
	signer    *auth.Signer
	hub       *Hub
	registry  *Registry
	delivery  *Delivery
	gatewayID string
	metrics   *metrics.Metrics
	log       *slog.Logger
}

func NewConnectHandler(signer *auth.Signer, hub *Hub, registry *Registry, delivery *Delivery, gatewayID string, m *metrics.Metrics, log *slog.Logger) *ConnectHandler {
	return &ConnectHandler{signer: signer, hub: hub, registry: registry, delivery: delivery, gatewayID: gatewayID, metrics: m, log: log}
}

func (h *ConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	claims, err := h.signer.Verify(token)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid token subject", http.StatusUnauthorized)
		return
	}

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("websocket upgrade failed", "error", err)
		return
	}

	conn := h.hub.Register(userID, claims.Region)
	if h.metrics != nil {
		h.metrics.WebSocketConnectionsActive.Inc()
	}
	ctx, cancel := context.WithCancel(r.Context())

	if err := h.registry.Register(ctx, userID, conn.ID, claims.Region, h.gatewayID); err != nil {
		h.log.Warn("registry register failed", "error", err)
	}

	cleanup := func(reason string) {
		cancel()
		h.hub.Unregister(conn)
		_ = h.registry.Unregister(context.Background(), userID, conn.ID)
		_ = wsConn.Close()
		if h.metrics != nil {
			h.metrics.WebSocketConnectionsActive.Dec()
			h.metrics.WebSocketDisconnectsTotal.WithLabelValues(reason).Inc()
		}
	}

	go h.writePump(ctx, wsConn, conn, cleanup)
	h.readPump(ctx, wsConn, userID, cleanup)
}

// writePump drains the connection's outbound buffer to the socket and sends
// periodic pings. WebSocket delivery is inherently best-effort
// (INSTRUCTIONS.md §18) — there is no retry here by design.
func (h *ConnectHandler) writePump(ctx context.Context, wsConn *websocket.Conn, conn *Connection, cleanup func(reason string)) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-conn.send:
			if !ok {
				return
			}
			wsConn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := wsConn.WriteMessage(websocket.TextMessage, payload); err != nil {
				cleanup("write_error")
				return
			}
		case <-ticker.C:
			wsConn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := wsConn.WriteMessage(websocket.PingMessage, nil); err != nil {
				cleanup("ping_failed")
				return
			}
		}
	}
}

// TypingFrame is the JSON payload pushed to WebSocket clients for
// typing.updated — deliberately not modeled in internal/events, since it's
// never written to the outbox or Kafka (see relayTyping).
type TypingFrame struct {
	Type      string    `json:"type"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
	Typing    bool      `json:"typing"`
}

// inboundFrame is the one thing a client can send up the socket today:
// typing presence. Everything durable (messages, reactions, read receipts)
// still goes through the HTTP API, never this connection — only an
// ephemeral, no-persistence signal belongs on the realtime path itself.
type inboundFrame struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id"`
}

// readPump detects liveness/close and now also accepts typing.start/
// typing.stop frames — everything else a client sends (messages, reactions,
// read receipts) still goes through the HTTP API, never this connection.
func (h *ConnectHandler) readPump(ctx context.Context, wsConn *websocket.Conn, userID uuid.UUID, cleanup func(reason string)) {
	wsConn.SetReadDeadline(time.Now().Add(pongWait))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	defer cleanup("client_disconnected")
	for {
		_, data, err := wsConn.ReadMessage()
		if err != nil {
			return
		}
		h.handleInbound(ctx, userID, data)
	}
}

// handleInbound is best-effort by design: a malformed or unrecognized frame
// is simply ignored, not a reason to drop the connection — the same
// tolerance readPump already had for anything it didn't expect.
func (h *ConnectHandler) handleInbound(ctx context.Context, userID uuid.UUID, data []byte) {
	var frame inboundFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return
	}
	channelID, err := uuid.Parse(frame.ChannelID)
	if err != nil {
		return
	}
	switch frame.Type {
	case "typing.start":
		h.relayTyping(ctx, userID, channelID, true)
	case "typing.stop":
		h.relayTyping(ctx, userID, channelID, false)
	}
}

// relayTyping never touches Postgres or Kafka — typing presence has no
// durability requirement (a missed frame just means the indicator doesn't
// show, exactly as safe as it not being sent), so it's relayed directly
// through the same Delivery local/remote resolution Fanout uses, skipping
// the outbox entirely. Membership is verified fresh (never trusted from the
// client) so a client can't broadcast typing into a channel it doesn't
// belong to (INSTRUCTIONS.md §43).
func (h *ConnectHandler) relayTyping(ctx context.Context, userID, channelID uuid.UUID, typing bool) {
	isMember, err := h.delivery.IsMember(ctx, channelID, userID)
	if err != nil {
		h.log.Warn("typing: check membership", "error", err)
		return
	}
	if !isMember {
		return
	}

	frame, err := json.Marshal(TypingFrame{
		Type:      "typing.updated",
		ChannelID: channelID,
		UserID:    userID,
		Typing:    typing,
	})
	if err != nil {
		h.log.Warn("typing: marshal frame", "error", err)
		return
	}
	if err := h.delivery.ToChannelMembers(ctx, channelID, frame, userID); err != nil {
		h.log.Warn("typing: relay", "error", err)
	}
}

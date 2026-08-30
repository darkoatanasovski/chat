package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/darkoatanasovski/chat/internal/apps"
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

// PresenceToucher marks a user active right now — implemented by
// internal/users.Service.TouchActivity, injected here rather than this
// package importing internal/users directly, matching how ConnectHandler
// already takes hub/registry/delivery as narrow collaborators rather than
// owning account storage itself. Nil-safe: a nil PresenceToucher (e.g. in
// tests that don't care about presence) simply skips every touch.
type PresenceToucher interface {
	TouchActivity(ctx context.Context, userID uuid.UUID) error
}

// CapabilitiesResolver resolves the live "Channel Capabilities" toggle set
// for an app — implemented by internal/apps.Repo.ChannelCapabilities,
// injected the same narrow-interface way PresenceToucher is (this package
// doesn't otherwise need internal/apps.Repo's full surface, just the
// ChannelCapabilities type it returns). Read fresh on every gated inbound
// frame and every connect/disconnect — never cached, matching every other
// per-app setting's discipline in this codebase. A nil resolver (e.g. tests
// that don't care about capability gating) is handled by capabilityEnabled
// treating every capability as on, preserving pre-capability behavior.
type CapabilitiesResolver interface {
	ChannelCapabilities(ctx context.Context, appID int64) (apps.ChannelCapabilities, error)
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
	// presence marks this connection's user active on connect, on every
	// heartbeat pong (~pingInterval cadence, so a connected-but-idle user
	// still reads as online), and once more on disconnect (so
	// last_active_at freezes at the true last-seen instant). See
	// internal/users.OnlineWindow for how that recency becomes is_online.
	presence PresenceToucher
	// capabilities backs the "typing_events" gate on relayTyping and the
	// "connection_events" gate on relayConnectionEvent — see
	// CapabilitiesResolver's doc comment.
	capabilities CapabilitiesResolver
}

func NewConnectHandler(signer *auth.Signer, hub *Hub, registry *Registry, delivery *Delivery, gatewayID string, m *metrics.Metrics, log *slog.Logger, presence PresenceToucher, capabilities CapabilitiesResolver) *ConnectHandler {
	return &ConnectHandler{signer: signer, hub: hub, registry: registry, delivery: delivery, gatewayID: gatewayID, metrics: m, log: log, presence: presence, capabilities: capabilities}
}

// capabilityEnabled reports whether extract(app's live ChannelCapabilities)
// is true. A nil resolver (tests) or a failed resolve both default to
// enabled — the pre-capability behavior — rather than silently dropping
// every typing/connection frame because of a transient control-plane read;
// contrast with cmd/api's REST handlers, which do treat a failed app load
// as a hard error, since a client there gets a real HTTP error status to
// react to and retry — a WebSocket frame has no equivalent "tell the caller
// it failed" channel worth adding for this.
func (h *ConnectHandler) capabilityEnabled(ctx context.Context, appID int64, extract func(apps.ChannelCapabilities) bool) bool {
	if h.capabilities == nil {
		return true
	}
	caps, err := h.capabilities.ChannelCapabilities(ctx, appID)
	if err != nil {
		h.log.Warn("capability check: resolve", "error", err, "app_id", appID)
		return true
	}
	return extract(caps)
}

// touchPresence is best-effort and never blocks the caller: a failed or
// slow write to the control-plane database should never stall a socket
// upgrade, a ping cycle, or a disconnect cleanup.
func (h *ConnectHandler) touchPresence(userID uuid.UUID) {
	if h.presence == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := h.presence.TouchActivity(ctx, userID); err != nil {
			h.log.Warn("touch presence", "error", err, "user_id", userID)
		}
	}()
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
	h.touchPresence(userID)
	if h.metrics != nil {
		h.metrics.WebSocketConnectionsActive.Inc()
	}
	ctx, cancel := context.WithCancel(r.Context())

	if err := h.registry.Register(ctx, userID, conn.ID, claims.Region, h.gatewayID); err != nil {
		h.log.Warn("registry register failed", "error", err)
	}
	// Best-effort, gated by the "connection_events" capability — see
	// relayConnectionEvent's doc comment. Fired with a background context
	// (not ctx, which this same call is about to be raced against by
	// cleanup's cancel()) so a slow broadcast never gets cut short by the
	// connection it's announcing.
	h.relayConnectionEvent(context.Background(), userID, claims.AppID, true)

	cleanup := func(reason string) {
		cancel()
		h.hub.Unregister(conn)
		_ = h.registry.Unregister(context.Background(), userID, conn.ID)
		_ = wsConn.Close()
		// One last touch so last_active_at freezes at the true moment this
		// user actually went away, rather than at their last ping (up to
		// pingInterval stale) or last inbound frame.
		h.touchPresence(userID)
		h.relayConnectionEvent(context.Background(), userID, claims.AppID, false)
		if h.metrics != nil {
			h.metrics.WebSocketConnectionsActive.Dec()
			h.metrics.WebSocketDisconnectsTotal.WithLabelValues(reason).Inc()
		}
	}

	go h.writePump(ctx, wsConn, conn, cleanup)
	h.readPump(ctx, wsConn, userID, claims.AppID, cleanup)
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
func (h *ConnectHandler) readPump(ctx context.Context, wsConn *websocket.Conn, userID uuid.UUID, appID int64, cleanup func(reason string)) {
	wsConn.SetReadDeadline(time.Now().Add(pongWait))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(pongWait))
		h.touchPresence(userID)
		return nil
	})

	defer cleanup("client_disconnected")
	for {
		_, data, err := wsConn.ReadMessage()
		if err != nil {
			return
		}
		h.handleInbound(ctx, userID, appID, data)
	}
}

// handleInbound is best-effort by design: a malformed or unrecognized frame
// is simply ignored, not a reason to drop the connection — the same
// tolerance readPump already had for anything it didn't expect.
func (h *ConnectHandler) handleInbound(ctx context.Context, userID uuid.UUID, appID int64, data []byte) {
	var frame inboundFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return
	}
	h.touchPresence(userID)
	channelID, err := uuid.Parse(frame.ChannelID)
	if err != nil {
		return
	}
	switch frame.Type {
	case "typing.start":
		h.relayTyping(ctx, userID, channelID, appID, true)
	case "typing.stop":
		h.relayTyping(ctx, userID, channelID, appID, false)
	}
}

// relayTyping never touches Postgres or Kafka — typing presence has no
// durability requirement (a missed frame just means the indicator doesn't
// show, exactly as safe as it not being sent), so it's relayed directly
// through the same Delivery local/remote resolution Fanout uses, skipping
// the outbox entirely. Membership is verified fresh (never trusted from the
// client) so a client can't broadcast typing into a channel it doesn't
// belong to (INSTRUCTIONS.md §43). Gated on this app's "typing_events"
// capability — a silent no-op when it's off, the same "ignore, don't error"
// tolerance as an unrecognized frame type, since there's no response
// channel to report a rejection on anyway.
func (h *ConnectHandler) relayTyping(ctx context.Context, userID, channelID uuid.UUID, appID int64, typing bool) {
	if !h.capabilityEnabled(ctx, appID, func(c apps.ChannelCapabilities) bool { return c.TypingEvents }) {
		return
	}
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
	if err := h.delivery.ToChannelMembers(ctx, channelID, frame, userID, userID); err != nil {
		h.log.Warn("typing: relay", "error", err)
	}
}

// ConnectionFrame is the JSON payload pushed to WebSocket clients for
// connection.updated — same "ephemeral, never durable" shape as
// TypingFrame, for the same reason: a missed connect/disconnect notice just
// means a peer's presence indicator goes briefly stale, no worse a
// guarantee than typing already carries.
type ConnectionFrame struct {
	Type      string    `json:"type"`
	UserID    uuid.UUID `json:"user_id"`
	Connected bool      `json:"connected"`
}

// relayConnectionEvent broadcasts userID's connect/disconnect to every
// channel it's currently a member of — the "connection_events" capability.
// Gated the same live, no-cache way as relayTyping. Unlike relayTyping
// (which already knows the one channel a typing frame concerns),
// ServeHTTP's connect/disconnect hooks don't have a channel_id to work
// from, so this resolves the user's whole channel list first — an extra
// control-plane round trip relayTyping doesn't pay, acceptable here since
// connects/disconnects happen orders of magnitude less often than typing
// keystrokes. Best-effort throughout: every failure is logged and
// swallowed, never lets a connect/disconnect itself fail because of it.
func (h *ConnectHandler) relayConnectionEvent(ctx context.Context, userID uuid.UUID, appID int64, connected bool) {
	if !h.capabilityEnabled(ctx, appID, func(c apps.ChannelCapabilities) bool { return c.ConnectionEvents }) {
		return
	}
	channelIDs, err := h.delivery.ChannelsForUser(ctx, userID)
	if err != nil {
		h.log.Warn("connection event: list channels", "error", err, "user_id", userID)
		return
	}
	if len(channelIDs) == 0 {
		return
	}
	frame, err := json.Marshal(ConnectionFrame{
		Type:      "connection.updated",
		UserID:    userID,
		Connected: connected,
	})
	if err != nil {
		h.log.Warn("connection event: marshal frame", "error", err)
		return
	}
	for _, channelID := range channelIDs {
		if err := h.delivery.ToChannelMembers(ctx, channelID, frame, userID, userID); err != nil {
			h.log.Warn("connection event: relay", "error", err, "channel_id", channelID)
		}
	}
}

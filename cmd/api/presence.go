package main

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// touchPresence best-effort marks identity.UserID active right now, so
// online status (last_active_at / is_online, see internal/users.IsOnline)
// reflects real actions. Called only from the REST mutation handlers that
// clearly mean "this user just did something" — sendMessage, add/remove
// reaction, markRead — deliberately not from requireAuth for every request,
// so read-heavy traffic (listMessages, listChannelMembers, ...) never turns
// into write traffic against the control-plane database. A live WebSocket
// connection is the other, usually-fresher signal — see
// internal/realtime/websocket.go's connect/pong/disconnect hooks.
//
// It never blocks the response and never fails the request: presence is
// best-effort by nature, so an error here is logged, not surfaced.
func (a *App) touchPresence(userID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.usersSvc.TouchActivity(ctx, userID); err != nil {
			a.log.Warn("touch presence", "error", err, "user_id", userID)
		}
	}()
}

package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// EdgePublisher mirrors every delivered frame to the edge Durable Object
// realtime worker (infra/cloudflare/realtime), so clients on the edge
// transport receive fan-out at their nearest Cloudflare PoP instead of over a
// WebSocket terminated in the origin region. It is an ADDITIVE sink: the normal
// Hub/Registry delivery is unchanged, and a failure here never affects it.
//
// The DO enforces membership at connect time; to preserve per-message block and
// exclude filtering, the origin sends the already-resolved recipient set (the
// same list Delivery computed) and the DO delivers only to those users' sockets.
type EdgePublisher struct {
	url    string // base URL of the realtime worker, e.g. https://chat-realtime.example.workers.dev
	key    string // shared secret sent as X-Internal-Key; the DO rejects /broadcast without it
	client *http.Client
	log    Logger
}

func NewEdgePublisher(url, key string, log Logger) *EdgePublisher {
	return &EdgePublisher{
		url:    url,
		key:    key,
		client: &http.Client{Timeout: 3 * time.Second},
		log:    log,
	}
}

type edgeBroadcast struct {
	To    []string        `json:"to"`
	Frame json.RawMessage `json:"frame"`
}

// Publish pushes frame to the edge DO for channelID, addressed to exactly the
// resolved recipients. Best-effort and asynchronous: it returns immediately and
// never blocks or fails the caller's own delivery path.
func (e *EdgePublisher) Publish(channelID uuid.UUID, frame []byte, recipients []uuid.UUID) {
	if e == nil || e.url == "" || len(recipients) == 0 {
		return
	}
	to := make([]string, len(recipients))
	for i, u := range recipients {
		to[i] = u.String()
	}
	body, err := json.Marshal(edgeBroadcast{To: to, Frame: json.RawMessage(frame)})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			e.url+"/broadcast?channel="+channelID.String(), bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Key", e.key)
		resp, err := e.client.Do(req)
		if err != nil {
			if e.log != nil {
				e.log.Error("edge realtime publish failed", "error", err, "channel_id", channelID)
			}
			return
		}
		resp.Body.Close()
	}()
}

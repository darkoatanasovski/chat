package main

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type deliveryFrame struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	Sequence  int64  `json:"sequence"`
	CreatedAt string `json:"created_at"`
}

// socket wraps one WebSocket connection for the duration of the test.
type socket struct {
	conn      *websocket.Conn
	received  int64
	latencies *latencyRecorder
}

func dialSocket(wsURL, token string, latencies *latencyRecorder) (*socket, error) {
	url := wsURL + "/connect?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	s := &socket{conn: conn, latencies: latencies}
	go s.readLoop()
	return s, nil
}

func (s *socket) readLoop() {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame deliveryFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		if frame.Type != "message.created" {
			continue
		}
		atomic.AddInt64(&s.received, 1)
		if createdAt, err := time.Parse("2006-01-02T15:04:05.000Z07:00", frame.CreatedAt); err == nil {
			s.latencies.record(time.Since(createdAt))
		}
	}
}

func (s *socket) close() {
	_ = s.conn.Close()
}

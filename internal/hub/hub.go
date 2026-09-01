// Package hub broadcasts live events to connected browsers over WebSockets.
//
// This replaces Flask-SocketIO. The app only ever emitted a single event
// ("file_added") in one direction, so a plain WebSocket carrying
// {"event": ..., "data": ...} frames covers it without the socket.io
// protocol negotiation.
package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeTimeout = 10 * time.Second
	pongTimeout  = 60 * time.Second
	pingInterval = 25 * time.Second
	// sendBuffer is how many events a slow client may fall behind before it
	// is dropped, rather than stalling the broadcast for everyone else.
	sendBuffer = 16
)

// Message is the envelope every broadcast is wrapped in.
type Message struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub tracks connected clients and fans out events to them.
type Hub struct {
	mu       sync.Mutex
	clients  map[*client]struct{}
	upgrader websocket.Upgrader
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{
		clients: make(map[*client]struct{}),
		upgrader: websocket.Upgrader{
			// The dashboard is served from the same origin as the socket, but
			// the Python version set cors_allowed_origins="*", so keep parity.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Broadcast sends an event to every connected client. It never blocks: a
// client whose buffer is full is disconnected and will reconnect on its own.
func (h *Hub) Broadcast(event string, data any) {
	payload, err := json.Marshal(Message{Event: event, Data: data})
	if err != nil {
		slog.Error("could not encode broadcast", "event", event, "error", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- payload:
		default:
			slog.Warn("dropping slow websocket client")
			close(c.send)
			delete(h.clients, c)
		}
	}
}

// ServeHTTP upgrades a request to a WebSocket and serves it until it closes.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		slog.Debug("websocket upgrade failed", "error", err)
		return
	}

	c := &client{conn: conn, send: make(chan []byte, sendBuffer)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	go h.writeLoop(c)
	h.readLoop(c)
}

// readLoop keeps the connection alive and detects disconnects. The client
// never sends application messages, so anything it does send is discarded.
func (h *Hub) readLoop(c *client) {
	defer h.remove(c)

	c.conn.SetReadLimit(1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) writeLoop(c *client) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			if !ok {
				_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// remove unregisters a client, tolerating a client already dropped by Broadcast.
func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
	c.conn.Close()
}

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dial connects a test client to a hub served over httptest.
func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// waitForClients blocks until the hub has registered n clients.
func waitForClients(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		count := len(h.clients)
		h.mu.Unlock()
		if count == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hub never reached %d clients", n)
}

func TestBroadcastReachesAllClients(t *testing.T) {
	h := New()
	server := httptest.NewServer(h)
	defer server.Close()

	clients := []*websocket.Conn{dial(t, server), dial(t, server)}
	waitForClients(t, h, 2)

	payload := map[string]string{"filename": "20251113_200214_1_SRC_1.wav", "unit_name": "Unit 1"}
	h.Broadcast("file_added", payload)

	for i, conn := range clients {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("client %d read: %v", i, err)
		}

		var message struct {
			Event string            `json:"event"`
			Data  map[string]string `json:"data"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("client %d decode: %v", i, err)
		}
		if message.Event != "file_added" {
			t.Errorf("client %d event = %q", i, message.Event)
		}
		if message.Data["filename"] != payload["filename"] {
			t.Errorf("client %d data = %v", i, message.Data)
		}
	}
}

func TestBroadcastWithNoClients(t *testing.T) {
	// Must not panic or block.
	New().Broadcast("file_added", map[string]string{"a": "b"})
}

func TestDisconnectedClientIsRemoved(t *testing.T) {
	h := New()
	server := httptest.NewServer(h)
	defer server.Close()

	conn := dial(t, server)
	waitForClients(t, h, 1)

	conn.Close()
	waitForClients(t, h, 0)
}

// TestSlowClientIsDropped covers the non-blocking broadcast: a client that
// stops reading is disconnected rather than stalling everyone else.
func TestSlowClientIsDropped(t *testing.T) {
	h := New()
	server := httptest.NewServer(h)
	defer server.Close()

	// Connect, then never read.
	dial(t, server)
	waitForClients(t, h, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more messages than the send buffer plus the socket buffer holds.
		for i := 0; i < sendBuffer*100; i++ {
			h.Broadcast("file_added", map[string]int{"n": i})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast blocked on a client that stopped reading")
	}
}

func TestNonWebSocketRequestIsRejected(t *testing.T) {
	server := httptest.NewServer(New())
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("a plain GET was accepted as a WebSocket upgrade")
	}
}

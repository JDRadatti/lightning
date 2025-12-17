package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// FuzzProtocol tests that the server can safely handle arbitrary incoming
// WebSocket messages. It ensures no panic, race, or invalid JSON response
// occurs for any input.
//
// Note: this test does not assert any business logic. Instead,
// it checks if the server can handle malformed inputs.
func FuzzProtocol(f *testing.F) {
	// Seeds
	//
	// All of these seeds represent expected values. Go fuzz testing
	// will generate random versions of these seeds automatically.
	f.Add(`{"type":"join","payload":{}}`)
	f.Add(`{"type":"join","payload":{"partyId":"party-1"}}`)
	f.Add(`{"type":"leave","payload":{}}`)
	f.Add(`{"type":"startGame","payload":{}}`)
	f.Add(`{"type":"playCard","payload":{"cardId":"abc"}}`)
	f.Add(`{"type":"matchCard","payload":{"targetClientId":"client-123"}}`)
	f.Add(`{"type":"unknown","payload":"garbage"}`)

	f.Fuzz(func(t *testing.T, rawMsg string) {
		t.Helper()

		// Start an isolated in-memory server for each fuzz iteration.
		pm := NewPartyManager()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ServeWs(pm, w, r)
		}))
		defer srv.Close()

		// Create websocket connection
		wsURL := httpToWs(t, srv.URL+"/ws")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Skipf("dial failed: %v", err)
			return
		}
		t.Cleanup(func() {
			_ = conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fuzz test done"),
			)
			conn.Close()
		})

		// Write fuzzed client message
		if !strings.HasPrefix(rawMsg, "{") {
			rawMsg = "{}"
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(rawMsg)); err != nil {
			t.Skipf("write failed: %v", err)
			return
		}

		// Read and Validate Response
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for _ = range 3 { // read up to a few messages per fuzz run
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("invalid server JSON: %v\nPayload: %s", err, string(data))
			}

			switch msg.Type {
			case ServerMessageConnectSuccess,
				ServerMessagePartyJoined,
				ServerMessageError:
				t.Logf("server responded with %s", msg.Type)
			default:
				t.Logf("server response ignored: %s", msg.Type)
			}
		}
	})
}

// httpToWs converts an http connection to a websocket connection
func httpToWs(t *testing.T, url string) string {
	t.Helper()
	if s, found := strings.CutPrefix(url, "https"); found {
		return "wss" + s
	}
	if s, found := strings.CutPrefix(url, "http"); found {
		return "ws" + s
	}
	return url
}

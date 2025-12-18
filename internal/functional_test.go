package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const timeout = 2 * time.Second

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// startTestServer starts a WebSocket server.
// returns the websocket server and its PartyManager.
func startTestServer(t *testing.T) (*httptest.Server, *PartyManager) {
	t.Helper()
	pm := NewPartyManager()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ServeWs(pm, w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pm
}

// wsDial connects to the test WebSocket endpoint and returns the connection.
func wsDial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := httpToWs(t, srv.URL+"/ws")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "functional test done"),
		)
		conn.Close()
	})
	return conn
}

// readMessage reads and parses a ServerMessage within the given timeout.
func readMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) ServerMessage {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var msg ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("invalid JSON from server: %v\nPayload: %s", err, string(data))
	}
	return msg
}

// sendMessage sends a ClientMessage over the WebSocket connection.
func sendMessage(t *testing.T, conn *websocket.Conn, msg ClientMessage) {
	t.Helper()
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

// ---------------------------------------------------------------------
// Functional Tests
// ---------------------------------------------------------------------

// TestConnectAndJoin verifies a basic join flow:
//
//	connect -> connectSuccess -> join -> partyJoined
func TestConnectAndJoin(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := wsDial(t, srv)

	// expect connectSuccess
	msg := readMessage(t, conn, timeout)
	if msg.Type != ServerMessageConnectSuccess {
		t.Fatalf("expected connectSuccess, got %s", msg.Type)
	}

	// send join request
	payload := json.RawMessage(`{"partyId": ""}`)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	// expect partyJoined
	msg2 := readMessage(t, conn, timeout)
	if msg2.Type != ServerMessageQueueJoined {
		t.Fatalf("expected queueJoined, got %s", msg2.Type)
	}

	// Wait for party assignment
	msg3 := readMessage(t, conn, 2*timeout)
	if msg3.Type != ServerMessagePartyJoined {
		t.Fatalf("expected partyJoined eventually, got %s", msg3.Type)
	}
}

// TestInvalidParty verifies that trying to join a nonexistent party
// returns an error message instead of crashing or ignoring it.
func TestInvalidParty(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := wsDial(t, srv)

	// read initial connectSuccess
	msg := readMessage(t, conn, timeout)
	if msg.Type != ServerMessageConnectSuccess {
		t.Fatalf("expected connectSuccess, got %s", msg.Type)
	}

	// send join request with invalid partyId
	payload := json.RawMessage(`{"partyId":"nonexistent-party"}`)
	sendMessage(t, conn, ClientMessage{
		Type:    ClientMessageJoin,
		Payload: payload,
	})

	// expect error message
	msg2 := readMessage(t, conn, timeout)
	if msg2.Type != ServerMessageError {
		t.Fatalf("expected error, got %s", msg2.Type)
	}
}

// TestMultipleClients verifies that multiple clients can join the same party.
func TestMultipleClients(t *testing.T) {
	srv, _ := startTestServer(t)
	connA := wsDial(t, srv)
	connB := wsDial(t, srv)

	// read initial connectSuccess
	_ = readMessage(t, connA, timeout)
	_ = readMessage(t, connB, timeout)

	// both join queue
	payload := json.RawMessage(`{"partyId": ""}`)
	sendMessage(t, connA, ClientMessage{Type: ClientMessageJoin, Payload: payload})
	sendMessage(t, connB, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	// eat the queueJoined messages
	_ = readMessage(t, connA, timeout)
	_ = readMessage(t, connB, timeout)

	// Wait for party assignment
	msgA := readMessage(t, connA, 3*timeout)
	msgB := readMessage(t, connB, 3*timeout)

	if msgA.Type != ServerMessagePartyJoined || msgB.Type != ServerMessagePartyJoined {
		t.Fatalf("expected both clients to eventually receive partyJoined (got %s, %s)", msgA.Type, msgB.Type)
	}
}

// TestJoinWithPartyID verifies that a client can successfully join
// an existing party with its PartyID.
func TestJoinWithPartyID(t *testing.T) {
	srv, _ := startTestServer(t)
	connA := wsDial(t, srv)
	connB := wsDial(t, srv)
	defer connA.Close()
	defer connB.Close()

	// eat connectSuccess messages
	_ = readMessage(t, connA, timeout)
	_ = readMessage(t, connB, timeout)

	// A joins with empty partyId and enters queue
	payloadA := json.RawMessage(`{"partyId": ""}`)
	sendMessage(t, connA, ClientMessage{Type: ClientMessageJoin, Payload: payloadA})

	// Expect QueueJoined and then PartyJoined for A
	queueMsgA := readMessage(t, connA, timeout)
	if queueMsgA.Type != ServerMessageQueueJoined {
		t.Fatalf("expected queueJoined for A, got %s", queueMsgA.Type)
	}

	joinedMsgA := readMessage(t, connA, 2*timeout)
	if joinedMsgA.Type != ServerMessagePartyJoined {
		t.Fatalf("expected partyJoined for A, got %s", joinedMsgA.Type)
	}

	// Extract PartyID from A's partyJoined message
	payloadAny, err := UnmarshalServerMessage(joinedMsgA)
	if err != nil {
		t.Fatalf("failed to unmarshal payload for A: %v", err)
	}
	joinedPayload, ok := payloadAny.(ServerMessagePartyJoinedPayload)
	if !ok {
		t.Fatalf("unexpected payload type for partyJoined: %T", payloadAny)
	}

	t.Logf("Client A joined party %s", joinedPayload.PartyID)

	// B joins A's specific party
	rawB, _ := json.Marshal(map[string]any{"partyId": joinedPayload.PartyID})
	sendMessage(t, connB, ClientMessage{Type: ClientMessageJoin, Payload: json.RawMessage(rawB)})

	// Expect B to receive PartyJoined for the same PartyID
	msgB := readMessage(t, connB, 2*timeout)
	if msgB.Type != ServerMessagePartyJoined {
		t.Fatalf("expected partyJoined for B, got %s", msgB.Type)
	}

	payloadB, err := UnmarshalServerMessage(msgB)
	if err != nil {
		t.Fatalf("failed to unmarshal payload for B: %v", err)
	}
	bJoinedPayload, ok := payloadB.(ServerMessagePartyJoinedPayload)
	if !ok {
		t.Fatalf("unexpected payload type for B: %T", payloadB)
	}

	if joinedPayload.PartyID != bJoinedPayload.PartyID {
		t.Fatalf("expected both clients in same party, got %s and %s",
			joinedPayload.PartyID, bJoinedPayload.PartyID)
	}
}

// TestMalformedMessages ensures that completely invalid payloads
// trigger an error message.
func TestMalformedMessages(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := wsDial(t, srv)

	// read initial connectSuccess
	_ = readMessage(t, conn, timeout)

	// send broken JSON
	raw := []byte(`{"type":"join","payload":"notAnObject"}`)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write raw failed: %v", err)
	}

	msg := readMessage(t, conn, timeout)
	if msg.Type != ServerMessageError {
		t.Fatalf("expected error message, got %s", msg.Type)
	}
}

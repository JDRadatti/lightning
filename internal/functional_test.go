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
		conn.Close()
	})
	return conn
}

// expectMessageType drains messages until it finds the target type or times out.
func expectMessageType(t *testing.T, conn *websocket.Conn, target ServerMessageType, timeout time.Duration) ServerMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for message type %s", target)
		}

		conn.SetReadDeadline(deadline)
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed while waiting for %s: %v", target, err)
		}

		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}

		if msg.Type == target {
			return msg
		}

		// Skip background noise
		if msg.Type == ServerMessageMemberUpdate || msg.Type == ServerMessageQueueJoined {
			continue
		}

		// If we get an Error when we didn't ask for one, log the details
		if msg.Type == ServerMessageError {
			t.Fatalf("received unexpected error while waiting for %s: %s", target, string(data))
		}

		t.Fatalf("expected %s, but got %s", target, msg.Type)
	}
}

// connectAndJoin handles connecting, connectSuccess, and joining a party.
func connectAndJoin(t *testing.T, srv *httptest.Server, partyID PartyID) (*websocket.Conn, PartyID) {
	t.Helper()
	conn := wsDial(t, srv)

	expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)

	payload := json.RawMessage(`{"partyId": "` + partyID + `"}`)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	msg := expectMessageType(t, conn, ServerMessagePartyJoined, 2*timeout)

	payloadAny, err := UnmarshalServerMessage(msg)
	if err != nil {
		t.Fatalf("failed to unmarshal partyJoined: %v", err)
	}
	pID := payloadAny.(ServerMessagePartyJoinedPayload).PartyID

	return conn, PartyID(pID)
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
	conn, partyID := connectAndJoin(t, srv, "")
	defer conn.Close()

	if partyID == "" {
		t.Fatal("expected valid partyID")
	}
}

// TestInvalidParty verifies that trying to join a nonexistent party
// returns an error message instead of crashing or ignoring it.
func TestInvalidParty(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := wsDial(t, srv)

	expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)

	payload := json.RawMessage(`{"partyId":"nonexistent-party"}`)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	expectMessageType(t, conn, ServerMessageError, timeout)
}

// TestMultipleClients verifies that multiple clients can join the same party.
func TestMultipleClients(t *testing.T) {
	srv, _ := startTestServer(t)

	connA, partyID := connectAndJoin(t, srv, "")
	defer connA.Close()

	connB, _ := connectAndJoin(t, srv, partyID)
	defer connB.Close()

	t.Log("Both clients joined successfully")
}

// TestJoinWithPartyID verifies that a client can successfully join
// an existing party with its PartyID.
func TestJoinWithPartyID(t *testing.T) {
	srv, _ := startTestServer(t)

	connA, partyID := connectAndJoin(t, srv, "")
	defer connA.Close()

	connB, bPartyID := connectAndJoin(t, srv, partyID)
	defer connB.Close()

	if partyID != bPartyID {
		t.Fatalf("expected both clients in same party, got %s and %s", partyID, bPartyID)
	}
}

// TestMalformedMessages ensures that completely invalid payloads
// trigger an error message.
func TestMalformedMessages(t *testing.T) {
	srv, _ := startTestServer(t)
	conn := wsDial(t, srv)

	_ = expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)

	raw := []byte(`{"type":"join","payload":"notAnObject"}`)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write raw failed: %v", err)
	}

	msg := expectMessageType(t, conn, ServerMessageError, timeout)
	if msg.Type != ServerMessageError {
		t.Fatalf("expected error message, got %s", msg.Type)
	}
}

// TestPartyHostTransfer verifies that when the host leaves a party,
// another connected client becomes the new host.
func TestPartyHostTransfer(t *testing.T) {
	srv, _ := startTestServer(t)

	connA, partyID := connectAndJoin(t, srv, "")
	connB, _ := connectAndJoin(t, srv, partyID)
	defer connB.Close()
	defer connA.Close()

	// Host (A) leaves
	sendMessage(t, connA, ClientMessage{Type: ClientMessageLeave, Payload: json.RawMessage(`{}`)})

	expectMessageType(t, connA, ServerMessagePartyLeft, 2*timeout)

	// B should get a memberUpdate reflecting the host transfer
	updateMsg := expectMessageType(t, connB, ServerMessageMemberUpdate, 2*timeout)

	payloadAny, err := UnmarshalServerMessage(updateMsg)
	if err != nil {
		t.Fatalf("failed to unmarshal memberUpdate: %v", err)
	}
	payloadBytes, _ := json.Marshal(payloadAny)
	var memberUpdatePayload ServerMessageMemberUpdatePayload
	if err := json.Unmarshal(payloadBytes, &memberUpdatePayload); err != nil {
		t.Fatalf("invalid memberUpdate payload shape: %v", err)
	}

	var bIsHost bool
	for _, m := range memberUpdatePayload.Members {
		if m.ID != "" && m.IsHost {
			bIsHost = true
			break
		}
	}

	if !bIsHost {
		t.Fatalf("expected B to become the new host after A left")
	}
}

// TestStartGame verifies that when the host
// requests to start a game, all party members receive gameStarted.
func TestStartGame(t *testing.T) {
	srv, _ := startTestServer(t)

	connA, partyID := connectAndJoin(t, srv, "")
	connB, _ := connectAndJoin(t, srv, partyID)
	defer connA.Close()
	defer connB.Close()

	sendMessage(t, connA, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	_ = expectMessageType(t, connA, ServerMessageGameStarted, timeout)
	_ = expectMessageType(t, connB, ServerMessageGameStarted, timeout)
}

// TestNonHostCannotStartGame verifies that the server returns an error
// when a member who is not the host attempts to start the game.
func TestNonHostCannotStartGame(t *testing.T) {
	srv, _ := startTestServer(t)

	_, partyID := connectAndJoin(t, srv, "")
	connB, _ := connectAndJoin(t, srv, partyID)
	defer connB.Close()

	sendMessage(t, connB, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	msgError := expectMessageType(t, connB, ServerMessageError, timeout)

	payloadErr, _ := UnmarshalServerMessage(msgError)
	plErr := payloadErr.(ServerMessageErrorPayload)
	if plErr.Code != ErrorCodeNotPartyHost {
		t.Fatalf("expected error code %s, got %s", ErrorCodeNotPartyHost, plErr.Code)
	}
}

// TestGameCannotStartWithSinglePlayer verifies that the server returns an error
// when the host attempts to start a game while being the only member.
func TestGameCannotStartWithSinglePlayer(t *testing.T) {
	srv, _ := startTestServer(t)

	connA, _ := connectAndJoin(t, srv, "")
	defer connA.Close()

	sendMessage(t, connA, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	msgError := expectMessageType(t, connA, ServerMessageError, timeout)

	payloadErr, _ := UnmarshalServerMessage(msgError)
	plErr := payloadErr.(ServerMessageErrorPayload)
	if plErr.Code != ErrorCodeNotEnoughMembers {
		t.Fatalf("expected error code %s, got %s", ErrorCodeNotEnoughMembers, plErr.Code)
	}
}

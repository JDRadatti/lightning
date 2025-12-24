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

// TestClient wraps all session info needed to verify and reconnect a client.
type TestClient struct {
	Conn      *websocket.Conn
	ID        ClientID
	SecretKey SecretKey
	PartyID   PartyID
}

type joinPayload struct {
	ClientID string `json:"clientId"`
	PartyID  string `json:"partyId"`
	Secret   string `json:"secret,omitempty"`
}

// startTestServer starts a WebSocket server.
// returns the websocket server and its PartyManager.
func startTestServer(t *testing.T) (*httptest.Server, *PartyManager) {
	t.Helper()
	pm := NewPartyManagerWithTimeouts(100*time.Millisecond, 50*time.Millisecond)
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
func connectAndJoin(t *testing.T, srv *httptest.Server, jp joinPayload) *TestClient {
	t.Helper()
	conn := wsDial(t, srv)

	msgSuccess := expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)
	payloadAny, err := UnmarshalServerMessage(msgSuccess)
	if err != nil {
		t.Fatalf("failed to unmarshal connectSuccess: %v", err)
	}
	success := payloadAny.(ServerMessageConnectSuccessPayload)

	payloadBytes, _ := json.Marshal(jp)
	payload := json.RawMessage(payloadBytes)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	msg := expectMessageType(t, conn, ServerMessagePartyJoined, timeout)

	payloadAny, err = UnmarshalServerMessage(msg)
	if err != nil {
		t.Fatalf("failed to unmarshal partyJoined: %v", err)
	}
	pID := payloadAny.(ServerMessagePartyJoinedPayload).PartyID

	// Drain the MemberUpdate broadcast when joining
	_ = expectMessageType(t, conn, ServerMessageMemberUpdate, timeout)

	return &TestClient{
		Conn:      conn,
		ID:        ClientID(success.ClientID),
		SecretKey: success.SecretKey,
		PartyID:   PartyID(pID),
	}
}

// connectAndJoinFail handles connecting, receiving connectSuccess,
// attempting to join a party, and expecting an error response.
// It returns the error message received.
func connectAndJoinFail(t *testing.T, srv *httptest.Server, jp joinPayload) *TestClient {
	t.Helper()
	conn := wsDial(t, srv)

	msgSuccess := expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)
	payloadAny, err := UnmarshalServerMessage(msgSuccess)
	if err != nil {
		t.Fatalf("failed to unmarshal connectSuccess: %v", err)
	}
	success := payloadAny.(ServerMessageConnectSuccessPayload)

	payloadBytes, _ := json.Marshal(jp)
	payload := json.RawMessage(payloadBytes)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	_ = expectMessageType(t, conn, ServerMessageError, timeout)

	return &TestClient{
		Conn:      conn,
		ID:        ClientID(success.ClientID),
		SecretKey: success.SecretKey,
		PartyID:   "",
	}
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
	client := connectAndJoin(t, srv, joinPayload{})
	defer client.Conn.Close()

	if client.PartyID == "" || client.SecretKey == "" {
		t.Fatal("expected valid session data (PartyID and SecretKey)")
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

	clientA := connectAndJoin(t, srv, joinPayload{})
	defer clientA.Conn.Close()

	clientB := connectAndJoin(t, srv, joinPayload{
		PartyID: string(clientA.PartyID),
	})
	defer clientB.Conn.Close()

	t.Logf("Both clients joined successfully. Host: %s, Peer: %s", clientA.ID, clientB.ID)
}

// TestJoinWithPartyID verifies that a client can successfully join
// an existing party with its PartyID.
func TestJoinWithPartyID(t *testing.T) {
	srv, _ := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	defer clientA.Conn.Close()

	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientB.Conn.Close()

	if clientA.PartyID != clientB.PartyID {
		t.Fatalf("expected both clients in same party, got %s and %s", clientA.PartyID, clientB.PartyID)
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

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientB.Conn.Close()
	defer clientA.Conn.Close()

	// Host (A) leaves
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageLeave, Payload: json.RawMessage(`{}`)})

	expectMessageType(t, clientA.Conn, ServerMessagePartyLeft, timeout)

	// B should get a memberUpdate reflecting the host transfer
	updateMsg := expectMessageType(t, clientB.Conn, ServerMessageMemberUpdate, timeout)

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
		if m.ID == string(clientB.ID) && m.IsHost {
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

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientA.Conn.Close()
	defer clientB.Conn.Close()

	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	_ = expectMessageType(t, clientA.Conn, ServerMessageGameStarted, timeout)
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameStarted, timeout)
}

// TestNonHostCannotStartGame verifies that the server returns an error
// when a member who is not the host attempts to start the game.
func TestNonHostCannotStartGame(t *testing.T) {
	srv, _ := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientB.Conn.Close()

	sendMessage(t, clientB.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	msgError := expectMessageType(t, clientB.Conn, ServerMessageError, timeout)

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

	clientA := connectAndJoin(t, srv, joinPayload{})
	defer clientA.Conn.Close()

	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})

	msgError := expectMessageType(t, clientA.Conn, ServerMessageError, timeout)

	payloadErr, _ := UnmarshalServerMessage(msgError)
	plErr := payloadErr.(ServerMessageErrorPayload)
	if plErr.Code != ErrorCodeNotEnoughMembers {
		t.Fatalf("expected error code %s, got %s", ErrorCodeNotEnoughMembers, plErr.Code)
	}
}

// TestClientDisconnectAndReconnect verifies that a client can reconnect
// within the abandonment window and continue in the party.
func TestClientDisconnectAndReconnect(t *testing.T) {
	srv, pm := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{})
	defer clientB.Conn.Close()

	// A disconnects
	clientA.Conn.Close()

	// Wait a bit but within abandonment timeout
	time.Sleep(5 * time.Millisecond)

	// A reconnects with same PartyID
	clientA2 := connectAndJoin(t, srv, joinPayload{
		ClientID: string(clientA.ID),
		PartyID:  string(clientA.PartyID),
		Secret:   string(clientA.SecretKey),
	})
	defer clientA2.Conn.Close()

	// Add new Client and check that old ClientID is being used in MemberUpdate
	_ = connectAndJoin(t, srv, joinPayload{})
	defer clientB.Conn.Close()
	// B should get a memberUpdate reflecting the reconnected user
	_ = expectMessageType(t, clientB.Conn, ServerMessageMemberUpdate, timeout)
	updateMsg := expectMessageType(t, clientB.Conn, ServerMessageMemberUpdate, timeout)

	payloadAny, err := UnmarshalServerMessage(updateMsg)
	if err != nil {
		t.Fatalf("failed to unmarshal memberUpdate: %v", err)
	}

	payloadBytes, _ := json.Marshal(payloadAny)
	var memberUpdatePayload ServerMessageMemberUpdatePayload
	if err := json.Unmarshal(payloadBytes, &memberUpdatePayload); err != nil {
		t.Fatalf("invalid memberUpdate payload shape: %v", err)
	}

	if len(memberUpdatePayload.Members) != 2 {
		t.Fatal("There should be two members in the party")
	}

	var clientARejoined bool
	for _, m := range memberUpdatePayload.Members {
		if m.ID == string(clientA.ID) {
			clientARejoined = true
			break
		}
	}

	// Client A should be in the member list now
	if !clientARejoined {
		t.Fatal("Client A failed ot rejoin")
	}

	// Both should still be in party
	if _, stillAbandoned := pm.Abandoned[clientA.ID]; stillAbandoned {
		t.Fatal("client should not be abandoned after reconnect")
	}
}

// TestClientAbandonment verifies that after abandonmentTimeout,
// a client is permanently removed from the party.
func TestClientAbandonment(t *testing.T) {
	srv, pm := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{
		PartyID: string(clientA.PartyID),
	})
	defer clientB.Conn.Close()

	// A disconnects
	clientA.Conn.Close()

	// Wait for abandonment timeout + cleanup interval
	time.Sleep(200 * time.Millisecond)

	// A is no longer in Members
	if _, inParty := pm.Members[clientA.ID]; inParty {
		t.Fatal("abandoned client should be removed from party")
	}

	// Verify B is still in party
	if _, inParty := pm.Members[clientB.ID]; !inParty {
		t.Fatal("non-abandoned client should still be in party")
	}
}

// TestReconnectAfterAbandonmentTimeout verifies that reconnecting
// after abandonment timeout fails with SessionExpired error.
func TestReconnectAfterAbandonmentTimeout(t *testing.T) {
	srv, _ := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	partyID := clientA.PartyID
	clientA.Conn.Close()

	// Wait for abandonment
	time.Sleep(200 * time.Millisecond)

	// Try to reconnect
	conn := wsDial(t, srv)
	defer conn.Close()

	_ = expectMessageType(t, conn, ServerMessageConnectSuccess, timeout)

	payload := json.RawMessage(`{"partyId": "` + string(partyID) + `"}`)
	sendMessage(t, conn, ClientMessage{Type: ClientMessageJoin, Payload: payload})

	// Should get error (party or session expired)
	msgErr := expectMessageType(t, conn, ServerMessageError, timeout)
	if msgErr.Type != ServerMessageError {
		t.Fatalf("expected error, got %s", msgErr.Type)
	}
}

// TestReconnectWithWrongSecret - Reconnect with invalid secret
func TestReconnectWithWrongSecret(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientA.Conn.Close()
	time.Sleep(5 * time.Millisecond)

	// Try to reconnect with wrong secret
	_ = connectAndJoinFail(t, srv, joinPayload{
		ClientID: string(clientA.ID),
		Secret:   "invalid secret",
	})

	// Original client should be cleaned up
	if _, stillAbandoned := pm.Abandoned[clientA.ID]; stillAbandoned {
		t.Fatal("client should be removed from abandoned after failed reconnect")
	}
}

// TestPartyDisbandedWhenAllAbandoned - Verify party cleanup
func TestPartyDisbandedWhenAllAbandoned(t *testing.T) {
	srv, pm := startTestServer(t)

	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	partyID := clientA.PartyID

	// Both disconnect
	clientA.Conn.Close()
	clientB.Conn.Close()

	// Wait for abandonment timeout
	time.Sleep(150 * time.Millisecond)

	// Party should be removed
	if _, exists := pm.Parties[partyID]; exists {
		t.Fatal("party should be removed when all members abandoned")
	}
}

// TestRapidReconnectAttempts - Multiple reconnect tries in quick succession
func TestRapidReconnectAttempts(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	originalID := clientA.ID
	clientA.Conn.Close()
	time.Sleep(5 * time.Millisecond)

	// Try to reconnect 3 times rapidly
	for _ = range 3 {
		clientA2 := connectAndJoin(t, srv, joinPayload{
			ClientID: string(clientA.ID),
			Secret:   string(clientA.SecretKey),
			PartyID:  string(clientA.PartyID),
		})
		clientA2.Conn.Close()
		time.Sleep(2 * time.Millisecond)
	}

	// Should only be one instance in party
	if _, inParty := pm.Members[originalID]; !inParty {
		t.Fatal("client should be in party")
	}
}

// TestClientRemovedOnLeave - Verify client is removed from Members when they leave
func TestClientRemovedOnLeave(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientID := clientA.ID
	partyID := clientA.PartyID

	// Verify client is in Members
	if _, inParty := pm.Members[clientID]; !inParty {
		t.Fatal("client should be in Members after join")
	}

	// Client leaves
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageLeave, Payload: json.RawMessage(`{}`)})
	_ = expectMessageType(t, clientA.Conn, ServerMessagePartyLeft, timeout)

	// Verify client is removed from Members
	if _, inParty := pm.Members[clientID]; inParty {
		t.Fatal("client should be removed from Members after leave")
	}

	// Party should not exist
	if _, exists := pm.Parties[partyID]; exists {
		t.Fatal("party should be disbanded when empty")
	}
}

// TestClientRemovedOnAbandonment - Verify abandoned client is removed after timeout
func TestClientRemovedOnAbandonment(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientB.Conn.Close()

	clientID := clientA.ID

	// Verify client is in Members
	if _, inParty := pm.Members[clientID]; !inParty {
		t.Fatal("client should be in Members after join")
	}

	// A disconnects
	clientA.Conn.Close()
	time.Sleep(5 * time.Millisecond)

	// Verify client is still in Members
	if _, inParty := pm.Members[clientID]; !inParty {
		t.Fatal("client should still be in Members while abandoned")
	}

	if _, isAbandoned := pm.Abandoned[clientID]; !isAbandoned {
		t.Fatal("client should be in Abandoned")
	}

	// Wait for abandonment timeout
	time.Sleep(150 * time.Millisecond)

	// Verify client is removed from Members
	if _, inParty := pm.Members[clientID]; inParty {
		t.Fatal("client should be removed from Members after abandonment timeout")
	}

	// Verify client is removed from Abandoned
	if _, isAbandoned := pm.Abandoned[clientID]; isAbandoned {
		t.Fatal("client should be removed from Abandoned after cleanup")
	}
}

// TestPartyRemovedWhenEmpty - Party is removed when last member leaves
func TestPartyRemovedWhenEmpty(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	partyID := clientA.PartyID

	// Verify party exists
	if _, exists := pm.Parties[partyID]; !exists {
		t.Fatal("party should exist after client joins")
	}

	// Client leaves
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageLeave, Payload: json.RawMessage(`{}`)})
	_ = expectMessageType(t, clientA.Conn, ServerMessagePartyLeft, timeout)

	// Verify party is removed
	if _, exists := pm.Parties[partyID]; exists {
		t.Fatal("party should be removed when empty")
	}
}

// TestPartyRemovedWhenAllAbandonedTimeout - Party removed when all members abandoned
func TestPartyRemovedWhenAllAbandonedTimeout(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	partyID := clientA.PartyID

	// Both disconnect
	clientA.Conn.Close()
	clientB.Conn.Close()
	time.Sleep(5 * time.Millisecond)

	// Party still exists (members are just abandoned)
	if _, exists := pm.Parties[partyID]; !exists {
		t.Fatal("party should still exist while members are abandoned")
	}

	// Wait for abandonment timeout
	time.Sleep(150 * time.Millisecond)

	// Party should be removed
	if _, exists := pm.Parties[partyID]; exists {
		t.Fatal("party should be removed when all members abandoned")
	}
}

// TestGameRemovedOnEnd - Game is removed from Games map after ending
func TestGameRemovedOnEnd(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientA.Conn.Close()
	defer clientB.Conn.Close()

	// Start game
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})
	_ = expectMessageType(t, clientA.Conn, ServerMessageGameStarted, timeout)
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameStarted, timeout)

	// Get gameID from the party
	partyID := clientA.PartyID
	party := pm.Parties[partyID]
	gameID := party.game.ID

	// Verify game exists in Games map
	if _, exists := pm.Games[gameID]; !exists {
		t.Fatal("game should exist in Games map")
	}

	// End game by having player disconnect
	clientA.Conn.Close()
	time.Sleep(10 * time.Millisecond)

	// B should receive game over
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameOver, timeout)
	time.Sleep(10 * time.Millisecond)

	// Game should be removed from Games map
	if _, exists := pm.Games[gameID]; exists {
		t.Fatal("game should be removed from Games map after ending")
	}

	// Game reference should be cleared from party
	if party.game != nil {
		t.Fatal("game reference should be cleared from party after ending")
	}
}

// TestGameClientReferencesCleared - Client.game is nil after game ends
func TestGameClientReferencesCleared(t *testing.T) {
	srv, _ := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientB.Conn.Close()

	// Start game
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})
	_ = expectMessageType(t, clientA.Conn, ServerMessageGameStarted, timeout)
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameStarted, timeout)

	// End game
	clientA.Conn.Close()
	time.Sleep(10 * time.Millisecond)

	_ = expectMessageType(t, clientB.Conn, ServerMessageGameOver, timeout)
	time.Sleep(10 * time.Millisecond)

	// Try to send player action - should fail (not in game)
	payload := json.RawMessage(`{"action": "flip"}`)
	sendMessage(t, clientB.Conn, ClientMessage{Type: ClientMessagePlayerAction, Payload: payload})

	msgErr := expectMessageType(t, clientB.Conn, ServerMessageError, timeout)
	payloadErr, _ := UnmarshalServerMessage(msgErr)
	if payloadErr.(ServerMessageErrorPayload).Code != ErrorCodeNotInGame {
		t.Fatal("expected NotInGame error after game ends")
	}
}

// TestPartyPersistsAfterGame - Party exists after game ends, ready for new game
func TestPartyPersistsAfterGame(t *testing.T) {
	srv, pm := startTestServer(t)
	clientA := connectAndJoin(t, srv, joinPayload{})
	clientB := connectAndJoin(t, srv, joinPayload{PartyID: string(clientA.PartyID)})
	defer clientA.Conn.Close()
	defer clientB.Conn.Close()

	partyID := clientA.PartyID

	// Start game
	sendMessage(t, clientA.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})
	_ = expectMessageType(t, clientA.Conn, ServerMessageGameStarted, timeout)
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameStarted, timeout)

	// End game by disconnecting
	clientA.Conn.Close()
	time.Sleep(10 * time.Millisecond)
	_ = expectMessageType(t, clientB.Conn, ServerMessageGameOver, timeout)

	// Reconnect A
	clientA2 := connectAndJoin(t, srv, joinPayload{
		ClientID: string(clientA.ID),
		PartyID:  string(partyID),
		Secret:   string(clientA.SecretKey),
	})
	defer clientA2.Conn.Close()

	// Party should still exist and both clients in it
	if _, exists := pm.Parties[partyID]; !exists {
		t.Fatal("party should persist after game ends")
	}

	party := pm.Parties[partyID]
	if len(party.Members) != 2 {
		t.Fatalf("party should have 2 members, got %d", len(party.Members))
	}

	// Party should be ready for another game
	if party.game != nil {
		t.Fatal("party.game should be nil after previous game ended")
	}

	// Host can start a new game. Note: host was transfered to B when A left
	sendMessage(t, clientB.Conn, ClientMessage{Type: ClientMessageStartGame, Payload: json.RawMessage(`{}`)})
	newGameMsg := expectMessageType(t, clientB.Conn, ServerMessageGameStarted, timeout)
	if newGameMsg.Type != ServerMessageGameStarted {
		t.Fatal("should be able to start new game after previous one ended")
	}
}

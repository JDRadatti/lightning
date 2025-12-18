package internal

import (
	"encoding/json"
	"fmt"
)

type ServerMessageType string
type ServerErrorCode string
type ClientMessageType string

const (
	ServerMessageConnectSuccess ServerMessageType = "connectSuccess"
	ServerMessagePartyJoined    ServerMessageType = "partyJoined"
	ServerMessagePartyLeft      ServerMessageType = "partyLeft"
	ServerMessageQueueJoined    ServerMessageType = "queueJoined"
	ServerMessageError          ServerMessageType = "error"
	ServerMessageMemberUpdate   ServerMessageType = "memberUpdate"
	ServerMessageGameOver       ServerMessageType = "gameOver"
	ServerMessageGameStarted    ServerMessageType = "gameStarted"
)

const (
	ErrorCodeInvalidRequest ServerErrorCode = "invalidRequest"
	ErrorCodeAlreadyInParty ServerErrorCode = "alreadyInParty"
	ErrorCodePartyNotFound  ServerErrorCode = "partyNotFound"
	ErrorCodeNotInSession   ServerErrorCode = "notInSession"
	ErrorCodePartyFull      ServerErrorCode = "partyFull"
	ErrorCodeQueueFull      ServerErrorCode = "queueFull"
)

const (
	ClientMessageJoin      ClientMessageType = "join"
	ClientMessageLeave     ClientMessageType = "leave"
	ClientMessageStartGame ClientMessageType = "startGame"
)

// ---------------------------------------------------------------------
// Client Messages
// ---------------------------------------------------------------------

type ClientMessage struct {
	Type    ClientMessageType `json:"type"`
	Payload json.RawMessage   `json:"payload"`
}

type ClientMessageJoinPayload struct {
	PartyID PartyID `json:"partyId"`
}

type ClientMessageStartGamePayload struct {
	PartyID PartyID `json:"partyId"`
}

type ClientMessageLeavePayload struct{}

// ---------------------------------------------------------------------
// Server Messages
// ---------------------------------------------------------------------

type ServerMessage struct {
	Type    ServerMessageType `json:"type"`
	Payload json.RawMessage   `json:"payload"`
}

type ServerMessageConnectSuccessPayload struct {
	ClientID ClientID `json:"clientId"`
}

type ServerMessageGameStartedPayload struct {
	CountdownSeconds int   `json:"countdownSeconds"`
	Timestamp        int64 `json:"timestamp"`
}

type ServerMessageGameEndedPayload struct {
	WinnerID string `json:"winnerId"`
	Reason   string `json:"reason"`
}

type ServerMessagePartyJoinedPayload struct {
	PartyID PartyID `json:"partyId"`
}

type ServerMessageMemberUpdatePayload struct {
	Members []PartyMemberInfo `json:"members"`
}

type ServerMessageQueueJoinedPayload struct{}

type ServerMessageErrorPayload struct {
	Code        ServerErrorCode   `json:"code"`
	Message     string            `json:"message"`
	RequestType ClientMessageType `json:"requestType,omitempty"`
}

// UnmarshalServerMessage decodes the Payload of a ServerMessage
// into its corresponding typed payload struct.
//
// Returns (payload, error)
func UnmarshalServerMessage(msg ServerMessage) (any, error) {
	switch msg.Type {

	case ServerMessageConnectSuccess:
		var p ServerMessageConnectSuccessPayload
		return p, json.Unmarshal(msg.Payload, &p)

	case ServerMessageQueueJoined:
		var p ServerMessageQueueJoinedPayload
		return p, json.Unmarshal(msg.Payload, &p)

	case ServerMessagePartyJoined:
		var p ServerMessagePartyJoinedPayload
		return p, json.Unmarshal(msg.Payload, &p)

	case ServerMessageError:
		var p ServerMessageErrorPayload
		return p, json.Unmarshal(msg.Payload, &p)

	case ServerMessageMemberUpdate:
		var p ServerMessageMemberUpdatePayload
		return p, json.Unmarshal(msg.Payload, &p)

	default:
		return nil, fmt.Errorf("unknown server message type: %s", msg.Type)
	}
}

// UnmarshalClientMessage decodes the ClientMessage payload
// into the appropriate typed struct depending on msg.Type.
//
// Returns (payload, error)
func UnmarshalClientMessage(msg ClientMessage) (any, error) {
	switch msg.Type {
	case ClientMessageJoin:
		var payload ClientMessageJoinPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return nil, err
		}
		return payload, nil

	case ClientMessageLeave:
		var payload ClientMessageLeavePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return nil, err
		}
		return payload, nil

	case ClientMessageStartGame:
		var payload ClientMessageStartGamePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return nil, err
		}
		return payload, nil

	default:
		// Unknown or invalid message type
		return nil, fmt.Errorf("unknown client message type: %s", msg.Type)
	}
}

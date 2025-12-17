package internal

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

const (
	sendBufferSize = 6
)

type ClientID string

type Client struct {
	ID   ClientID
	conn *websocket.Conn
	send chan ServerMessage
	pm   *PartyManager
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// ServeWs is the main entrypoint of a client. It creates the Client object and
// starts the read and write pumps.
func ServeWs(pm *PartyManager, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	c := &Client{
		ID:   ClientID(r.RemoteAddr),
		conn: conn,
		send: make(chan ServerMessage, sendBufferSize),
		pm:   pm,
	}

	go c.writePump()
	go c.readPump()

	c.SendMessage(ServerMessageConnectSuccess, ServerMessageConnectSuccessPayload{ClientID: c.ID})
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer c.conn.Close()
	for {
		var msg ClientMessage
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("connection closed: %v", err)
			break
		}

		payload, err := UnmarshalClientMessage(msg)
		if err != nil {
			c.SendError(ErrorCodeInvalidRequest, "Malformed client payload.", msg.Type)
			continue
		}

		switch msg.Type {
		case ClientMessageJoin:
			if p, ok := payload.(ClientMessageJoinPayload); ok {
				c.pm.SendCommand(PartyManagerCommand{
					Type:    PartyManagerCommandAddClient,
					Payload: PartyManagerAddClientPayload{Client: c, PartyID: p.PartyID},
				})
			}
		default:
			c.SendError(ErrorCodeInvalidRequest, "Unknown request.", msg.Type)
		}
	}
}

// writePump pumps messages from the ProjectManager/Game to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump() {
	defer c.Close()
	for msg := range c.send {
		if err := c.conn.WriteJSON(msg); err != nil {
			log.Printf("write err: %v", err)
			return
		}
	}
}

func (c *Client) SendMessage(msgType ServerMessageType, payload any) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("SendMessage: failed to marshal payload for client %s: %v (msgType=%s)", c.ID, err, msgType)
		return
	}
	select {
	case c.send <- ServerMessage{Type: msgType, Payload: bytes}:
	default:
		log.Printf("Send buffer full for %s", c.ID)
	}
}

func (c *Client) SendError(code ServerErrorCode, message string, reqType ClientMessageType) {
	payload := ServerMessageErrorPayload{
		Code:        code,
		Message:     message,
		RequestType: reqType,
	}
	c.SendMessage(ServerMessageError, payload)
}

func (c *Client) Close() {
	_ = c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "closed"))
	c.conn.Close()
}

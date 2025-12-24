package internal

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512

	// Size of the client's send buffer
	sendBufferSize = 6
)

type ClientID string

// NewClientID creates a new ClientID.
func NewClientID() ClientID {
	return ClientID(uuid.New().String())
}

// SecretKey validates a client.
type SecretKey string

// NewSecretKey creates a new SecretKey.
func NewSecretKey() SecretKey {
	return SecretKey(uuid.New().String())
}

type Client struct {
	ID     ClientID
	Secret SecretKey
	conn   *websocket.Conn
	send   chan ServerMessage
	pm     *PartyManager
	game   *Game
	mu     sync.Mutex
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// ServeWs is the main entrypoint of a client. It creates the Client object and
// starts the read and write pumps.
func ServeWs(pm *PartyManager, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	c := &Client{
		ID:     NewClientID(),
		Secret: NewSecretKey(),
		conn:   conn,
		send:   make(chan ServerMessage, sendBufferSize),
		pm:     pm,
	}

	go c.writePump()
	go c.readPump()

	c.SendMessage(ServerMessageConnectSuccess, ServerMessageConnectSuccessPayload{ClientID: c.ID, SecretKey: c.Secret})
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		// Tell the PartyManager this client DISCONNECTED
		c.pm.SendCommand(PartyManagerCommand{
			Type:    PartyManagerCommandDisconnectClient,
			Payload: PartyManagerDisconnectPayload{Client: c},
		})
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

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
					Payload: PartyManagerAddClientPayload{Client: c, ClientID: p.ClientID, PartyID: p.PartyID, SecretKey: p.SecretKey},
				})
			}
		case ClientMessageLeave:
			if _, ok := payload.(ClientMessageLeavePayload); ok {
				c.pm.SendCommand(PartyManagerCommand{
					Type:    PartyManagerCommandRemoveClient,
					Payload: PartyManagerRemoveClientPayload{Client: c},
				})
			}
		case ClientMessageStartGame:
			if _, ok := payload.(ClientMessageStartGamePayload); ok {
				c.pm.SendCommand(PartyManagerCommand{
					Type:    PartyManagerCommandStartGame,
					Payload: PartyManagerStartGamePayload{Client: c},
				})
			}
		case ClientMessagePlayerAction:
			if p, ok := payload.(ClientMessagePlayerActionPayload); ok {
				c.mu.Lock()
				game := c.game
				c.mu.Unlock()

				if game == nil {
					c.SendError(ErrorCodeNotInGame, "Not in a game.", msg.Type)
					continue
				}

				game.SendCommand(GameCommand{
					Type: GameCommandPlayerAction,
					Payload: GameCommandPlayerActionPayload{
						ClientID: c.ID,
						Action:   p.Action,
					},
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
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			data, err := json.Marshal(message)
			if err != nil {
				_ = w.Close()
				return
			}

			_, _ = w.Write(data)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
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

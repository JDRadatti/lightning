package internal

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// GameID uniquely identifies a game instance.
type GameID string

// NewGameID returns a new randomly generated GameID.
func NewGameID() GameID {
	return GameID(uuid.New().String())
}

// GameCommandType defines the list of commands that a Game can process.
type GameCommandType string

const (
	GameCommandStartGame        GameCommandType = "startGame"
	GameCommandEndGame          GameCommandType = "endGame"
	GameCommandPlayerAction     GameCommandType = "playerAction"
	GameCommandClientDisconnect GameCommandType = "clientDisconnect"
)

// GameCommand represents a single instruction sent to a Game
// through its `commands` channel.
type GameCommand struct {
	Type    GameCommandType
	Payload any
}

// GameCommandPlayerActionPayload carries player action data
type GameCommandPlayerActionPayload struct {
	ClientID ClientID
	Action   string
}

// GameCommandClientDisconnectPayload carries disconnect data
type GameCommandClientDisconnectPayload struct {
	ClientID ClientID
}

// GameEventType defines supported GameEvent kinds.
type GameEventType string

const (
	GameEventStarted GameEventType = "gameStarted"
	GameEventEnded   GameEventType = "gameEnded"
)

// GameEvent represents an event sent from a Game to the PartyManager
// once key lifecycle transitions occur.
type GameEvent struct {
	Type   GameEventType
	GameID GameID
}

// Game controls the runtime session between Clients once a Party starts.
// It runs its own goroutine, processes GameCommands from PartyManager,
// and reports lifecycle changes back to the PartyManager.
//
// Each Game instance owns its client references and sends
// outbound server messages via Client.SendMessage.
type Game struct {
	ID       GameID
	Clients  map[ClientID]*Client
	pm       *PartyManager
	p        *Party
	commands chan GameCommand
	mu       sync.RWMutex
}

// NewGame creates a new Game and initializes its command channel.
func NewGame(pm *PartyManager, p *Party, clients map[ClientID]*Client) *Game {
	return &Game{
		ID:       NewGameID(),
		Clients:  clients,
		pm:       pm,
		p:        p,
		commands: make(chan GameCommand, 64),
	}
}

// Start begins the Game's event loop as a goroutine.
func (g *Game) Start() {
	go g.Run()
}

// Run is the main loop of the Game.
// It processes incoming commands until a GameCommandEndGame is received.
func (g *Game) Run() {
	defer close(g.commands)

	for cmd := range g.commands {
		if g.handleCommand(cmd) {
			return
		}
	}
}

// handleCommand executes the given GameCommand and returns true if the
// Game should terminate.
func (g *Game) handleCommand(cmd GameCommand) bool {
	switch cmd.Type {
	case GameCommandStartGame:
		g.broadcast(ServerMessageGameStarted, ServerMessageGameStartedPayload{
			CountdownSeconds: 3,
			Timestamp:        time.Now().UnixMilli(),
		})

		g.pm.GameEvents <- GameEvent{
			Type:   GameEventStarted,
			GameID: g.ID,
		}

	case GameCommandEndGame:
		g.broadcast(ServerMessageGameOver, ServerMessageGameEndedPayload{
			Reason:   "manualEnd",
			WinnerID: "",
		})
		g.pm.GameEvents <- GameEvent{
			Type:   GameEventEnded,
			GameID: g.ID,
		}
		return true

	case GameCommandPlayerAction:
		pl := cmd.Payload.(GameCommandPlayerActionPayload)
		log.Printf("Game %s: Player %s action: %s", g.ID, pl.ClientID, pl.Action)

	case GameCommandClientDisconnect:
		pl := cmd.Payload.(GameCommandClientDisconnectPayload)
		g.mu.Lock()
		delete(g.Clients, pl.ClientID)
		clientCount := len(g.Clients)
		g.mu.Unlock()

		// End game if not enough players
		if clientCount < minPartySize {
			return g.handleCommand(GameCommand{Type: GameCommandEndGame})
		}
	}
	return false
}

// SendCommand safely queues a command for the Game goroutine.
// If the buffer is full, the command is dropped and logged.
func (g *Game) SendCommand(cmd GameCommand) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Game %s already closed, ignoring command (%s)",
				g.ID, cmd.Type)
		}
	}()

	select {
	case g.commands <- cmd:
	default:
		log.Printf("Game %s command buffer full (%s)", g.ID, cmd.Type)
	}
}

// broadcast marshals a payload and sends the resulting message
// to all connected Clients in the Game.
func (g *Game) broadcast(msgType ServerMessageType, payload any) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Game %s broadcast marshal error: %v", g.ID, err)
		return
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, c := range g.Clients {
		c.SendMessage(msgType, bytes)
	}
}

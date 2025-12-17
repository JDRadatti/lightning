package internal

import (
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
)

const (
	gameCommandBufferSize = 64
)

// GameID uniquely identifies a game instance.
type GameID string

// NewGameID returns a new randomly generated GameID.
func NewGameID() GameID {
	return GameID(uuid.New().String())
}

// ---------------------------------------------------------------------
// Communication
// ---------------------------------------------------------------------
//
// Game → PartyManager:
//   Games send GameEvents through the PartyManager’s `GameEvents` channel.
//
// Game ← Party:
//   A Game receives all commands from its owning Party via
//   its internal `commands` channel.
//
// Game → Clients:
//   A Game sends ServerMessages to Clients by broadcasting
//   through each Client’s `send` channel.
//
// IMPORTANT: PartyManager, Party, Game, and Client each manage their own
// data exclusively inside their goroutine. This avoids data races and
// keeps behavior deterministic across concurrent operations.
// ---------------------------------------------------------------------

// GameCommandType defines the list of commands that a Game can process.
type GameCommandType string

const (
	GameCommandStartGame GameCommandType = "startGame"
	GameCommandEndGame   GameCommandType = "endGame"
)

// GameCommand represents a single instruction sent to a Game
// through its `commands` channel.
type GameCommand struct {
	Type    GameCommandType
	Payload any
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

// ---------------------------------------------------------------------
// Game
// ---------------------------------------------------------------------

// Game controls the runtime session between Clients once a Party starts.
// It runs its own goroutine, processes GameCommands from its owning Party,
// and reports lifecycle changes back to the PartyManager.
//
// Each Game instance owns its client references and sends
// outbound server messages via Client.SendMessage.
type Game struct {
	ID       GameID
	Clients  map[*Client]bool
	pm       *PartyManager
	commands chan GameCommand
}

// NewGame creates a new Game, registers it with the given PartyManager,
// and initializes its command channel. The caller should start it with Run().
func NewGame(pm *PartyManager) *Game {
	return &Game{
		ID:       NewGameID(),
		Clients:  make(map[*Client]bool),
		pm:       pm,
		commands: make(chan GameCommand, gameCommandBufferSize),
	}
}

// Run starts the Game’s event loop and processes incoming commands.
// It exits when a GameCommandEndGame is received or upon channel close.
func (g *Game) Run() {
	defer close(g.commands)
	for cmd := range g.commands {
		if g.handleCommand(cmd) {
			break
		}
	}
	log.Printf("Game %s closed.", g.ID)
}

// handleCommand executes the given GameCommand and returns true if the
// Game should terminate. Commands are expected to originate from the Party.
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
	}
	return false
}

// AddClient registers a new Client as a participant in the Game.
func (g *Game) AddClient(c *Client) {
	g.Clients[c] = true
}

// SendCommand safely queues a command for the Game goroutine.
// If the buffer is full, the command is dropped and logged.
func (g *Game) SendCommand(cmd GameCommand) {
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
	for c := range g.Clients {
		c.SendMessage(msgType, bytes)
	}
}

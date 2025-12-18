package internal

import (
	"log"

	"github.com/google/uuid"
)

const (
	maxPartySize      = 6
	minPartySize      = 2
	commandBufferSize = 64
)

// PartyID uniquely identifies a Party instance.
type PartyID string

// NewPartyID creates a new PartyID.
func NewPartyID() PartyID {
	return PartyID(uuid.New().String())
}

// PartyMemberInfo contains relevant information for
// each party member, like connection status and
// whether they are a host or not. This information
// is sent to the client each time a member's status
// is updated.
type PartyMemberInfo struct {
	ID        string `json:"id"`
	IsHost    bool   `json:"isHost"`
	Connected bool   `json:"connected"`
}

// ---------------------------------------------------------------------
// Communication
// ---------------------------------------------------------------------
//
// Party → PartyManager:
//   A Party emits PartyEvent messages on the PartyManager’s `PartyEvents` channel.
//
// Party → Game:
//   A Party sends GameCommands to its Game via the Game’s `commands` channel.
//
// Party ← PartyManager:
//   A Party receives PartyCommands from the PartyManager through its own
//   internal `commands` channel.
//
// IMPORTANT: PartyManager, Party, Game, and Client each manage their own
// fields only within their goroutine. This ensures deterministic behavior
// and prevents data races.
// ---------------------------------------------------------------------

// PartyCommandType enumerates the supported command types a Party can handle.
type PartyCommandType string

const (
	PartyCommandAddClient    PartyCommandType = "addClient"
	PartyCommandRemoveClient PartyCommandType = "removeClient"
	PartyCommandStartGame    PartyCommandType = "startGame"
)

// PartyCommand represents a message sent to a Party goroutine.
type PartyCommand struct {
	Type    PartyCommandType
	Payload any
}

// PartyCommandRemoveClientPayload carries info for removing a Client from the Party.
type PartyCommandRemoveClientPayload struct {
	Client *Client
}

// PartyCommandAddClientPayload carries info for adding a Client to the Party.
type PartyCommandAddClientPayload struct {
	Client *Client
}

// PartyCommandStartGamePayload carries info for starting a game. Client must be
// the host.
type PartyCommandStartGamePayload struct {
	Client *Client
}

// ---------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------

// PartyEventType enumerates all event types a Party can send to the PartyManager.
type PartyEventType string

const (
	PartyEventClientJoined PartyEventType = "clientJoined"
	PartyEventClientLeft   PartyEventType = "clientLeft"
	PartyEventDisbanded    PartyEventType = "partyDisbanded"
)

// PartyEvent represents events sent from a Party to the PartyManager.
type PartyEvent struct {
	Type    PartyEventType
	PartyID PartyID
	Payload any
}

// PartyEventClientJoinedPayload carries the details of a join event.
type PartyEventClientJoinedPayload struct {
	ClientID ClientID
	Party    *Party
}

// PartyEventClientLeftPayload carries the details of a leave event.
type PartyEventClientLeftPayload struct {
	ClientID ClientID
}

// ---------------------------------------------------------------------
// Party
// ---------------------------------------------------------------------

// Party represents a pre‑game lobby containing multiple Clients.
// It manages membership, host assignment, and transitions to the Game stage.
//
// Parties run their own goroutine, handle commands from the PartyManager,
// and report events back upstream through the PartyManager.PartyEvents channel.
type Party struct {
	ID       PartyID
	Members  map[ClientID]*Client
	HostID   ClientID
	pm       *PartyManager
	commands chan PartyCommand
	state    string
	game     *Game
}

// NewParty creates a new Party, initializing its member map and command channel.
func NewParty(pm *PartyManager, id PartyID) *Party {
	return &Party{
		ID:       id,
		Members:  make(map[ClientID]*Client),
		pm:       pm,
		commands: make(chan PartyCommand, commandBufferSize),
	}
}

// Run starts the main loop for this Party.
// It listens for PartyCommands until the channel is closed.
func (p *Party) Run() {
	defer close(p.commands)
	for cmd := range p.commands {
		p.handleCommand(cmd)
	}
}

// handleCommand processes a PartyCommand and performs the corresponding action.
func (p *Party) handleCommand(cmd PartyCommand) {
	switch cmd.Type {

	case PartyCommandAddClient:
		pl := cmd.Payload.(PartyCommandAddClientPayload)
		c := pl.Client

		p.Members[c.ID] = c
		if len(p.Members) == 1 {
			p.HostID = c.ID
		}

		c.SendMessage(ServerMessagePartyJoined, ServerMessagePartyJoinedPayload{
			PartyID: p.ID,
		})
		p.broadcast(ServerMessageMemberUpdate, ServerMessageMemberUpdatePayload{
			Members: p.getMemberInfo(),
		})

		p.pm.PartyEvents <- PartyEvent{
			Type:    PartyEventClientJoined,
			PartyID: p.ID,
			Payload: PartyEventClientJoinedPayload{ClientID: c.ID, Party: p},
		}

	case PartyCommandRemoveClient:
		pl := cmd.Payload.(PartyCommandRemoveClientPayload)
		c := pl.Client
		delete(p.Members, c.ID)

		// Send PartyManager a confirmation
		p.pm.PartyEvents <- PartyEvent{
			Type:    PartyEventClientLeft,
			PartyID: p.ID,
			Payload: PartyEventClientLeftPayload{ClientID: c.ID},
		}

		// If host left, disband this party
		if len(p.Members) == 0 {
			p.pm.PartyEvents <- PartyEvent{Type: PartyEventDisbanded, PartyID: p.ID}
			c.SendMessage(ServerMessagePartyLeft, ServerMessagePartyLeftPayload{
				Reason: "party-disbanded",
			})
			return
		}

		// Check if host left
		if p.HostID == c.ID {
			if len(p.Members) == 0 {
				// No members left, disband
				p.pm.PartyEvents <- PartyEvent{
					Type:    PartyEventDisbanded,
					PartyID: p.ID,
				}
				return
			} else {
				// Pick the first remaining member as new host
				for id := range p.Members {
					p.HostID = id
					break
				}
			}
		}

		c.SendMessage(ServerMessagePartyLeft, ServerMessagePartyLeftPayload{
			Reason: "self-initiated",
		})

		p.broadcast(ServerMessageMemberUpdate,
			ServerMessageMemberUpdatePayload{
				Members: p.getMemberInfo(),
			},
		)

	case PartyCommandStartGame:
		pl := cmd.Payload.(PartyCommandStartGamePayload)
		c := pl.Client

		// Only host can start the game
		if c.ID != p.HostID {
			c.SendError(ErrorCodeNotPartyHost, "Not party host.", ClientMessageStartGame)
			return
		}
		// Only start game if there is enought players
		if len(p.Members) < minPartySize {
			c.SendError(ErrorCodeNotEnoughMembers, "Party size is too small.", ClientMessageStartGame)
			return
		}

		game := NewGame(p.pm)
		p.game = game
		// This is safe because it happens before goroutine starts
		for _, c := range p.Members {
			game.AddClient(c)
		}
		go game.Run()
		game.SendCommand(GameCommand{Type: GameCommandStartGame})
	}
}

// IsFull checks if the Party has reached its maximum member limit.
func (p *Party) IsFull() bool {
	return len(p.Members) >= maxPartySize
}

// SendCommand safely queues a command for the Party goroutine.
// Drops the command if the buffer is full.
func (p *Party) SendCommand(cmd PartyCommand) {
	select {
	case p.commands <- cmd:
	default:
		log.Printf("Party %s command buffer full (%s)", p.ID, cmd.Type)
	}
}

// broadcast sends a ServerMessage to all Clients currently in the Party.
func (p *Party) broadcast(msgType ServerMessageType, payload any) {
	for _, c := range p.Members {
		c.SendMessage(msgType, payload)
	}
}

func (p *Party) getMemberInfo() []PartyMemberInfo {
	partyMembers := make([]PartyMemberInfo, 0, len(p.Members))
	for _, m := range p.Members {
		partyMembers = append(partyMembers, PartyMemberInfo{
			ID:        string(m.ID),
			IsHost:    p.HostID == m.ID,
			Connected: true,
		})
	}
	return partyMembers
}

package internal

import (
	"log"
)

// buffer size for PartyManager channels
const partyManagerBufferSize = 64

// ---------------------------------------------------------------------
// Communication
// ---------------------------------------------------------------------
//
// PartyManager → Party:
//   PartyManager sends PartyCommands through each Party's `commands` channel.
//
// PartyManager ← Party,
// PartyManager ← Game:
//   PartyManager receives events from both Parties and Games through
//   the `PartyEvents` and `GameEvents` channels.
//
// IMPORTANT: PartyManager, Party, Game, and Client each manage their own
// fields only within their respective goroutine.
// This ensures consistent access across goroutines and prevents data races.
// ---------------------------------------------------------------------

// PartyManagerCommandType lists all commands sent to the PartyManager.
type PartyManagerCommandType string

const (
	PartyManagerCommandAddClient    PartyManagerCommandType = "addClient"
	PartyManagerCommandRemoveClient PartyManagerCommandType = "removeClient"
	PartyManagerCommandStartGame    PartyManagerCommandType = "startGame"
)

// PartyManagerCommand wraps a command and its payload,
// used for communicating with the PartyManager goroutine.
type PartyManagerCommand struct {
	Type    PartyManagerCommandType
	Payload any
}

// PartyManagerAddClientPayload is used when a Client joins the queue
// or attempts to join a specific Party.
type PartyManagerAddClientPayload struct {
	Client  *Client
	PartyID PartyID
}

// PartyManagerRemoveClientPayload is used when a Client wants to leave
// a Party or disconnects.
type PartyManagerRemoveClientPayload struct {
	Client *Client
}

// PartyManagerStartGamePayload is sent when a Client wants to
// start a Game.
type PartyManagerStartGamePayload struct {
	Client *Client
}

// ---------------------------------------------------------------------
// PartyManager
// ---------------------------------------------------------------------

// PartyManager owns all Parties, manages the public queue,
// and receives events from both Parties and Games.
//
// It runs as its own goroutine, processing commands through its internal
// `Commands` channel.
type PartyManager struct {
	PublicParty *Party
	Parties     map[PartyID]*Party
	Members     map[ClientID]PartyID
	PublicQueue chan *Client

	PartyEvents chan PartyEvent
	GameEvents  chan GameEvent
	Commands    chan PartyManagerCommand
}

// NewPartyManager starts and returns a new PartyManager.
func NewPartyManager() *PartyManager {
	pm := &PartyManager{
		PublicParty: nil,
		Parties:     make(map[PartyID]*Party),
		Members:     make(map[ClientID]PartyID),
		PublicQueue: make(chan *Client, partyManagerBufferSize),
		PartyEvents: make(chan PartyEvent, partyManagerBufferSize),
		GameEvents:  make(chan GameEvent, partyManagerBufferSize),
		Commands:    make(chan PartyManagerCommand, partyManagerBufferSize),
	}
	go pm.Run()
	return pm
}

// Run is the main loop of the PartyManager.
// It processes incoming commands, queue joins, and events from Parties and Games.
func (pm *PartyManager) Run() {
	for {
		select {
		case cmd := <-pm.Commands:
			pm.handleCommand(cmd)
		case c := <-pm.PublicQueue:
			pm.handleQueueJoin(c)
		case evt := <-pm.PartyEvents:
			pm.handlePartyEvent(evt)
		case evt := <-pm.GameEvents:
			pm.handleGameEvent(evt)
		}
	}
}

// handleCommand routes and processes PartyManagerCommands.
// Commands are sent from Clients.
func (pm *PartyManager) handleCommand(cmd PartyManagerCommand) {
	switch cmd.Type {
	case PartyManagerCommandAddClient:
		payload := cmd.Payload.(PartyManagerAddClientPayload)
		client, partyID := payload.Client, payload.PartyID

		if partyID == "" {
			// client requested to join public queue
			select {
			case pm.PublicQueue <- client:
				client.SendMessage(ServerMessageQueueJoined, map[string]any{})
			default:
				client.SendError(ErrorCodeQueueFull, "Queue is full.", ClientMessageJoin)
			}
			return
		}

		// attempt to join specific party
		if p, ok := pm.Parties[partyID]; ok {
			p.SendCommand(PartyCommand{
				Type: PartyCommandAddClient,
				Payload: PartyCommandAddClientPayload{
					Client: client,
				},
			})
		} else {
			client.SendError(ErrorCodePartyNotFound, "Party not found.", ClientMessageJoin)
		}

	case PartyManagerCommandRemoveClient:
		payload := cmd.Payload.(PartyManagerRemoveClientPayload)
		client := payload.Client

		pm.removeClientFromParty(client, ClientMessageLeave)

	case PartyManagerCommandStartGame:
		payload := cmd.Payload.(PartyManagerStartGamePayload)
		client := payload.Client
		// get the client's party
		pid, exists := pm.Members[client.ID]
		if !exists {
			client.SendError(ErrorCodeNotInSession, "No session found.", ClientMessageStartGame)
			return
		}

		// attempt to get the party
		p, exists := pm.Parties[pid]
		if !exists && p != nil {
			client.SendError(ErrorCodePartyNotFound, "Party not found", ClientMessageJoin)
			return
		}

		p.SendCommand(PartyCommand{
			Type: PartyCommandStartGame,
			Payload: PartyCommandStartGamePayload{
				Client: client,
			},
		})

	default:
		log.Printf("Unknown party manager command %s", cmd.Type)

	}
}

// handleQueueJoin pulls clients off the public queue,
// creates a new Party if needed, and adds the client to that Party.
func (pm *PartyManager) handleQueueJoin(c *Client) {
	if pm.PublicParty == nil || pm.PublicParty.IsFull() {
		pid := NewPartyID()
		pm.PublicParty = NewParty(pm, pid)
		pm.Parties[pid] = pm.PublicParty
		go pm.PublicParty.Run()
	}
	pm.PublicParty.SendCommand(PartyCommand{
		Type: PartyCommandAddClient,
		Payload: PartyCommandAddClientPayload{
			Client: c,
		},
	})
}

// handlePartyEvent responds to events emitted by Parties.
//
// Most PartyManager state should be updated here. For example,
// Only add a Client as a Member when they were sucessfully
// added to a party (i.e. when PartyManager recieves a
// PartyEventClientJoinedPayload)
func (pm *PartyManager) handlePartyEvent(evt PartyEvent) {
	switch evt.Type {
	case PartyEventClientJoined:
		pl := evt.Payload.(PartyEventClientJoinedPayload)
		pm.Members[pl.ClientID] = evt.PartyID
		log.Printf("Client %s joined party %s", pl.ClientID, evt.PartyID)

	case PartyEventClientLeft:
		pl := evt.Payload.(PartyEventClientLeftPayload)
		delete(pm.Members, pl.ClientID)
		log.Printf("Client left party %s", evt.PartyID)

	case PartyEventDisbanded:
		delete(pm.Parties, evt.PartyID)
		log.Printf("Party disbanded %s", evt.PartyID)

	default:
		log.Printf("Unknonwn party event type %s", evt.Type)
	}
}

// handleGameEvent responds to events emitted by Games.
func (pm *PartyManager) handleGameEvent(evt GameEvent) {
	switch evt.Type {
	case GameEventStarted:
		log.Printf("Game %s started", evt.GameID)
	case GameEventEnded:
		log.Printf("Game %s ended", evt.GameID)
	default:
		log.Printf("Unknown game event type %s", evt.Type)
	}
}

// SendCommand safely queues a command for the PartyManager goroutine.
// If the buffer is full, the command is dropped.
func (pm *PartyManager) SendCommand(cmd PartyManagerCommand) {
	select {
	case pm.Commands <- cmd:
	default:
		log.Println("PartyManager command buffer full")
	}
}

// removeClientFromParty removes a client from a party
func (pm *PartyManager) removeClientFromParty(c *Client, cmt ClientMessageType) {
	pid, exists := pm.Members[c.ID]
	if !exists {
		c.SendError(ErrorCodeNotInSession, "Not in any party", cmt)
		return
	}

	p, exists := pm.Parties[pid]
	if !exists {
		delete(pm.Members, c.ID)
		c.SendError(ErrorCodePartyNotFound, "Party not found", cmt)
		return
	}

	p.SendCommand(PartyCommand{
		Type:    PartyCommandRemoveClient,
		Payload: PartyCommandRemoveClientPayload{Client: c},
	})
}

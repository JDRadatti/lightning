package internal

import (
	"log"
	"time"
)

// buffer size for PartyManager channels
const (
	partyManagerBufferSize = 64
	cleanupInterval        = 10 * time.Second
	abandonmentTimeout     = 15 * time.Second
)

// PartyManagerCommandType lists all commands sent to the PartyManager.
type PartyManagerCommandType string

const (
	PartyManagerCommandAddClient        PartyManagerCommandType = "addClient"
	PartyManagerCommandRemoveClient     PartyManagerCommandType = "removeClient"
	PartyManagerCommandStartGame        PartyManagerCommandType = "startGame"
	PartyManagerCommandDisconnectClient PartyManagerCommandType = "clientDisconnected"
	PartyManagerCommandCleanup          PartyManagerCommandType = "cleanUp"
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
	Client    *Client   // Current Client Session
	ClientID  ClientID  // ClientID attempting to reconnect to
	PartyID   PartyID   // PartyID attempting to join
	SecretKey SecretKey // SecretKey, for reconnecting
}

// PartyManagerRemoveClientPayload is used when a Client wants to leave
// a Party or disconnects.
type PartyManagerRemoveClientPayload struct {
	Client *Client
}

// PartyManagerDisconnectPayload is used when a Client wants to leave
// a Party or disconnects.
type PartyManagerDisconnectPayload struct {
	Client *Client
}

// PartyManagerStartGamePayload is sent when a Client wants to
// start a Game.
type PartyManagerStartGamePayload struct {
	Client *Client
}

// AbandonedClient keeps track of important information related to
// a client that was disconnected
type AbandonedClient struct {
	Client      *Client
	AbandonedAt time.Time
}

// PartyManager owns all Parties, manages the public queue,
// and receives events from Games.
//
// It runs as its own goroutine, processing commands through its internal
// `Commands` channel.
type PartyManager struct {
	PublicParty *Party
	Parties     map[PartyID]*Party
	Members     map[ClientID]PartyID
	Abandoned   map[ClientID]AbandonedClient
	Games       map[GameID]*Game

	PublicQueue chan *Client
	GameEvents  chan GameEvent
	Commands    chan PartyManagerCommand

	AbandonmentTimeout time.Duration
	CleanupInterval    time.Duration
}

// NewPartyManager starts and returns a new PartyManager.
func NewPartyManager() *PartyManager {
	return NewPartyManagerWithTimeouts(abandonmentTimeout, cleanupInterval)
}

func NewPartyManagerWithTimeouts(abandonmentTimeout, cleanupInterval time.Duration) *PartyManager {
	pm := &PartyManager{
		PublicParty:        nil,
		Parties:            make(map[PartyID]*Party),
		Members:            make(map[ClientID]PartyID),
		Abandoned:          make(map[ClientID]AbandonedClient),
		Games:              make(map[GameID]*Game),
		PublicQueue:        make(chan *Client, partyManagerBufferSize),
		GameEvents:         make(chan GameEvent, partyManagerBufferSize),
		Commands:           make(chan PartyManagerCommand, partyManagerBufferSize),
		AbandonmentTimeout: abandonmentTimeout,
		CleanupInterval:    cleanupInterval,
	}
	go pm.Run()
	go pm.cleanupAbandoned()
	return pm
}

// Run is the main loop of the PartyManager.
// It processes incoming commands and queue joins.
func (pm *PartyManager) Run() {
	for {
		select {
		case cmd := <-pm.Commands:
			pm.handleCommand(cmd)
		case c := <-pm.PublicQueue:
			pm.handleQueueJoin(c)
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
		client := payload.Client     // Client making join request
		partyID := payload.PartyID   // PartyID of party client is requesting to join
		clientID := payload.ClientID // Client ID of disconnected client (for reconnection)
		secret := payload.SecretKey  // Secret of disconnected client (for reconnection)

		// Check if client was abandoned and is within reconnection window.
		//
		// If a client is attempting to reconnect, they will be automatically reconnected
		// to the same party, if it still exists.
		if abandonedClient, wasAbandoned := pm.Abandoned[clientID]; wasAbandoned {
			if time.Since(abandonedClient.AbandonedAt) < pm.AbandonmentTimeout && secret == abandonedClient.Client.Secret {

				// Update abandoned client with new connection and send channel
				oldClient := abandonedClient.Client
				oldClient.conn = client.conn
				oldClient.send = client.send
				client = oldClient

				// Check if client was in party
				realPartyID, exists := pm.Members[clientID]
				if !exists {
					client.SendError(ErrorCodeSessionExpired, "Session expired.", ClientMessageJoin)
					delete(pm.Abandoned, clientID)
					return
				}
				party, partyExists := pm.Parties[realPartyID]
				if !partyExists {
					// Party was disbanded while they were disconnected
					delete(pm.Abandoned, clientID)
					delete(pm.Members, clientID)
					client.SendError(ErrorCodePartyNotFound, "Party no longer exists.", ClientMessageJoin)
					return
				}

				// Party exists, reconnect to it
				party.MarkClientConnected(clientID)
				client.mu.Lock()
				client.game = party.game
				client.mu.Unlock()

				// Notify client that they re-joined the party
				client.SendMessage(ServerMessagePartyJoined, ServerMessagePartyJoinedPayload{
					PartyID: partyID,
				})
				// Notify other party members
				party.broadcast(ServerMessageMemberUpdate, ServerMessageMemberUpdatePayload{
					Members: party.getMemberInfo(),
				})

				delete(pm.Abandoned, clientID)
				log.Printf("Client %s reconnected", client.ID)
				return // Done with reconnection
			} else {
				client.SendError(ErrorCodeSessionExpired, "Reconnection window expired.", ClientMessageJoin)
				delete(pm.Abandoned, clientID)
				return
			}
		}

		// Check if client is already in a party
		if _, inParty := pm.Members[client.ID]; inParty {
			client.SendError(ErrorCodeAlreadyInParty, "Already In Party.", ClientMessageJoin)
		}

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
			p.AddClient(client)
			pm.Members[client.ID] = partyID

			client.SendMessage(ServerMessagePartyJoined, ServerMessagePartyJoinedPayload{
				PartyID: partyID,
			})
			p.broadcast(ServerMessageMemberUpdate, ServerMessageMemberUpdatePayload{
				Members: p.getMemberInfo(),
			})

			log.Printf("Client %s joined party %s", client.ID, partyID)
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
		if !exists {
			client.SendError(ErrorCodePartyNotFound, "Party not found", ClientMessageStartGame)
			return
		}

		// Only host can start the game
		if client.ID != p.HostID {
			client.SendError(ErrorCodeNotPartyHost, "Not party host.", ClientMessageStartGame)
			return
		}
		// Only start game if there is enough players
		if len(p.Members) < minPartySize {
			client.SendError(ErrorCodeNotEnoughMembers, "Party size is too small.", ClientMessageStartGame)
			return
		}

		// Create and start game
		clientsMap := make(map[ClientID]*Client)
		for cid, member := range p.Members {
			clientsMap[cid] = member.Client
		}

		game := NewGame(pm, clientsMap)
		p.game = game
		pm.Games[game.ID] = game

		// Assign game to each client
		for _, member := range p.Members {
			member.Client.mu.Lock()
			member.Client.game = game
			member.Client.mu.Unlock()
		}

		game.Start()
		game.SendCommand(GameCommand{Type: GameCommandStartGame})

		log.Printf("Game %s started in party %s", game.ID, pid)

	case PartyManagerCommandDisconnectClient:
		payload := cmd.Payload.(PartyManagerDisconnectPayload)
		client := payload.Client

		// Tell the party the client disconnected
		if partyID, exists := pm.Members[client.ID]; exists {
			if party, partyExists := pm.Parties[partyID]; partyExists {
				party.MarkClientDisconnected(client.ID)

				// Notify other party members
				party.broadcast(ServerMessageMemberUpdate, ServerMessageMemberUpdatePayload{
					Members: party.getMemberInfo(),
				})
			}
		}

		// Clear game reference
		client.mu.Lock()
		client.game = nil
		client.mu.Unlock()

		// Mark as abandoned
		pm.Abandoned[client.ID] = AbandonedClient{
			Client:      client,
			AbandonedAt: time.Now(),
		}
		log.Printf("Client %s disconnected. Waiting %v to see if they return...", client.ID, pm.AbandonmentTimeout)

	case PartyManagerCommandCleanup:
		now := time.Now()
		for cid, abandonedClient := range pm.Abandoned {
			if now.Sub(abandonedClient.AbandonedAt) > pm.AbandonmentTimeout {
				delete(pm.Abandoned, cid)

				// notify the game that the player is permanently gone
				if partyID, exists := pm.Members[cid]; exists {
					if party, partyExists := pm.Parties[partyID]; partyExists {
						if party.game != nil {
							party.game.SendCommand(GameCommand{
								Type: GameCommandClientDisconnect,
								Payload: GameCommandClientDisconnectPayload{
									ClientID: cid,
								},
							})
						}
					}
				}
				pm.removeClientFromParty(&Client{ID: cid}, "")
				log.Printf("Client %s permanently removed after abandonment", cid)
			}
		}

	default:
		log.Printf("Unknown party manager command %s", cmd.Type)
	}
}

// handleQueueJoin pulls clients off the public queue,
// creates a new Party if needed, and adds the client to that Party.
func (pm *PartyManager) handleQueueJoin(c *Client) {
	if pm.PublicParty == nil || pm.PublicParty.IsFull() {
		pid := NewPartyID()
		pm.PublicParty = NewParty(pid)
		pm.Parties[pid] = pm.PublicParty
	}
	pm.PublicParty.AddClient(c)
	pm.Members[c.ID] = pm.PublicParty.ID

	c.SendMessage(ServerMessagePartyJoined, ServerMessagePartyJoinedPayload{
		PartyID: pm.PublicParty.ID,
	})
	pm.PublicParty.broadcast(ServerMessageMemberUpdate,
		ServerMessageMemberUpdatePayload{
			Members: pm.PublicParty.getMemberInfo(),
		},
	)

	log.Printf("Client %s joined public queue (party %s)", c.ID, pm.PublicParty.ID)
}

// handleGameEvent responds to events emitted by Games.
func (pm *PartyManager) handleGameEvent(evt GameEvent) {
	switch evt.Type {
	case GameEventStarted:
		log.Printf("Game %s started", evt.GameID)
	case GameEventEnded:
		log.Printf("Game %s ended", evt.GameID)
		// Remove game reference from all clients in finished game
		if game, exists := pm.Games[evt.GameID]; exists {
			for _, client := range game.Clients {
				client.mu.Lock()
				client.game = nil
				client.mu.Unlock()
			}
		}
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

	p.RemoveClient(c.ID)
	delete(pm.Members, c.ID)

	// Send PartyManager a confirmation
	if cmt != "" {
		c.SendMessage(ServerMessagePartyLeft, ServerMessagePartyLeftPayload{
			Reason: "self-initiated",
		})
	}

	// If no members left, disband this party
	if p.IsEmpty() {
		delete(pm.Parties, pid)

		// If this was the public party, clear the reference
		if pm.PublicParty != nil && pm.PublicParty.ID == pid {
			pm.PublicParty = nil
		}

		log.Printf("Party %s disbanded", pid)
		return
	}

	p.broadcast(ServerMessageMemberUpdate,
		ServerMessageMemberUpdatePayload{
			Members: p.getMemberInfo(),
		},
	)

	log.Printf("Client left party %s", pid)
}

// cleanupAbandoned is a goroutine that sends a
// PartyManagerCommandCleanup every cleanupInterval
func (pm *PartyManager) cleanupAbandoned() {
	ticker := time.NewTicker(pm.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		pm.SendCommand(PartyManagerCommand{
			Type: PartyManagerCommandCleanup,
		})
	}
}

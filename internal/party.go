package internal

import (
	"github.com/google/uuid"
)

const (
	maxPartySize = 6
	minPartySize = 2
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
	ID          string `json:"id"`
	IsHost      bool   `json:"isHost"`
	IsConnected bool   `json:"isConnected"`
}

// PartyMember carries info related to a client in a Party
type PartyMember struct {
	Client      *Client
	IsConnected bool
}

// Party represents a pre‑game lobby containing multiple Clients.
// It is now just a data structure managed by PartyManager.
type Party struct {
	ID      PartyID
	Members map[ClientID]*PartyMember
	HostID  ClientID
	game    *Game
}

// NewParty creates a new Party, initializing its member map.
func NewParty(id PartyID) *Party {
	return &Party{
		ID:      id,
		Members: make(map[ClientID]*PartyMember),
	}
}

// AddClient adds a client to the party
func (p *Party) AddClient(c *Client) {
	p.Members[c.ID] = &PartyMember{Client: c, IsConnected: true}
	if len(p.Members) == 1 {
		p.HostID = c.ID
	}
}

// RemoveClient removes a client from the party
func (p *Party) RemoveClient(cid ClientID) {
	delete(p.Members, cid)

	// Check if host left
	if p.HostID == cid {
		// Pick the first remaining member as new host
		for id := range p.Members {
			p.HostID = id
			break
		}
	}
}

// MarkClientDisconnected marks a client as disconnected
func (p *Party) MarkClientDisconnected(cid ClientID) bool {
	if member, exists := p.Members[cid]; exists {
		member.IsConnected = false
		return true
	}
	return false
}

// MarkClientConnected marks a client as connected
func (p *Party) MarkClientConnected(cid ClientID) bool {
	if member, exists := p.Members[cid]; exists {
		member.IsConnected = true
		return true
	}
	return false
}

// IsFull checks if the Party has reached its maximum member limit.
func (p *Party) IsFull() bool {
	return len(p.Members) >= maxPartySize
}

// IsEmpty checks if the party has no members
func (p *Party) IsEmpty() bool {
	return len(p.Members) == 0
}

// broadcast sends a ServerMessage to all Clients currently in the Party.
func (p *Party) broadcast(msgType ServerMessageType, payload any) {
	for _, m := range p.Members {
		m.Client.SendMessage(msgType, payload)
	}
}

// getMemberInfo returns the PartyMemberInfo for all members
func (p *Party) getMemberInfo() []PartyMemberInfo {
	partyMembers := make([]PartyMemberInfo, 0, len(p.Members))
	for _, m := range p.Members {
		partyMembers = append(partyMembers, PartyMemberInfo{
			ID:          string(m.Client.ID),
			IsHost:      p.HostID == m.Client.ID,
			IsConnected: m.IsConnected,
		})
	}
	return partyMembers
}

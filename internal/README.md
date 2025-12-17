# Overview

There are four main constructs that handle creating and managing games.
1. `Client`: Connection point between client and server.
2. `PartyManager`: Manages all Party and Game objects. 
3. `Party`: Pre-game session.
4. `Game`: Active gameplay session. Talks directly with clients.

The communication channels are as follows: 

```
+-------------------+             +-------------------+           
|  WebSocketHandler | <---------- | Client (Browser)  |           
+-------------------+             +-------------------+          
          |               
          | PartyManagerCommand: i.e. PartyManagerCommandJoinParty              
          |               (via PartyManager.commands chan)
          V                                  
+-------------------+                        
|                   |
|   PartyManager    | pm.partyEvents <---------------------------------+  
|                   | pm.gameEvents <-----------------------------+    | 
-------------------+                                              |    | 
    |                                                             |    | 
    |                                                             |    | 
    | PartyCommands: i.e. PartyCommandAddClient                   |    |  
    |     (via Party.commands chan)                               |    | 
    V                                                             |    | 
+-------------------+                                             |    | 
|                   |                                             |    | 
|     Party         | ---------------PartyEvent------------------------+ 
|                   |                                             |
+-------------------+                                             |
    |                                                             |
    |                                                             |
    | GameCommand: i.e. GameCommandStartGame                      | 
    |     (via Game.commands chan)                                |
    V                                                             |
+-------------------+                                             |
|                   |                                             |
|      Game         |----------------GameEvent--------------------+
|                   |                                             
+-------------------+                                             


Note: ServerMessage's are sent via Client.SendMessage or Client.SendError or Party.broadcast
``` 


# API Documentation

Message definitions can be found in [message.go](./message.go)

### Message Structure 

Client -> Server

```
{
  "type": "ClientMessage",
  "payload": {
    "key1": "value1",
    "key2": "value2"
  }
}
```

Server -> Client 
```
{
  "type": "ServerMessage",
  "payload": {
    "key1": "value1",
    "key2": "value2"
  }
}
```

## Error Handling (Server -> Client)

Provides details about a failure.

- Type: errorMessage
- Payload:
	- code (string): A short code indicating the type of error.
	- message (string): A human-readable description of the error.
	- requestType (string, optional): The type of the client message that triggered this error, if applicable.

```
{
  "type": "error",
  "payload": {
    "code": "partyFull",
    "message": "The party you tried to join is already full.",
    "requestType": "join",
  }
}
```

## Client -> Server (ClientMessage)

### Join

Client requests to join public queue or a specific party 

- Type: join
- Payload:
	-  partyID (string, optional): ID of desired party.


```
// To join general queue
{
  "type": "join",
  "payload": {}
}
```
```
// To join a specific party
{
  "type": "join",
  "payload": {
    "partyID": "party-123"
  }
}
```

Possible Server Responses:

- Success: queueJoined, partyJoined
- Failure: errorMessage
	- code: "partyNotFound": If partyID is provided but doesn't exist.
	- code: "partyFull": If partyID is provided and the party is at its member limit.
	- code: "alreadyInParty": If the client is already in a party.
	- code: "invalidRequest": If the payload could not be read or is invalid.

### Leave Success


Client requests to leave either the queue or their current party.


- Type: leaveSuccess
- Payload: {}

```
{
  "type": "leaveSuccess",
  "payload": {}
}
```

Possible Server Responses:

- Success: leaveSuccess
- Failure: errorMessage
	- code: "notInSession": If the client is not currently in a party or the queue.
	- code: "invalidRequest": If the payload could not be read or is invalid.

### Start Game

Party host requests to start the game.

- Type: startGame
- Payload: {}

```
{
  "type": "startGame",
  "payload": {}
}
```

Possible Server Responses:

- Success: gameStarted
- Failure: errorMessage
	- code: "notPartyHost": If the client making the request is not the party host.
	- code: "notEnoughMembers": If the party does not meet the minimum member requirement to start a game.
    - code: "invalidState": The action is not allowed given the client's current status (e.g., not in a game, not in a party, game already started).
	- code: "invalidRequest": If the payload could not be read or is invalid.

### Play Card

Client requests to play a card. 

- Type: playCard
- Payload: {}

```
{
  "type": "playCard",
  "payload": {}
}
```

Possible Server Responses:

- Success: cardPlayed
- Failure: errorMessage
	- code: "notYourTurn": If the client attempts to play a card when it's not their designated turn.
    - code: "invalidState": The action is not allowed given the client's current status (e.g., not in a game, not in a party, game not started).
	- code: "invalidRequest": If the payload could not be read or is invalid.

### Match Card 

Client notifies that they think their card matches another Client's card.

- Type: matchCard
- Payload: 
    - targetClientID (string): ID of Client who played the card that the current player thinks matches.

```
{
  "type": "matchCard",
  "payload": {
    "targetClientID": "client-123"
  }
}
```

Possible Server Responses:

- Success: matchUpdate
- Failure: errorMessage
	- code: "invalidMatch": If matchClientID does not correspond to a valid player.
    - code: "invalidState": The action is not allowed given the client's current status (e.g., not in a game, not in a party, game not started).
	- code: "invalidRequest": If the payload could not be read or is invalid.

## Server -> Client (ServerMessage)

### Connect Success


Server sends this immediately after a new WebSocket connection is successfully established.

- Type: connectSuccess
- Payload:
	- clientId (string): The unique id of this client for their current session.
    - name (string): Unique name given by server.

```
{
  "type": "connectSuccess",
  "payload": {
    "clientId": "client-123",
    "name": "randomName",
  }
}
```

### Queue Joined

Server notifies the Client they are in the queue.

- Type: queueJoined
- Payload: {}

```
{
  "type": "queueJoined",
  "payload": {}
}
```

### Party Joined

Server notifies a client that they have successfully joined a party.


- Type: partyJoined
- Payload:
    - partyID (string): ID of joined Party.

```
{
  "type": "partyJoined",
  "payload": {
    "partyID": "party-123",
  }
}
```

### Party Member Update

Server notifies all party members about changes in the party (e.g., new member joins or leaves, host change, connection update).

- Type: memberUpdate
- Payload:
	- members (array of PartyMember objects): List of all current party members.
```
{
  "type": "memberUpdate",
  "payload": {
    "members": [
      { "id": "client-host", "name": "HostClient", "isHost": true, "connected": true, "turnPosition": 0 },
      { "id": "client-joiner", "name": "JoiningClient", "isHost": false, "connected": true, "turnPosition": 1 },
      { "id": "client-new", "name": "NewMember", "isHost": false, "connected": true, "turnPosition": 2 }
    ]
  }
}
```

### Party Left

Server notifies Client they have left a party.

- Type: partyLeft
- Payload:
    - reason (string): Reason member left the party.

```
{
  "type": "partyLeft",
  "payload": {
    "reason": "self-initiated"
  }
}
```

### Game Started

Server notifies all clients in a party that the game is starting.

- Type: gameStarted
- Payload:
    - countdownSeconds (number, optional): The time in seconds until the game starts.
    - timestamp (number, optional): Unix timestamp in milliseconds UTC when the countdown begins. 

```
{
  "type": "gameStarted",
  "payload": {
    "countdownSeconds: 10,
    "timestamp": 1678886400000

  }
}
```


### Turn Update 

Server notifies all clients whose turn it is.

- Type: turnUpdate
- Payload:
	- turnClientID (string): The ID of the client whose turn it is
    - turnNumber (number, optional): The current turn number.
    - turnTimeoutSeconds (number, optional): The time in seconds the client has to make a move.
    - timestamp (number, optional): Unix timestamp in milliseconds UTC when the turn started. 

```
{
  "type": "turnUpdate",
  "payload": {
    "turnClientID": "client-abc",
    "turnNumber": 3,
    "turnTimeoutSeconds": 10,
    "timestamp": 1678886400000
  }
}
```

### Card Played 

Server broadcasts to all clients that a card has been played.

- Type: cardPlayed
- Payload:
	- clientID (string): The ID of the client who played the card.
	- cardID (string): The ID of the card played.
    - gameState (object): Information such as all the client's top card and number of cards in their stack.

```
{
  "type": "cardPlayed",
  "payload": {
    "clientID": "client-123",
    "cardID": "card-abc",
    "gameState": {
      "client-123": { "topCard": "card-abc", "stackSize": 2,  "deckSize": 17 },
      "client-456": { "topCard": "card-def", "stackSize": 17, "deckSize": 1 },
      "client-789": { "topCard": "card-ghi", "stackSize": 23, "deckSize": 3 }
    },
  }
}
```

### Match Update

Server broadcasts to all clients result of a `matchCard` message.

- Type: matchUpdate
- Payload:
	- targetClientID (string): The ID of the client who played the card.
	- initiatorClientID (string): The ID of the client that sent the `matchCard` message.
    - success (bool): true if the match was valid, and `initiatorClientID` won the hand.
    - gameState (object): Information such as all the client's top card and number of cards in their stack.

```
{
  "type": "matchUpdate",
  "payload": {
    "targetClientID": "client-123",
    "initiatorClientID": "client-456",
    "success": true,
    "gameState": {
      "client-123": { "topCard": "", "stackSize": 0, "deckSize": 36 },
      "client-456": { "topCard": "", "stackSize": 0, "deckSize": 1 },
      "client-789": { "topCard": "card-ghi", "stackSize": 23, "deckSize": 39 }
    },
  }
}
```

```
{
  "type": "cardPlayed",
  "payload": {
    "targetClientID": "client-123",
    "initiatorClientID": "client-456",
    "success": false,
    "gameState": {
      "client-123": { "topCard": "", "stackSize": 0,  "deckSize": 17 },
      "client-456": { "topCard": "", "stackSize": 0, "deckSize": 43 },
      "client-789": { "topCard": "", "stackSize": 0, "deckSize": 3 }
    },
  }
}
```

### Game Over

Server notifies all clients that the game has ended.

- Type: gameOver
- Payload:
	- winnerID (string, optional): The ID of the winning client, if any.
    - reason (string): Reason for the game being over (e.g. winner finished their deck, server exploded)

```
{
  "type": "gameOver",
  "payload": {
    "winnerID": "client-abc",
    "reason": "deckEmpty"
  }
}
```

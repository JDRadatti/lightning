# Overview

There are four main constructs that handle creating and managing games.
1. `Client`: Connection point between client and server.
2. `PartyManager`: Manages Party and Game state. 
3. `Party`: Pre-game session.
4. `Game`: Active gameplay session. Talks directly with clients.

The communication channels are as follows: 

```
PartyManager -> Game (Game.commands)
Game -> PartyManager (PartyManager.GameEvents)

Game -> Client
PartyManager -> Client
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

## Errors (Server -> Client)

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

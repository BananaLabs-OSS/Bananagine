// Package registry defines the transport-neutral server-registry capability
// used by Bananagine compositions. Values intentionally carry both JSON and
// MessagePack tags so HTTP and Pulp sibling adapters share one contract.
package registry

import "fmt"

const (
	Capability = "bananagine.registry.v1"

	FnRegister    = "bananagine.registry.v1.register"
	FnList        = "bananagine.registry.v1.list"
	FnGet         = "bananagine.registry.v1.get"
	FnUpdate      = "bananagine.registry.v1.update"
	FnUnregister  = "bananagine.registry.v1.unregister"
	FnSetPlayers  = "bananagine.registry.v1.set_players"
	FnPutMatch    = "bananagine.registry.v1.put_match"
	FnRemoveMatch = "bananagine.registry.v1.remove_match"
)

type ServerType string

const (
	TypeLobby ServerType = "lobby"
	TypeGame  ServerType = "game"
)

type MatchStatus string

const (
	StatusReady    MatchStatus = "ready"
	StatusBusy     MatchStatus = "busy"
	StatusStarting MatchStatus = "starting"
)

type Match struct {
	Status  MatchStatus `json:"status" msgpack:"status"`
	Need    int         `json:"need" msgpack:"need"`
	Players []string    `json:"players" msgpack:"players"`
}

// Server preserves the legacy HTTP wire shape, including the historical
// capital-M "Metadata" field emitted by Bananagine's original Go struct.
type Server struct {
	ID          string            `json:"id" msgpack:"id"`
	Type        ServerType        `json:"type" msgpack:"type"`
	Mode        string            `json:"mode" msgpack:"mode"`
	Host        string            `json:"host" msgpack:"host"`
	Port        int               `json:"port" msgpack:"port"`
	WebhookPort int               `json:"webhookPort,omitempty" msgpack:"webhook_port,omitempty"`
	Players     int               `json:"players" msgpack:"players"`
	MaxPlayers  int               `json:"maxPlayers" msgpack:"max_players"`
	Matches     map[string]Match  `json:"matches" msgpack:"matches"`
	Metadata    map[string]string `json:"Metadata" msgpack:"metadata"`
}

type RegisterRequest struct {
	Server Server `json:"server" msgpack:"server"`
}

type ListRequest struct {
	Type          ServerType `json:"type,omitempty" msgpack:"type,omitempty"`
	Mode          string     `json:"mode,omitempty" msgpack:"mode,omitempty"`
	HasCapacity   bool       `json:"has_capacity,omitempty" msgpack:"has_capacity,omitempty"`
	HasReadyMatch bool       `json:"has_ready_match,omitempty" msgpack:"has_ready_match,omitempty"`
}

type GetRequest struct {
	ID string `json:"id" msgpack:"id"`
}

type UpdateRequest struct {
	ID         string            `json:"id" msgpack:"id"`
	Players    *int              `json:"players,omitempty" msgpack:"players,omitempty"`
	MaxPlayers *int              `json:"maxPlayers,omitempty" msgpack:"max_players,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty" msgpack:"metadata,omitempty"`
}

type UnregisterRequest struct {
	ID string `json:"id" msgpack:"id"`
}

type SetPlayersRequest struct {
	ID      string `json:"id" msgpack:"id"`
	Players int    `json:"players" msgpack:"players"`
}

type PutMatchRequest struct {
	ServerID string `json:"server_id" msgpack:"server_id"`
	MatchID  string `json:"match_id" msgpack:"match_id"`
	Match    Match  `json:"match" msgpack:"match"`
}

type RemoveMatchRequest struct {
	ServerID string `json:"server_id" msgpack:"server_id"`
	MatchID  string `json:"match_id" msgpack:"match_id"`
}

type Ack struct {
	Status string `json:"status" msgpack:"status"`
}

const (
	CodeInvalidArgument = "invalid_argument"
	CodeNotFound        = "not_found"
	CodeInternal        = "internal"
)

// ServiceError is carried in-band so local MessagePack calls and remote HTTP
// calls preserve the same domain error instead of flattening it into a Pulp
// transport error code.
type ServiceError struct {
	Code      string `json:"code" msgpack:"code"`
	Message   string `json:"message" msgpack:"message"`
	Retryable bool   `json:"retryable" msgpack:"retryable"`
}

func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Result is the common local/remote response envelope.
type Result[T any] struct {
	OK    bool          `json:"ok" msgpack:"ok"`
	Value T             `json:"value,omitempty" msgpack:"value,omitempty"`
	Error *ServiceError `json:"error,omitempty" msgpack:"error,omitempty"`
}

func Success[T any](value T) Result[T] {
	return Result[T]{OK: true, Value: value}
}

func Failure[T any](err error) Result[T] {
	var zero T
	serviceErr, ok := err.(*ServiceError)
	if !ok {
		serviceErr = &ServiceError{Code: CodeInternal, Message: fmt.Sprint(err)}
	}
	return Result[T]{OK: false, Value: zero, Error: serviceErr}
}

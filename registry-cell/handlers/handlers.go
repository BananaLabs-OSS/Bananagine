// Package handlers implements the MessagePack boundary for the standalone
// Bananagine registry cell without importing WASM-only Pulp bindings.
package handlers

import (
	"fmt"

	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/vmihailenco/msgpack/v5"
)

type Provider func([]byte) ([]byte, error)

type Set struct {
	state *registry.State
}

func New(state *registry.State) *Set {
	if state == nil {
		state = registry.NewState()
	}
	return &Set{state: state}
}

func (s *Set) Providers() map[string]Provider {
	return map[string]Provider{
		registry.FnRegister:    s.register,
		registry.FnList:        s.list,
		registry.FnGet:         s.get,
		registry.FnUpdate:      s.update,
		registry.FnUnregister:  s.unregister,
		registry.FnSetPlayers:  s.setPlayers,
		registry.FnPutMatch:    s.putMatch,
		registry.FnRemoveMatch: s.removeMatch,
	}
}

func (s *Set) Call(name string, input []byte) ([]byte, error) {
	provider, ok := s.Providers()[name]
	if !ok {
		return nil, fmt.Errorf("unknown registry function %q", name)
	}
	return provider(input)
}

func (s *Set) register(input []byte) ([]byte, error) {
	var request registry.RegisterRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	value, err := s.state.Register(request.Server)
	return encode(value, err)
}

func (s *Set) list(input []byte) ([]byte, error) {
	var request registry.ListRequest
	if err := decodeOptional(input, &request); err != nil {
		return nil, err
	}
	return encode(s.state.List(request), nil)
}

func (s *Set) get(input []byte) ([]byte, error) {
	var request registry.GetRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	value, err := s.state.Get(request.ID)
	return encode(value, err)
}

func (s *Set) update(input []byte) ([]byte, error) {
	var request registry.UpdateRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	value, err := s.state.Update(request)
	return encode(value, err)
}

func (s *Set) unregister(input []byte) ([]byte, error) {
	var request registry.UnregisterRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	s.state.Unregister(request.ID)
	return encode(registry.Ack{Status: "ok"}, nil)
}

func (s *Set) setPlayers(input []byte) ([]byte, error) {
	var request registry.SetPlayersRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	value, err := s.state.SetPlayers(request)
	return encode(value, err)
}

func (s *Set) putMatch(input []byte) ([]byte, error) {
	var request registry.PutMatchRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	value, err := s.state.PutMatch(request)
	return encode(value, err)
}

func (s *Set) removeMatch(input []byte) ([]byte, error) {
	var request registry.RemoveMatchRequest
	if err := decode(input, &request); err != nil {
		return nil, err
	}
	err := s.state.RemoveMatch(request)
	return encode(registry.Ack{Status: "ok"}, err)
}

func decode(input []byte, output any) error {
	if len(input) == 0 {
		return fmt.Errorf("decode: empty request")
	}
	if err := msgpack.Unmarshal(input, output); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func decodeOptional(input []byte, output any) error {
	if len(input) == 0 {
		return nil
	}
	return decode(input, output)
}

func encode[T any](value T, err error) ([]byte, error) {
	var result registry.Result[T]
	if err != nil {
		result = registry.Failure[T](err)
	} else {
		result = registry.Success(value)
	}
	output, marshalErr := msgpack.Marshal(result)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode: %w", marshalErr)
	}
	return output, nil
}

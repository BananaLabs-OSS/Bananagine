package handlers

import (
	"testing"

	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/vmihailenco/msgpack/v5"
)

func TestMessagePackRegistryBoundary(t *testing.T) {
	set := New(nil)

	registerRequest, err := msgpack.Marshal(registry.RegisterRequest{
		Server: registry.Server{
			ID:         "game-1",
			Type:       registry.TypeGame,
			Mode:       "survival",
			Players:    1,
			MaxPlayers: 4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registerResponse, err := set.Call(registry.FnRegister, registerRequest)
	if err != nil {
		t.Fatalf("register transport error: %v", err)
	}
	var registered registry.Result[registry.Server]
	if err := msgpack.Unmarshal(registerResponse, &registered); err != nil {
		t.Fatal(err)
	}
	if !registered.OK || registered.Value.ID != "game-1" || registered.Value.Matches == nil {
		t.Fatalf("register result = %#v", registered)
	}

	listRequest, _ := msgpack.Marshal(registry.ListRequest{HasCapacity: true})
	listResponse, err := set.Call(registry.FnList, listRequest)
	if err != nil {
		t.Fatalf("list transport error: %v", err)
	}
	var listed registry.Result[[]registry.Server]
	if err := msgpack.Unmarshal(listResponse, &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.OK || len(listed.Value) != 1 || listed.Value[0].ID != "game-1" {
		t.Fatalf("list result = %#v", listed)
	}
}

func TestDomainErrorsStayInBand(t *testing.T) {
	set := New(nil)
	request, _ := msgpack.Marshal(registry.GetRequest{ID: "missing"})
	response, err := set.Call(registry.FnGet, request)
	if err != nil {
		t.Fatalf("domain error escaped as transport error: %v", err)
	}

	var result registry.Result[registry.Server]
	if err := msgpack.Unmarshal(response, &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Error == nil || result.Error.Code != registry.CodeNotFound {
		t.Fatalf("result = %#v", result)
	}
}

func TestMalformedAndUnknownCallsAreTransportErrors(t *testing.T) {
	set := New(nil)
	if _, err := set.Call(registry.FnGet, []byte{0xc1}); err == nil {
		t.Fatal("malformed MessagePack should fail at the transport boundary")
	}
	if _, err := set.Call("unknown", nil); err == nil {
		t.Fatal("unknown function should fail at the transport boundary")
	}
}

package main

import (
	"strings"
	"testing"
	"time"
)

func validFleetWorldTransferRequest() fleetWorldTransferRequest {
	return fleetWorldTransferRequest{
		Version: fleetWorldTransferContract, ServerID: "server-1", NodeID: "node-1",
		Object: fleetWorldTransferObject{
			Namespace: "public-upload.v1", Key: "uploads/upload_0123456789abcdef01234567/archive.zip",
			Generation: 1, SHA256: strings.Repeat("a", 64), SizeBytes: 1024,
		},
		Transfer: fleetWorldTransferReference{URL: "https://objects.example.test/exact.zip?sig=bounded", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()},
	}
}

func TestFleetWorldTransferRequiresExactBoundedHostReference(t *testing.T) {
	if err := validateFleetWorldTransferRequest(validFleetWorldTransferRequest()); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for name, mutate := range map[string]func(*fleetWorldTransferRequest){
		"expired":            func(v *fleetWorldTransferRequest) { v.Transfer.ExpiresAtUnix = time.Now().Add(-time.Second).Unix() },
		"bare key traversal": func(v *fleetWorldTransferRequest) { v.Object.Key = "uploads/../other.zip" },
		"invalid digest":     func(v *fleetWorldTransferRequest) { v.Object.SHA256 = "not-a-digest" },
		"unbounded scheme":   func(v *fleetWorldTransferRequest) { v.Transfer.URL = "http://objects.example.test/exact.zip" },
		"userinfo":           func(v *fleetWorldTransferRequest) { v.Transfer.URL = "https://token@objects.example.test/exact.zip" },
	} {
		t.Run(name, func(t *testing.T) {
			request := validFleetWorldTransferRequest()
			mutate(&request)
			if err := validateFleetWorldTransferRequest(request); err == nil {
				t.Fatal("invalid typed world transfer was accepted")
			}
		})
	}
}

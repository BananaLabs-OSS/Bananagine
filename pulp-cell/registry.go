package main

import "fmt"

type serverType string

const (
	typeLobby serverType = "lobby"
	typeGame  serverType = "game"
)

type matchStatus string

const (
	statusReady    matchStatus = "ready"
	statusBusy     matchStatus = "busy"
	statusStarting matchStatus = "starting"
)

type matchInfo struct {
	Status  matchStatus `json:"status"`
	Need    int         `json:"need"`
	Players []string    `json:"players"`
}

type serverInfo struct {
	ID          string              `json:"id"`
	Type        serverType          `json:"type"`
	Mode        string              `json:"mode"`
	Host        string              `json:"host"`
	Port        int                 `json:"port"`
	WebhookPort int                 `json:"webhookPort,omitempty"`
	Players     int                 `json:"players"`
	MaxPlayers  int                 `json:"maxPlayers"`
	Matches     map[string]matchInfo `json:"matches"`
	// No json tag — upstream potassium/registry/registry.go has no tag on
	// Metadata so it marshals as the Go field name "Metadata" (capital M).
	Metadata map[string]string
}

type listFilter struct {
	Type          serverType
	Mode          string
	HasCapacity   bool
	HasReadyMatch bool
}

// Single-threaded WASM — no mutex needed. See capacity.go for details.
type registry struct {
	servers map[string]serverInfo
}

func newRegistry() *registry {
	return &registry{servers: make(map[string]serverInfo)}
}

func (r *registry) register(s serverInfo) error {
	// Upstream potassium/registry.Register uses `errors.New("Server ID required")`
	// (capital S, no trailing id). Match verbatim so cell matches native body text.
	if s.ID == "" {
		return fmt.Errorf("Server ID required")
	}
	if s.Type == typeGame && s.Matches == nil {
		s.Matches = make(map[string]matchInfo)
	}
	r.servers[s.ID] = s
	return nil
}

func (r *registry) unregister(id string) {
	delete(r.servers, id)
}

func (r *registry) get(id string) (serverInfo, bool) {
	s, ok := r.servers[id]
	return s, ok
}

func (r *registry) update(id string, fn func(*serverInfo)) error {
	s, ok := r.servers[id]
	if !ok {
		// Upstream potassium/registry.Update: errors.New("Server not found").
		return fmt.Errorf("Server not found")
	}
	fn(&s)
	r.servers[id] = s
	return nil
}

func (r *registry) updateMatch(serverID, matchID string, m matchInfo) error {
	s, ok := r.servers[serverID]
	if !ok {
		// Upstream potassium/registry.UpdateMatch: errors.New("Server not found").
		return fmt.Errorf("Server not found")
	}
	if s.Matches == nil {
		s.Matches = make(map[string]matchInfo)
	}
	s.Matches[matchID] = m
	r.servers[serverID] = s
	return nil
}

func (r *registry) removeMatch(serverID, matchID string) error {
	s, ok := r.servers[serverID]
	if !ok {
		// Upstream potassium/registry.RemoveMatch: errors.New("server not found")
		// — note the lowercase "server" here, unlike Update/UpdateMatch above.
		// Preserved verbatim for body-text parity.
		return fmt.Errorf("server not found")
	}
	delete(s.Matches, matchID)
	r.servers[serverID] = s
	return nil
}

func (r *registry) list(f *listFilter) []serverInfo {
	// Upstream potassium/registry/registry.go uses `var result []ServerInfo`
	// (nil) so an empty result marshals to `null`. Keep that wire shape.
	var result []serverInfo
	for _, s := range r.servers {
		if f != nil {
			if f.Type != "" && s.Type != f.Type {
				continue
			}
			if f.Mode != "" && s.Mode != f.Mode {
				continue
			}
			if f.HasCapacity && s.Players >= s.MaxPlayers {
				continue
			}
			if f.HasReadyMatch {
				found := false
				for _, m := range s.Matches {
					if m.Status == statusReady {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
		}
		result = append(result, s)
	}
	return result
}

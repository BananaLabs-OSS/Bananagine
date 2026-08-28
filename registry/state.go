package registry

import "sync"

// State owns one registry's mutable server and match records. It is safe for
// native callers as well as Pulp's serialized single-cell execution model.
type State struct {
	mu      sync.RWMutex
	servers map[string]Server
}

func NewState() *State {
	return &State{servers: make(map[string]Server)}
}

func (s *State) Register(server Server) (Server, error) {
	if server.ID == "" {
		return Server{}, &ServiceError{
			Code:    CodeInvalidArgument,
			Message: "Server ID required",
		}
	}
	if server.Type == TypeGame && server.Matches == nil {
		server.Matches = make(map[string]Match)
	}
	server = cloneServer(server)

	s.mu.Lock()
	s.servers[server.ID] = server
	s.mu.Unlock()
	return cloneServer(server), nil
}

func (s *State) List(filter ListRequest) []Server {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Preserve the established wire semantics: no matches returns a nil slice,
	// which JSON encodes as null rather than [].
	var result []Server
	for _, server := range s.servers {
		if filter.Type != "" && server.Type != filter.Type {
			continue
		}
		if filter.Mode != "" && server.Mode != filter.Mode {
			continue
		}
		if filter.HasCapacity && server.Players >= server.MaxPlayers {
			continue
		}
		if filter.HasReadyMatch && !hasReadyMatch(server) {
			continue
		}
		result = append(result, cloneServer(server))
	}
	return result
}

func (s *State) Get(id string) (Server, error) {
	s.mu.RLock()
	server, ok := s.servers[id]
	s.mu.RUnlock()
	if !ok {
		return Server{}, notFound("Server not found")
	}
	return cloneServer(server), nil
}

func (s *State) Update(request UpdateRequest) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	server, ok := s.servers[request.ID]
	if !ok {
		return Server{}, notFound("Server not found")
	}
	if request.Players != nil {
		server.Players = *request.Players
	}
	if request.MaxPlayers != nil {
		server.MaxPlayers = *request.MaxPlayers
	}
	if request.Metadata != nil {
		server.Metadata = cloneStrings(request.Metadata)
	}
	s.servers[request.ID] = server
	return cloneServer(server), nil
}

func (s *State) Unregister(id string) {
	s.mu.Lock()
	delete(s.servers, id)
	s.mu.Unlock()
}

func (s *State) SetPlayers(request SetPlayersRequest) (Server, error) {
	players := request.Players
	return s.Update(UpdateRequest{ID: request.ID, Players: &players})
}

func (s *State) PutMatch(request PutMatchRequest) (Match, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	server, ok := s.servers[request.ServerID]
	if !ok {
		return Match{}, notFound("Server not found")
	}
	if server.Matches == nil {
		server.Matches = make(map[string]Match)
	}
	match := cloneMatch(request.Match)
	server.Matches[request.MatchID] = match
	s.servers[request.ServerID] = server
	return cloneMatch(match), nil
}

func (s *State) RemoveMatch(request RemoveMatchRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	server, ok := s.servers[request.ServerID]
	if !ok {
		// Preserve the lowercase legacy error text for this one route.
		return notFound("server not found")
	}
	delete(server.Matches, request.MatchID)
	s.servers[request.ServerID] = server
	return nil
}

func notFound(message string) *ServiceError {
	return &ServiceError{Code: CodeNotFound, Message: message}
}

func hasReadyMatch(server Server) bool {
	for _, match := range server.Matches {
		if match.Status == StatusReady {
			return true
		}
	}
	return false
}

func cloneServer(server Server) Server {
	server.Metadata = cloneStrings(server.Metadata)
	if server.Matches != nil {
		matches := server.Matches
		server.Matches = make(map[string]Match, len(matches))
		for id, match := range matches {
			server.Matches[id] = cloneMatch(match)
		}
	}
	return server
}

func cloneMatch(match Match) Match {
	if match.Players != nil {
		match.Players = append([]string(nil), match.Players...)
	}
	return match
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

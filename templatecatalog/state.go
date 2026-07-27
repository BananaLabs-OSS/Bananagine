package templatecatalog

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

type State struct {
	revision uint64
	entries  map[string]Entry
	requests map[string][]byte
}

func NewState() *State {
	return &State{
		entries:  make(map[string]Entry),
		requests: make(map[string][]byte),
	}
}

func (s *State) Replace(request ReplaceRequest) (Catalog, error) {
	if s == nil {
		return Catalog{}, internal("template catalog is unavailable")
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return Catalog{}, invalid("request_id is required")
	}
	entries, fingerprint, err := validateEntries(request.Entries)
	if err != nil {
		return Catalog{}, err
	}
	if prior, exists := s.requests[request.RequestID]; exists {
		if !bytes.Equal(prior, fingerprint) {
			return Catalog{}, conflict("request_id was already used for a different catalog")
		}
		return s.catalog(), nil
	}
	s.entries = entries
	s.revision++
	s.requests[request.RequestID] = append([]byte(nil), fingerprint...)
	return s.catalog(), nil
}

func (s *State) List() Catalog {
	if s == nil {
		return Catalog{}
	}
	return s.catalog()
}

func (s *State) Get(name string) (Entry, error) {
	if s == nil {
		return Entry{}, internal("template catalog is unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, invalid("template name is required")
	}
	entry, ok := s.entries[name]
	if !ok {
		return Entry{}, notFound("template not found")
	}
	return cloneEntry(entry), nil
}

func (s *State) Export() Snapshot {
	catalog := s.List()
	return Snapshot{
		Version:  SnapshotVersion,
		Revision: catalog.Revision,
		Entries:  catalog.Entries,
	}
}

func (s *State) Import(request ImportRequest) (Catalog, error) {
	if request.Snapshot.Version != SnapshotVersion {
		return Catalog{}, invalid("unsupported template catalog snapshot version")
	}
	entries, fingerprint, err := validateEntries(request.Snapshot.Entries)
	if err != nil {
		return Catalog{}, err
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return Catalog{}, invalid("request_id is required")
	}
	if prior, exists := s.requests[request.RequestID]; exists {
		if !bytes.Equal(prior, fingerprint) {
			return Catalog{}, conflict("request_id was already used for a different snapshot")
		}
		return s.catalog(), nil
	}
	s.entries = entries
	s.revision = request.Snapshot.Revision
	s.requests[request.RequestID] = append([]byte(nil), fingerprint...)
	return s.catalog(), nil
}

func (s *State) catalog() Catalog {
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	// Preserve an empty catalog as nil on the wire. Lua tables cannot retain
	// the distinction between an empty MessagePack array and map, while nil
	// round-trips unambiguously through the orchestrator.
	var entries []Entry
	if len(names) > 0 {
		entries = make([]Entry, 0, len(names))
	}
	for _, name := range names {
		entries = append(entries, cloneEntry(s.entries[name]))
	}
	return Catalog{Revision: s.revision, Entries: entries}
}

func validateEntries(source []Entry) (map[string]Entry, []byte, error) {
	entries := make(map[string]Entry, len(source))
	for _, entry := range source {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Game = strings.TrimSpace(entry.Game)
		if entry.Name == "" {
			return nil, nil, invalid("template name is required")
		}
		if entry.Game == "" {
			return nil, nil, invalid("template game is required")
		}
		if _, duplicate := entries[entry.Name]; duplicate {
			return nil, nil, conflict("duplicate template name")
		}
		if len(entry.ConfigJSON) == 0 {
			entry.ConfigJSON = json.RawMessage(`{}`)
		}
		if !json.Valid(entry.ConfigJSON) {
			return nil, nil, invalid("template config_json is invalid")
		}
		if len(entry.RuntimeJSON) > 0 && !json.Valid(entry.RuntimeJSON) {
			return nil, nil, invalid("template runtime_json is invalid")
		}
		entries[entry.Name] = cloneEntry(entry)
	}
	canonical := make([]Entry, 0, len(entries))
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		canonical = append(canonical, entries[name])
	}
	fingerprint, err := json.Marshal(canonical)
	if err != nil {
		return nil, nil, internal("encode template catalog fingerprint")
	}
	return entries, fingerprint, nil
}

func cloneEntry(entry Entry) Entry {
	entry.ConfigJSON = append(json.RawMessage(nil), entry.ConfigJSON...)
	entry.RuntimeJSON = append(json.RawMessage(nil), entry.RuntimeJSON...)
	return entry
}

func invalid(message string) error {
	return &ServiceError{Code: CodeInvalidArgument, Message: message}
}

func conflict(message string) error {
	return &ServiceError{Code: CodeConflict, Message: message}
}

func notFound(message string) error {
	return &ServiceError{Code: CodeNotFound, Message: message}
}

func internal(message string) error {
	return &ServiceError{Code: CodeInternal, Message: message, Retryable: true}
}

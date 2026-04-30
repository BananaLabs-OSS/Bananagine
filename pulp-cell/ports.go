package main

import "fmt"

// Single-threaded WASM — no mutex needed. See capacity.go for details.
type portPool struct {
	start     int
	end       int
	allocated map[int]string // port -> server ID
}

func newPortPool(start, end int) *portPool {
	return &portPool{
		start:     start,
		end:       end,
		allocated: make(map[int]string),
	}
}

func (p *portPool) allocate(serverID string) (int, error) {
	for port := p.start; port <= p.end; port++ {
		if _, used := p.allocated[port]; !used {
			p.allocated[port] = serverID
			return port, nil
		}
	}
	return 0, fmt.Errorf("no ports available in range %d-%d", p.start, p.end)
}

func (p *portPool) releaseByServer(serverID string) {
	for port, id := range p.allocated {
		if id == serverID {
			delete(p.allocated, port)
		}
	}
}

func (p *portPool) reKey(oldID, newID string) {
	for port, id := range p.allocated {
		if id == oldID {
			p.allocated[port] = newID
		}
	}
}

func (p *portPool) reserve(port int, serverID string) {
	p.allocated[port] = serverID
}

func (p *portPool) contains(port int) bool {
	return port >= p.start && port <= p.end
}

type portPoolSet struct {
	fallback *portPool
	pools    map[string]*portPool
}

func newPortPoolSet(fallback *portPool) *portPoolSet {
	return &portPoolSet{
		fallback: fallback,
		pools:    make(map[string]*portPool),
	}
}

func parseRange(r string) (int, int, error) {
	var start, end int
	_, err := fmt.Sscanf(r, "%d-%d", &start, &end)
	if err != nil || start > end {
		return 0, 0, fmt.Errorf("invalid port range: %q", r)
	}
	return start, end, nil
}

func (ps *portPoolSet) getOrCreate(rangeStr string) (*portPool, error) {
	if p, ok := ps.pools[rangeStr]; ok {
		return p, nil
	}
	start, end, err := parseRange(rangeStr)
	if err != nil {
		return nil, err
	}
	p := newPortPool(start, end)
	ps.pools[rangeStr] = p
	return p, nil
}

func (ps *portPoolSet) allocate(rangeStr, serverID string) (int, error) {
	if rangeStr == "" {
		return ps.fallback.allocate(serverID)
	}
	pool, err := ps.getOrCreate(rangeStr)
	if err != nil {
		return 0, err
	}
	return pool.allocate(serverID)
}

func (ps *portPoolSet) releaseByServer(serverID string) {
	ps.fallback.releaseByServer(serverID)
	for _, p := range ps.pools {
		p.releaseByServer(serverID)
	}
}

func (ps *portPoolSet) reKey(oldID, newID string) {
	ps.fallback.reKey(oldID, newID)
	for _, p := range ps.pools {
		p.reKey(oldID, newID)
	}
}

func (ps *portPoolSet) reserve(port int, serverID string) {
	for _, p := range ps.pools {
		if p.contains(port) {
			p.reserve(port, serverID)
			return
		}
	}
	ps.fallback.reserve(port, serverID)
}

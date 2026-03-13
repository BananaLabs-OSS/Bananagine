package ports

import (
	"fmt"
	"sync"
)

type Pool struct {
	mu        sync.Mutex
	start     int
	end       int
	allocated map[int]string // port → server ID
}

func NewPool(start, end int) *Pool {
	return &Pool{
		start:     start,
		end:       end,
		allocated: make(map[int]string),
	}
}

func (p *Pool) Allocate(serverID string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port := p.start; port <= p.end; port++ {
		if _, used := p.allocated[port]; !used {
			p.allocated[port] = serverID
			return port, nil
		}
	}

	return 0, fmt.Errorf("no ports available in range %d-%d", p.start, p.end)
}

func (p *Pool) AllocateN(n int, serverID string) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// First, reuse ports already allocated to this serverID
	var result []int
	for port := p.start; port <= p.end && len(result) < n; port++ {
		if owner, used := p.allocated[port]; used && owner == serverID {
			result = append(result, port)
		}
	}

	// Then fill remaining from free ports
	for port := p.start; port <= p.end && len(result) < n; port++ {
		if _, used := p.allocated[port]; !used {
			result = append(result, port)
		}
	}

	if len(result) < n {
		return nil, fmt.Errorf("need %d ports but only %d available in range %d-%d", n, len(result), p.start, p.end)
	}

	for _, port := range result {
		p.allocated[port] = serverID
	}

	return result, nil
}

func (p *Pool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.allocated, port)
}

func (p *Pool) ReleaseByServer(serverID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, id := range p.allocated {
		if id == serverID {
			delete(p.allocated, port)
		}
	}
}

func (p *Pool) ReKey(oldID, newID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, id := range p.allocated {
		if id == oldID {
			p.allocated[port] = newID
		}
	}
}

func (p *Pool) Reserve(port int, serverID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.allocated[port] = serverID
}

// Contains returns true if the port falls within this pool's range.
func (p *Pool) Contains(port int) bool {
	return port >= p.start && port <= p.end
}

// PoolSet manages multiple port pools keyed by range string (e.g. "25565-25599").
// Falls back to a default pool for ports without a range.
type PoolSet struct {
	mu       sync.Mutex
	fallback *Pool
	pools    map[string]*Pool // "start-end" → pool
}

func NewPoolSet(fallback *Pool) *PoolSet {
	return &PoolSet{
		fallback: fallback,
		pools:    make(map[string]*Pool),
	}
}

// ParseRange parses a range string like "25565-25599" into start and end.
func ParseRange(r string) (int, int, error) {
	var start, end int
	_, err := fmt.Sscanf(r, "%d-%d", &start, &end)
	if err != nil || start > end {
		return 0, 0, fmt.Errorf("invalid port range: %q", r)
	}
	return start, end, nil
}

// getOrCreate returns the pool for a range string, creating it if needed.
func (ps *PoolSet) getOrCreate(rangeStr string) (*Pool, error) {
	if p, ok := ps.pools[rangeStr]; ok {
		return p, nil
	}
	start, end, err := ParseRange(rangeStr)
	if err != nil {
		return nil, err
	}
	p := NewPool(start, end)
	ps.pools[rangeStr] = p
	return p, nil
}

// Allocate allocates a port from the pool matching rangeStr, or the fallback if empty.
func (ps *PoolSet) Allocate(rangeStr, serverID string) (int, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if rangeStr == "" {
		return ps.fallback.Allocate(serverID)
	}
	pool, err := ps.getOrCreate(rangeStr)
	if err != nil {
		return 0, err
	}
	return pool.Allocate(serverID)
}

// ReleaseByServer releases all ports owned by serverID across all pools.
func (ps *PoolSet) ReleaseByServer(serverID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.fallback.ReleaseByServer(serverID)
	for _, p := range ps.pools {
		p.ReleaseByServer(serverID)
	}
}

// ReKey updates ownership across all pools.
func (ps *PoolSet) ReKey(oldID, newID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.fallback.ReKey(oldID, newID)
	for _, p := range ps.pools {
		p.ReKey(oldID, newID)
	}
}

// Reserve reserves a port in whichever pool contains it, or the fallback.
func (ps *PoolSet) Reserve(port int, serverID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for _, p := range ps.pools {
		if p.Contains(port) {
			p.Reserve(port, serverID)
			return
		}
	}
	ps.fallback.Reserve(port, serverID)
}

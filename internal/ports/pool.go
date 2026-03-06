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

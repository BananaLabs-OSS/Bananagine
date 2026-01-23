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
			return
		}
	}
}

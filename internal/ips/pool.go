package ips

import (
	"fmt"
	"net"
	"sync"
)

type Pool struct {
	mu        sync.Mutex
	start     net.IP
	end       net.IP
	current   net.IP
	allocated map[string]string // IP → server ID
}

func NewPool(start, end string) *Pool {
	startIP := net.ParseIP(start).To4()
	current := make(net.IP, len(startIP))
	copy(current, startIP)

	return &Pool{
		start:     startIP,
		end:       net.ParseIP(end).To4(),
		current:   current,
		allocated: make(map[string]string),
	}
}

func (p *Pool) Allocate(serverID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ip := make(net.IP, len(p.start))
	copy(ip, p.start)

	for ; !ip.Equal(p.end); incIP(ip) {
		ipStr := ip.String()
		if _, used := p.allocated[ipStr]; !used {
			p.allocated[ipStr] = serverID
			return ipStr, nil
		}
	}

	return "", fmt.Errorf("no IPs available")
}

func (p *Pool) Release(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allocated, ip)
}

func (p *Pool) ReleaseByServer(serverID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for ip, id := range p.allocated {
		if id == serverID {
			delete(p.allocated, ip)
			return
		}
	}
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

package main

import (
	"fmt"
	"net"
)

// Single-threaded WASM — no mutex needed. See capacity.go for details.
type ipPool struct {
	start     net.IP
	end       net.IP
	allocated map[string]string // IP -> server ID
}

func newIPPool(start, end string) *ipPool {
	startIP := net.ParseIP(start).To4()
	endIP := net.ParseIP(end).To4()
	if startIP == nil || endIP == nil {
		panic(fmt.Sprintf("invalid IP pool range: %s - %s", start, end))
	}
	return &ipPool{
		start:     startIP,
		end:       endIP,
		allocated: make(map[string]string),
	}
}

func (p *ipPool) allocate(serverID string) (string, error) {
	ip := make(net.IP, len(p.start))
	copy(ip, p.start)
	for {
		ipStr := ip.String()
		if _, used := p.allocated[ipStr]; !used {
			p.allocated[ipStr] = serverID
			return ipStr, nil
		}
		if ip.Equal(p.end) {
			break
		}
		incIP(ip)
	}
	return "", fmt.Errorf("no IPs available")
}

func (p *ipPool) release(ip string) {
	delete(p.allocated, ip)
}

func (p *ipPool) releaseByServer(serverID string) {
	for ip, id := range p.allocated {
		if id == serverID {
			delete(p.allocated, ip)
			return
		}
	}
}

func (p *ipPool) reKey(oldID, newID string) {
	for ip, id := range p.allocated {
		if id == oldID {
			p.allocated[ip] = newID
		}
	}
}

func (p *ipPool) reserve(ip string, serverID string) {
	p.allocated[ip] = serverID
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

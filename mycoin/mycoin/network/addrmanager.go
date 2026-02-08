package network

import (
	"sync"
	"time"
)

type AddrManager struct {
	Known map[string]time.Time
	mu    sync.Mutex
}

func NewAddrManager() *AddrManager {
	return &AddrManager{
		Known: make(map[string]time.Time),
	}
}

func (am *AddrManager) Add(addr string) bool {
	am.mu.Lock()
	defer am.mu.Unlock()

	_, exists := am.Known[addr]
	am.Known[addr] = time.Now()

	return !exists
}

func (am *AddrManager) AddMany(addrs []string) {
	for _, a := range addrs {
		am.Add(a)
	}
}

func (am *AddrManager) GetSome(n int) []string {
	am.mu.Lock()
	defer am.mu.Unlock()

	out := make([]string, 0, n)
	for addr := range am.Known {
		out = append(out, addr)
		if len(out) >= n {
			break
		}
	}
	return out
}

func (am *AddrManager) GetAll() []string {
	am.mu.Lock()
	defer am.mu.Unlock()

	addrs := make([]string, 0, len(am.Known))
	for addr := range am.Known {
		addrs = append(addrs, addr)
	}
	return addrs
}

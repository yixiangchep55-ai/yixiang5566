package network

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
)

type PeerState int

const (
	StateInit PeerState = iota
	StateVersionSent
	StateVersionRecv
	StateActive
)

type PeerInfo struct {
	Addr     string `json:"addr"`
	LastSeen int64  `json:"last_seen"`
	NodeID   uint64 `json:"node_id,omitempty"`
	Version  int    `json:"version,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type Peer struct {
	Conn     net.Conn
	Addr     string
	State    PeerState
	Height   uint64
	CumWork  string
	LastSeen int64
	Outbound bool
	NodeID   uint64
	Mode     string

	mu  sync.Mutex
	enc *json.Encoder
	dec *json.Decoder

	onDisconnect func(*Peer)
	disconnectMu sync.Mutex
	disconnected bool
}

func (p *Peer) closeLocked() {
	if p.Conn != nil {
		_ = p.Conn.Close()
		p.Conn = nil
	}
	p.State = StateInit
}

func NewPeer(conn net.Conn) *Peer {
	dec := json.NewDecoder(conn)
	dec.UseNumber()

	return &Peer{
		Conn: conn,
		Addr: conn.RemoteAddr().String(),
		enc:  json.NewEncoder(conn),
		dec:  dec,
	}
}

func (p *Peer) Send(msg Message) bool {
	p.mu.Lock()
	if p.Conn == nil {
		p.mu.Unlock()
		return false
	}

	err := p.enc.Encode(msg)
	if err != nil {
		log.Printf("⚠️ [Network] 發送訊息失敗給 %s: %v\n", p.Addr, err)
		p.closeLocked()
		p.mu.Unlock()
		p.notifyDisconnected()
		log.Println("❌ peer disconnected:", p.Addr)
		return false
	}
	p.mu.Unlock()

	return true
}

func (p *Peer) ReadLoop(onMessage func(*Peer, *Message)) {
	for {
		var msg Message
		if err := p.dec.Decode(&msg); err != nil {
			p.Close()
			log.Println("❌ peer disconnected:", p.Addr)
			return
		}

		p.LastSeen = time.Now().Unix()
		onMessage(p, &msg)
	}
}

func (p *Peer) IsClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Conn == nil
}

func (p *Peer) IsActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Conn != nil && p.State == StateActive
}

func (p *Peer) Close() {
	p.mu.Lock()
	p.closeLocked()
	p.mu.Unlock()
	p.notifyDisconnected()
}

func (p *Peer) notifyDisconnected() {
	p.disconnectMu.Lock()
	if p.disconnected {
		p.disconnectMu.Unlock()
		return
	}
	p.disconnected = true
	callback := p.onDisconnect
	p.disconnectMu.Unlock()

	if callback != nil {
		callback(p)
	}
}

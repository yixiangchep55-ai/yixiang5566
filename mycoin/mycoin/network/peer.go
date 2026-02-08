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

	mu  sync.Mutex
	enc *json.Encoder
	dec *json.Decoder
}

func NewPeer(conn net.Conn) *Peer {
	return &Peer{
		Conn: conn,
		Addr: conn.RemoteAddr().String(),
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
	}
}

func (p *Peer) Send(msg Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enc.Encode(msg)
}

func (p *Peer) ReadLoop(onMessage func(*Peer, *Message)) {
	for {
		var msg Message
		if err := p.dec.Decode(&msg); err != nil {
			log.Println("❌ peer disconnected:", p.Addr)
			return
		}

		p.LastSeen = time.Now().Unix()

		// ⭐ 正确的调用方式：传入 peer + msg
		onMessage(p, &msg)
	}
}

func (p *Peer) IsClosed() bool {
	return p.Conn == nil
}

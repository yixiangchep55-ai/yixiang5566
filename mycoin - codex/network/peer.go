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
	// 🌟 探長建議：加上 NodeID，讓檔案更完整
	NodeID  uint64 `json:"node_id,omitempty"`
	Version int    `json:"version,omitempty"`
	Height  int    `json:"height,omitempty"`
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

func (p *Peer) Send(msg Message) {
	// 🌟 探長交通管制：加上這把鎖，確保打招呼和發送區塊不會撞車！
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Conn != nil {
		err := p.enc.Encode(msg)
		if err != nil {
			log.Printf("⚠️ [Network] 發送訊息失敗給 %s: %v\n", p.Addr, err)
		}
	}
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

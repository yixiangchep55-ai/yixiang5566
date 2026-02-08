package network

import (
	"mycoin/node"
	"net"
	"sync"
)

type Network struct {
	Peers       map[string]*Peer
	Handler     *Handler
	Node        *node.Node
	mu          sync.Mutex
	PeerManager *PeerManager
}

func NewNetwork(handler *Handler) *Network {
	return &Network{
		Peers:   make(map[string]*Peer),
		Handler: handler,
	}
}

func (n *Network) AddConn(conn net.Conn) {
	peer := NewPeer(conn)

	n.mu.Lock()
	n.Peers[peer.Addr] = peer
	n.mu.Unlock()

	// 先发送 version
	peer.Send(Message{
		Type: MsgVersion,
		Data: n.Handler.LocalVersion,
	})
	peer.State = StateVersionSent

	go peer.ReadLoop(func(p *Peer, msg *Message) {
		n.Handler.OnMessage(p, msg)
	})
}

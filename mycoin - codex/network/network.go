package network

import (
	"fmt"
	"mycoin/node"
	"net"
	"sync"
)

type Network struct {
	Peers       map[uint64]*Peer
	Handler     *Handler
	Node        *node.Node
	mu          sync.Mutex
	PeerManager *PeerManager
}

type mempool struct {
	AddrFrom uint64
}

func NewNetwork(handler *Handler) *Network {
	return &Network{
		Peers:   make(map[uint64]*Peer),
		Handler: handler,
	}
}

func (n *Network) PeerCount() int {
	if n == nil {
		return 0
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	count := 0
	for _, peer := range n.Peers {
		if peer != nil && !peer.IsClosed() {
			count++
		}
	}
	return count
}

func (n *Network) AddConn(conn net.Conn) {
	peer := NewPeer(conn)

	// =================================================================
	// 🛑 探長急救包：這裡還不知道對方的 NodeID，絕對不能加入 VIP 名單！
	// =================================================================
	// n.mu.Lock()
	// n.Peers[peer.Addr] = peer // 👈 把它整段註解掉或刪除！
	// n.mu.Unlock()

	fmt.Printf("👋 新連線進入大廳，來自: %s。等待交換身分證...\n", peer.Addr)

	// 🌟 遞出我們的名片 (發送 Version)
	// (注意：你要確保 n.Handler.LocalVersion 裡面，已經包含了你剛才生成的 NodeID)
	peer.Send(Message{
		Type: MsgVersion,
		Data: n.Handler.LocalVersion,
	})
	peer.State = StateVersionSent

	// 🕵️ 讓保全盯著他 (啟動 ReadLoop 聽他接下來要說什麼)
	go peer.ReadLoop(func(p *Peer, msg *Message) {
		n.Handler.OnMessage(p, msg)
	})
}

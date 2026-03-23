package network

import (
	"context"
	"encoding/json"
	"log"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"
)

var DefaultSeeds = []string{
	//"192.168.100.169:9001",
	//"192.168.100.215:9001",
}

type PeerManager struct {
	Network *Network
	AddrMgr *AddrManager

	Active   map[string]*Peer
	Inbound  int
	Outbound int

	MaxPeers int
	ListenOn string

	mu sync.Mutex
}

func NewPeerManager(netw *Network, listen string, maxPeers int) *PeerManager {
	return &PeerManager{
		Network:  netw,
		AddrMgr:  NewAddrManager(),
		Active:   make(map[string]*Peer),
		MaxPeers: maxPeers,
		ListenOn: listen,
	}
}

func normalizeHost(host string) string {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return "127.0.0.1"
	}
	return host
}

func (pm *PeerManager) isLocalHost(host string) bool {
	host = normalizeHost(host)
	switch host {
	case "", "0.0.0.0", "::", "127.0.0.1", "::1":
		return true
	}

	listenHost, _, err := net.SplitHostPort(pm.ListenOn)
	if err == nil {
		listenHost = normalizeHost(listenHost)
		if listenHost != "" && listenHost != "0.0.0.0" && listenHost != "::" && host == listenHost {
			return true
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}

	for _, addr := range addrs {
		switch value := addr.(type) {
		case *net.IPNet:
			if normalizeHost(value.IP.String()) == host {
				return true
			}
		case *net.IPAddr:
			if normalizeHost(value.IP.String()) == host {
				return true
			}
		}
	}

	return false
}

func (pm *PeerManager) isSelfDialAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	_, listenPort, err := net.SplitHostPort(pm.ListenOn)
	if err == nil && listenPort != "" && port != listenPort {
		return false
	}

	return pm.isLocalHost(host)
}

func (pm *PeerManager) Start() {
	pm.LoadStaticSeeds()
	go func() {
		pm.QueryDNSSeeds()
		pm.ensurePeers()
	}()

	known := pm.LoadPeers()
	if len(known) > 0 {
		log.Println("🌐 Restoring peers:", known)
	}

	for _, addr := range known {
		go pm.Connect(addr)
	}

	pm.startListener()
	pm.ensurePeers()
	go pm.maintain()
}

func (pm *PeerManager) startListener() {
	ln, err := net.Listen("tcp", pm.ListenOn)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("🌐 P2P listening on", pm.ListenOn)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				continue
			}
			pm.onNewConn(conn, false)
		}
	}()
}

func (pm *PeerManager) Connect(addr string) {
	if pm.isSelfDialAddress(addr) {
		return
	}

	pm.mu.Lock()
	if pm.Outbound >= pm.MaxPeers/2 {
		pm.mu.Unlock()
		return
	}
	if existing, ok := pm.Active[addr]; ok {
		if !existing.IsClosed() {
			pm.mu.Unlock()
			return
		}
	}
	pm.mu.Unlock()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}

	pm.onNewConn(conn, true)

	pm.mu.Lock()
	p, ok := pm.Active[addr]
	pm.mu.Unlock()
	if ok {
		pm.SavePeer(p)
	}
}

func (pm *PeerManager) removePeer(peer *Peer) {
	if peer == nil {
		return
	}

	remainingCount := 0

	pm.mu.Lock()
	if current, ok := pm.Active[peer.Addr]; ok && current == peer {
		delete(pm.Active, peer.Addr)
		if peer.Outbound {
			if pm.Outbound > 0 {
				pm.Outbound--
			}
		} else if pm.Inbound > 0 {
			pm.Inbound--
		}
	}
	pm.mu.Unlock()

	if peer.NodeID == 0 {
		pm.Network.RecordPeerDisconnected(peer.Addr, peer.DisconnectReason(), remainingCount)
		return
	}

	pm.Network.mu.Lock()
	if current, ok := pm.Network.Peers[peer.NodeID]; ok && current == peer {
		delete(pm.Network.Peers, peer.NodeID)
	}
	remainingCount = len(pm.Network.Peers)
	pm.Network.mu.Unlock()
	pm.Network.RecordPeerDisconnected(peer.Addr, peer.DisconnectReason(), remainingCount)
}

func (pm *PeerManager) cleanup() {
	pm.mu.Lock()
	stale := make([]*Peer, 0)
	for _, p := range pm.Active {
		if p.IsClosed() {
			stale = append(stale, p)
		}
	}
	pm.mu.Unlock()

	for _, p := range stale {
		pm.removePeer(p)
	}
}

func (pm *PeerManager) onNewConn(conn net.Conn, outbound bool) {
	remote := conn.RemoteAddr().String()
	remoteIP, _, _ := net.SplitHostPort(remote)
	localIP, _, _ := net.SplitHostPort(pm.ListenOn)

	if remoteIP == localIP {
		log.Println("⛔ Reject self-connection from", remote)
		conn.Close()
		return
	}

	peer := NewPeer(conn)
	peer.Outbound = outbound
	peer.onDisconnect = pm.removePeer

	if outbound && (pm.Network.Node == nil || pm.Network.Node.Best == nil) {
		log.Println("⚠️ [Network] Node 尚未就緒，拒絕 outbound handshake:", peer.Addr)
		conn.Close()
		return
	}

	pm.AddrMgr.Add(peer.Addr)

	pm.mu.Lock()
	if len(pm.Active) >= pm.MaxPeers {
		pm.mu.Unlock()
		conn.Close()
		return
	}
	pm.Active[peer.Addr] = peer
	if outbound {
		pm.Outbound++
	} else {
		pm.Inbound++
	}
	pm.mu.Unlock()

	if !outbound {
		go pm.RelayAddress(peer.Addr)
	}

	go peer.ReadLoop(pm.Network.Handler.OnMessage)

	if outbound {
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  pm.Network.Node.Best.Height,
				CumWork: pm.Network.Node.Best.CumWork,
				NodeID:  pm.Network.Node.NodeID,
			},
		})
		log.Println("🚀 Sent version handshake to", peer.Addr)
	}
}

func (pm *PeerManager) RelayAddress(newAddr string) {
	host, _, err := net.SplitHostPort(newAddr)
	if err != nil {
		log.Println("⚠️ [Relay] 無法解析地址:", newAddr)
		return
	}
	correctAddr := host + ":9001"

	pm.mu.Lock()
	defer pm.mu.Unlock()

	msg := Message{
		Type: MsgAddr,
		Data: []string{correctAddr},
	}

	for addr, peer := range pm.Active {
		if addr != newAddr && peer.State == StateActive {
			log.Printf("📢 [Relay] 向 %s 推薦新節點: %s\n", addr, correctAddr)
			peer.Send(msg)
		}
	}
}

func (pm *PeerManager) ensurePeers() {
	pm.mu.Lock()
	need := pm.MaxPeers - len(pm.Active)
	pm.mu.Unlock()

	if need <= 0 {
		return
	}

	addrs := pm.AddrMgr.GetSome(need)
	for _, addr := range addrs {
		if pm.isSelfDialAddress(addr) {
			continue
		}
		go pm.Connect(addr)
	}
}

func (pm *PeerManager) maintain() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pm.cleanup()
		pm.ensurePeers()
	}
}

func (pm *PeerManager) SavePeer(p *Peer) {
	info := PeerInfo{
		Addr:     p.Addr,
		LastSeen: time.Now().Unix(),
		NodeID:   p.NodeID,
		Height:   int(p.Height),
	}

	data, _ := json.Marshal(info)
	pm.Network.Node.DB.Put("peerstore", p.Addr, data)
}

func (pm *PeerManager) LoadPeers() []string {
	var peers []string

	pm.Network.Node.DB.Iterate("peerstore", func(k, v []byte) {
		peers = append(peers, string(k))
	})

	return peers
}

func (pm *PeerManager) LoadStaticSeeds() {
	for _, seed := range DefaultSeeds {
		if pm.isSelfDialAddress(seed) {
			log.Println("⛔ skipping self seed:", seed)
			continue
		}
		pm.AddrMgr.Add(seed)
		log.Println("📌 static seed added:", seed)
	}
}

func (pm *PeerManager) QueryDNSSeeds() {
	seeds := []string{
		"seed1.mycoin.org",
		"seed2.mycoin.org",
		"seed.mycoin.net",
	}

	rand.Shuffle(len(seeds), func(i, j int) {
		seeds[i], seeds[j] = seeds[j], seeds[i]
	})

	resolver := net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, domain := range seeds {
		ips, err := resolver.LookupHost(ctx, domain)
		if err != nil {
			log.Println("⚠️ DNS seed lookup failed:", domain, err)
			continue
		}

		for _, ip := range ips {
			if strings.Contains(ip, ":") {
				ip = "[" + ip + "]"
			}

			addr := ip + ":9001"
			pm.AddrMgr.Add(addr)
			log.Println("🌐 DNS seed discovered:", addr)
		}
	}
}

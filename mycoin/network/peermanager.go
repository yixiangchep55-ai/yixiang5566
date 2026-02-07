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

func NewPeerManager(net *Network, listen string, maxPeers int) *PeerManager {
	return &PeerManager{
		Network:  net,
		AddrMgr:  NewAddrManager(),
		Active:   make(map[string]*Peer),
		MaxPeers: maxPeers,
		ListenOn: listen,
	}
}

func (pm *PeerManager) Start() {

	// -----------------------------------
	// 0️⃣ 加载静态 SEEDS（内网 / 公网）
	// -----------------------------------
	pm.LoadStaticSeeds()

	// -----------------------------------
	// 0️⃣.5 启动 DNS SEEDS（自动发现公网节点）
	// -----------------------------------
	//go pm.QueryDNSSeeds()

	// -----------------------------------
	// 1️⃣ 从 DB 恢复存档 peers
	// -----------------------------------
	known := pm.LoadPeers()
	if len(known) > 0 {
		log.Println("🌐 Restoring peers:", known)
	}

	for _, addr := range known {
		go pm.Connect(addr)
	}

	// -----------------------------------
	// 2️⃣ 启动 listener
	// -----------------------------------
	pm.startListener()

	// -----------------------------------
	// 3️⃣ 启动自动重连逻辑
	// -----------------------------------
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

	if addr == pm.ListenOn { // ⭐ 阻止自连接
		return
	}
	pm.mu.Lock()
	if pm.Outbound >= pm.MaxPeers/2 {
		pm.mu.Unlock()
		return
	}
	if _, ok := pm.Active[addr]; ok {
		pm.mu.Unlock()
		return
	}
	pm.mu.Unlock()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}

	// ⭐ 创建 peer 并启动 ReadLoop（onNewConn 会自动做）
	pm.onNewConn(conn, true)

	// ⭐ 持久化 peer 地址
	pm.SavePeer(addr)
}

func (pm *PeerManager) cleanup() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for addr, p := range pm.Active {
		if p.IsClosed() {
			delete(pm.Active, addr)
			if p.Outbound {
				pm.Outbound--
			} else {
				pm.Inbound--
			}
			log.Println("❌ peer disconnected:", addr)
		}
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

	// outbound：主动发 version
	if outbound {
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  pm.Network.Node.Best.Height,
				CumWork: pm.Network.Node.Best.CumWork,
			},
		})
		log.Println("🚀 Sent version handshake to", peer.Addr)
	}

	// 启动读循环
	go peer.ReadLoop(pm.Network.Handler.OnMessage)
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

		// 🚫 不要连接自己的监听地址
		if addr == pm.ListenOn {
			continue
		}

		// 🚫 不要连接自己的 NodeID（本机对外广告地址）
		if pm.Network != nil &&
			pm.Network.Handler != nil &&
			addr == pm.Network.Handler.LocalVersion.NodeID {
			continue
		}

		go pm.Connect(addr)
	}
}
func (pm *PeerManager) maintain() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		pm.cleanup()
		pm.ensurePeers()
	}
}

func (pm *PeerManager) SavePeer(addr string) {
	info := PeerInfo{
		Addr:     addr,
		LastSeen: time.Now().Unix(),
	}

	data, _ := json.Marshal(info)
	pm.Network.Node.DB.Put("peerstore", addr, data)
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
		if seed == pm.ListenOn { // ⭐ 不允许把自己加入 AddrMgr
			log.Println("⛔ skipping self seed:", seed)
			continue
		}
		pm.AddrMgr.Add(seed)
		log.Println("📌 static seed added:", seed)
	}
}

// ===============================
// DNS SEED DISCOVERY（带超时 + IPv6 支持）
// ===============================
func (pm *PeerManager) QueryDNSSeeds() {
	seeds := []string{
		"seed1.mycoin.org",
		"seed2.mycoin.org",
		"seed.mycoin.net",
	}

	// 随机化顺序（更专业）
	rand.Shuffle(len(seeds), func(i, j int) {
		seeds[i], seeds[j] = seeds[j], seeds[i]
	})

	resolver := net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, domain := range seeds {
		ips, err := resolver.LookupHost(ctx, domain)
		if err != nil {
			log.Println("⚠ DNS seed lookup failed:", domain, err)
			continue
		}

		for _, ip := range ips {

			// IPv6 地址要加 []
			if strings.Contains(ip, ":") {
				ip = "[" + ip + "]"
			}

			addr := ip + ":9001"
			pm.AddrMgr.Add(addr)
			log.Println("🌎 DNS seed discovered:", addr)
		}
	}
}

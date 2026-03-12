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

	if pm.isSelfDialAddress(addr) {
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

	// 2. 🌟 探長修正：從 Active 名單中抓出剛剛建立好的物件
	pm.mu.Lock()
	p, ok := pm.Active[addr]
	pm.mu.Unlock()

	if ok {
		// 現在 p 是 *Peer 型別了，這就不會報錯了！
		pm.SavePeer(p)
	}
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
	// ==========================================
	// 🕵️ 大偵探新增：地址廣播 (媒人牽線邏輯)
	// 如果是別人連進來 (!outbound)，我們就把他介紹給其他人！
	// ==========================================
	if !outbound {
		go pm.RelayAddress(peer.Addr)
	}

	// ==========================================
	// 啟動讀循環 (確保能收到對方的回應)
	// ==========================================

	go peer.ReadLoop(pm.Network.Handler.OnMessage)

	// outbound：主動發 version
	if outbound {
		// 防止 Node 或 Best 還沒初始化完成導致 nil panic
		if pm.Network.Node == nil || pm.Network.Node.Best == nil {
			log.Println("⚠️ [Network] Node 尚未就緒，暫緩發送 Handshake 給", peer.Addr)
			// 你可以選擇斷開連線，或簡單地 return 讓對方稍後重試
			return
		}

		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  pm.Network.Node.Best.Height,
				CumWork: pm.Network.Node.Best.CumWork,
				NodeID:  pm.Network.Node.NodeID, // 🌟 探長急救：千萬別忘記帶身分證出門！
			},
		})
		log.Println("🚀 Sent version handshake to", peer.Addr)
	}
}

func (pm *PeerManager) RelayAddress(newAddr string) {
	// 🕵️ 偵探校正：只取 IP，強制補上你的 P2P 監聽埠口 (9001)
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
		// 1. 不發給剛連進來的那個人
		// 2. 只發給已經握手成功 (StateActive) 的老朋友
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

		// 🚫 不要连接自己的监听地址
		if pm.isSelfDialAddress(addr) {
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

func (pm *PeerManager) SavePeer(p *Peer) { // 👈 改成傳入 *Peer 物件
	info := PeerInfo{
		Addr:     p.Addr,
		LastSeen: time.Now().Unix(),
		NodeID:   p.NodeID, // 🌟 存入身分證字號
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

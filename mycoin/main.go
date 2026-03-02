package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time" // 引入 time 包

	"mycoin/miner"
	"mycoin/network"
	"mycoin/node"
	"mycoin/rpc"
	"mycoin/rpcwallet"
	"mycoin/wallet"
)

// ... (loadOrCreateMinerWallet 函數保持不變) ...
func loadOrCreateMinerWallet(path string) *wallet.Wallet {
	// ... (保持原樣) ...
	if _, err := os.Stat(path); err == nil {
		w, err := wallet.LoadWallet(path)
		if err == nil {
			fmt.Println("⛏ Miner wallet loaded:", w.Address)
			return w
		}
		fmt.Println("⚠️ 矿工钱包读取失败，重新生成:", err)
	}
	fmt.Println("矿工钱包不存在，正在生成...")
	w, _ := wallet.NewWallet()
	if err := wallet.SaveWallet(path, w); err != nil {
		fmt.Println("❌ 保存矿工钱包失败:", err)
		os.Exit(1)
	}
	fmt.Println("⛏ Miner wallet created:", w.Address)
	return w
}

func main() {
	mode := flag.String("mode", "archive", "Node mode: archive or pruned")
	datadir := flag.String("datadir", "", "Directory for all node data")
	flag.Parse()

	if *datadir == "" {
		if *mode == "archive" {
			*datadir = "archive"
		} else {
			*datadir = "pruned"
		}
	}

	os.MkdirAll(*datadir, 0755)
	// dbPath := filepath.Join(*datadir, "chain.db") // unused variable
	fmt.Println("📁 Using datadir:", *datadir)

	// -------------------------------
	// 1. 创建 Node
	// -------------------------------
	nd := node.NewNode(*mode, *datadir)
	nd.Start()

	// -------------------------------
	// 2. 载入矿工钱包
	// -------------------------------
	walletPath := filepath.Join(*datadir, "miner.dat")
	minerWallet := loadOrCreateMinerWallet(walletPath)

	// -------------------------------
	// 3. 设置挖矿地址
	// -------------------------------
	nd.MiningAddress = minerWallet.Address

	// 🔥🔥🔥 原本在這裡的「啟動礦工」移走了！ 🔥🔥🔥

	// -------------------------------
	// 4. 启动 P2P (先建立網路！)
	// -------------------------------
	handler := network.NewHandler(nd)
	net := network.NewNetwork(handler)
	handler.Network = net
	net.Node = nd

	nd.Broadcaster = handler // 這裡綁定廣播器

	listenAddr := "0.0.0.0:9001"
	publicIP := detectBestIP()
	handler.LocalVersion = network.VersionPayload{
		Version: 1,
		Height:  uint64(len(nd.Chain)),
		NodeID:  publicIP + ":9001",
	}
	fmt.Println("🔎 Node will advertise itself as:", handler.LocalVersion.NodeID)
	pm := network.NewPeerManager(net, listenAddr, 16)
	net.PeerManager = pm
	pm.Start() // 啟動監聽

	// -------------------------------
	// 5. 启动 RPC 服务
	// -------------------------------
	nodeRPC := rpc.RPCServer{
		Node:    nd,
		Handler: handler,
	}
	go nodeRPC.Start(":8081")

	walletRPC := rpcwallet.RPCServer{
		Node:    nd,
		Wallet:  minerWallet,
		Handler: handler,
	}
	go walletRPC.Start(":8082")

	fmt.Println("🟢 Full Node + Wallet RPC 已完全启动")

	// -------------------------------
	// 6. 🔥 最後才启动矿工 (確保網路已就緒)
	// -------------------------------
	// 確保 Miner 實例存在
	nd.Miner = miner.NewMiner(nd.MiningAddress, nd)

	// 給 P2P 一點時間去發現節點 (建議加這行)
	fmt.Println("⏳ 等待 5 秒讓 P2P 網路建立連線...")
	time.Sleep(5 * time.Second)

	// 啟動 Node 主控挖礦
	go nd.Mine()

	fmt.Println("⛏ Miner started (Node-controlled) with address:", nd.MiningAddress)

	// -------------------------------
	// 7. 阻塞主线程
	// -------------------------------
	select {}
}

func detectBestIP() string {
	// ... (保持不變) ...
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		local := conn.LocalAddr().(*net.UDPAddr)
		return local.IP.String()
	}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && !ipNet.IP.IsLoopback() {
				ipv4 := ipNet.IP.To4()
				if ipv4 != nil {
					return ipv4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"mycoin/miner"
	"mycoin/network"
	"mycoin/node"
	"mycoin/rpc"
	"mycoin/rpcwallet"
	"mycoin/wallet"
)

// 统一的矿工钱包加载逻辑
func loadOrCreateMinerWallet(path string) *wallet.Wallet {
	// 文件存在 → 加载
	if _, err := os.Stat(path); err == nil {
		w, err := wallet.LoadWallet(path)
		if err == nil {
			fmt.Println("⛏ Miner wallet loaded:", w.Address)
			return w
		}
		fmt.Println("⚠️ 矿工钱包读取失败，重新生成:", err)
	}

	// 文件不存在 → 生成
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
	// ⭐ 添加 mode 参数
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
	dbPath := filepath.Join(*datadir, "chain.db")
	fmt.Println("📁 Using datadir:", *datadir)
	fmt.Println("📁 DB path:", dbPath)
	// -------------------------------
	// 1. 创建 Node
	// -------------------------------
	nd := node.NewNode(*mode, *datadir)

	// ⭐ 必须先启动 Node（加载 DB / 重建链 / 恢复 Best）
	nd.Start()

	// -------------------------------
	// 2. 载入矿工钱包（固定）
	// -------------------------------
	walletPath := filepath.Join(*datadir, "miner.dat")
	minerWallet := loadOrCreateMinerWallet(walletPath)

	// -------------------------------
	// 3. 设置挖矿地址
	// -------------------------------
	nd.MiningAddress = minerWallet.Address

	// -------------------------------
	// 4. 启动矿工（自动挖矿）
	// -------------------------------
	nd.Miner = miner.NewMiner(nd.MiningAddress, nd)

	// ❌ 刪除舊的啟動方式：
	// nd.Miner.Start()

	// ✅ 使用新的 Node 主控挖礦 (包含廣播邏輯)
	// 使用 go 關鍵字讓它在背景執行，不要卡住後面的 P2P/RPC 啟動
	go nd.Mine()

	fmt.Println("⛏ Miner started with address:", nd.MiningAddress)

	// -------------------------------
	// 5. 启动 P2P
	// -------------------------------
	handler := network.NewHandler(nd)
	net := network.NewNetwork(handler)
	handler.Network = net
	net.Node = nd

	nd.Broadcaster = handler

	// 监听固定地址，不变
	listenAddr := "0.0.0.0:9001"

	// 广播外网地址给其他 peer
	publicIP := detectBestIP()
	handler.LocalVersion = network.VersionPayload{
		Version: 1,
		Height:  uint64(len(nd.Chain)),
		NodeID:  publicIP + ":9001",
	}
	fmt.Println("🔎 Node will advertise itself as:", handler.LocalVersion.NodeID)
	pm := network.NewPeerManager(net, listenAddr, 16)
	net.PeerManager = pm
	pm.Start()
	// -------------------------------
	// 6. 启动 RPC 服务
	// -------------------------------
	// Full Node RPC
	nodeRPC := rpc.RPCServer{
		Node:    nd,
		Handler: handler,
	}
	go nodeRPC.Start(":8081")

	// Wallet RPC（使用同一个矿工钱包）
	walletRPC := rpcwallet.RPCServer{
		Node:    nd,
		Wallet:  minerWallet,
		Handler: handler,
	}
	go walletRPC.Start(":8082")

	fmt.Println("🟢 Full Node + Wallet RPC 已完全启动")

	// -------------------------------
	// 7. 阻塞主线程
	// -------------------------------
	select {}
}

func detectBestIP() string {
	// 尝试检测公网 IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		local := conn.LocalAddr().(*net.UDPAddr)
		return local.IP.String()
	}

	// 尝试检测局域网 IP
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

	// fallback
	return "127.0.0.1"
}

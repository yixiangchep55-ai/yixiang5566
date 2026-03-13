package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time" // 引入 time 包

	"mycoin/api"
	"mycoin/indexer"
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
	mode := flag.String("mode", node.ModeArchive, "Node mode: archive or pruned")
	datadir := flag.String("datadir", "", "Directory for all node data")
	flag.Parse()
	*mode = node.NormalizeMode(*mode)

	if *datadir == "" {
		if *mode == node.ModeArchive {
			*datadir = node.ModeArchive
		} else {
			*datadir = node.ModePruned
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

	// ==========================================
	// 🧬 2. 提取 DNA 並初始化 PostgreSQL Indexer
	// ==========================================
	// 確保節點成功加載了區塊鏈 (至少會有 1 個創世區塊)
	if len(nd.Chain) == 0 {
		panic("🚨 嚴重錯誤：節點啟動失敗，沒有任何區塊！")
	}

	// 取得創世區塊的 Hash 並轉成 Hex 字串
	// (注意：需要 import "encoding/hex")
	genesisHash := hex.EncodeToString(nd.Chain[0].Hash)

	// 把這串 DNA 傳給 Indexer 進行比對與大掃除！
	nodeHeight := len(nd.Chain)
	indexer.InitDB(genesisHash, nodeHeight)
	// -------------------------------
	// 3. 载入矿工钱包
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

	// ==========================================
	// 🌟 探長終極修正：把大腦裡的「數字身分證」印到名片上！
	// ==========================================
	handler.LocalVersion = network.VersionPayload{
		Version: 1,
		// 💡 探長小提醒：如果你的 nd.Chain 已經棄用，建議改成 nd.Best.Height
		Height:  nd.Best.Height,  // 或者維持你原本的 uint64(len(nd.Chain)) 也可以
		CumWork: nd.Best.CumWork, // 順便把工作量也帶上
		NodeID:  nd.NodeID,       // 🚀 關鍵：放入真正的 uint64 靈魂代碼！
		Mode:    nd.Mode,
	}

	// 升級一下超帥的啟動日誌！
	fmt.Printf("🔎 Node will advertise itself with IP: %s:9001 and NodeID: %d\n", publicIP, handler.LocalVersion.NodeID)

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
	//go nd.Mine()

	fmt.Println("⛏ Miner started (Node-controlled) with address:", nd.MiningAddress)

	// ==========================================
	// 🌟 6.5 啟動區塊瀏覽器 API 伺服器 (背景執行)
	// ==========================================
	if indexer.Enabled {
		go api.StartServer("8080")
	}

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

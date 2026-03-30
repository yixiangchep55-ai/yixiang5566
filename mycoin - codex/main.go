package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mycoin/api"
	"mycoin/indexer"
	"mycoin/miner"
	"mycoin/network"
	"mycoin/node"
	"mycoin/rpc"
	"mycoin/rpcwallet"
	"mycoin/wallet"
)

func loadOrCreateMinerWallet(path string) *wallet.Wallet {
	if _, err := os.Stat(path); err == nil {
		w, err := wallet.LoadWallet(path)
		if err == nil {
			fmt.Println("Miner wallet loaded:", w.Address)
			return w
		}
		fmt.Println("Miner wallet load failed, regenerating:", err)
	}

	fmt.Println("Miner wallet not found, generating...")
	w, _ := wallet.NewWallet()
	if err := wallet.SaveWallet(path, w); err != nil {
		fmt.Println("Failed to save miner wallet:", err)
		os.Exit(1)
	}
	fmt.Println("Miner wallet created:", w.Address)
	return w
}

func main() {
	mode := flag.String("mode", node.ModeArchive, "Node mode: archive or pruned")
	datadir := flag.String("datadir", "", "Directory for all node data")
	maxPeers := flag.Int("maxpeers", 0, "Max active P2P peers (default: archive=8, pruned=4)")
	flag.Parse()
	*mode = node.NormalizeMode(*mode)

	if *datadir == "" {
		if *mode == node.ModeArchive {
			*datadir = node.ModeArchive
		} else {
			*datadir = node.ModePruned
		}
	}

	if err := os.MkdirAll(*datadir, 0o755); err != nil {
		fmt.Println("Failed to create datadir:", err)
		os.Exit(1)
	}
	fmt.Println("Using datadir:", *datadir)

	nd := node.NewNode(*mode, *datadir)
	nd.Start()

	if len(nd.Chain) == 0 {
		panic("node started without any blocks in chain")
	}

	genesisHash := hex.EncodeToString(nd.Chain[0].Hash)
	nodeHeight := len(nd.Chain)
	indexer.InitDB(genesisHash, nodeHeight)
	if required, reason := indexer.DetectBackfillNeed(nd.Chain); required {
		fmt.Printf("[Indexer] Startup check requires backfill: %s\\n", reason)
		if err := indexer.BackfillMainChain(genesisHash, nd.Chain); err != nil {
			fmt.Printf("[Indexer] Historical backfill failed: %v\\n", err)
		}
	}

	walletPath := filepath.Join(*datadir, "miner.dat")
	minerWallet := loadOrCreateMinerWallet(walletPath)
	nd.MiningAddress = minerWallet.Address

	handler := network.NewHandler(nd)
	netw := network.NewNetwork(handler)
	handler.Network = netw
	netw.Node = nd
	nd.Broadcaster = handler

	listenAddr := "0.0.0.0:9001"
	advertiseAddr := resolveAdvertiseAddr()

	handler.LocalVersion = network.VersionPayload{
		Version:       1,
		Height:        nd.Best.Height,
		CumWork:       nd.Best.CumWork,
		NodeID:        nd.NodeID,
		Mode:          nd.Mode,
		AdvertiseAddr: advertiseAddr,
	}

	fmt.Printf("Node will advertise itself with address: %s and NodeID: %d\n", advertiseAddr, handler.LocalVersion.NodeID)

	effectiveMaxPeers := *maxPeers
	if effectiveMaxPeers <= 0 {
		if *mode == node.ModePruned {
			effectiveMaxPeers = 4
		} else {
			effectiveMaxPeers = 8
		}
	}
	fmt.Printf("P2P max peers: %d\n", effectiveMaxPeers)

	pm := network.NewPeerManager(netw, listenAddr, effectiveMaxPeers)
	netw.PeerManager = pm
	pm.Start()

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

	fmt.Println("Full Node + Wallet RPC fully started")

	nd.Miner = miner.NewMiner(nd.MiningAddress, nd)

	fmt.Println("Waiting 5 seconds for P2P network to establish peers...")
	time.Sleep(5 * time.Second)

	if shouldAutostartMiner() {
		go nd.Mine()
		fmt.Println("Miner started (Node-controlled) with address:", nd.MiningAddress)
	} else {
		nd.SetMiningEnabled(false)
		fmt.Println("Miner autostart disabled by environment.")
	}

	go api.StartServer("8080")

	select {}
}

func shouldAutostartMiner() bool {
	if raw, ok := os.LookupEnv("MYCOIN_AUTOSTART_MINER"); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "0", "false", "no", "off":
			return false
		}
	}

	if raw, ok := os.LookupEnv("MYCOIN_DISABLE_MINER"); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			return false
		}
	}

	return true
}

func resolveAdvertiseAddr() string {
	if explicit := os.Getenv("MYCOIN_ADVERTISE_ADDR"); explicit != "" {
		return explicit
	}

	if publicIP := os.Getenv("MYCOIN_PUBLIC_IP"); publicIP != "" {
		return net.JoinHostPort(publicIP, "9001")
	}

	publicIP := detectBestIP()
	return net.JoinHostPort(publicIP, "9001")
}

func detectBestIP() string {
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

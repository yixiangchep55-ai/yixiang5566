package network

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"
	"os"
	"strings"
	"sync"
	"time"
)

type Handler struct {
	Node              *node.Node
	Network           *Network
	LocalVersion      VersionPayload
	debugBlockTraffic bool
	debugP2PTraffic   bool
	requestMu         sync.Mutex
	requestedBlocks   map[string]time.Time
	requestedTxs      map[string]time.Time
	deferredTxMu      sync.Mutex
	deferredLocalTxs  map[string]struct{}
}

func NewHandler(n *node.Node) *Handler {
	return &Handler{
		Node:              n,
		debugBlockTraffic: envBool("MYCOIN_DEBUG_BLOCKS"),
		debugP2PTraffic:   envBool("MYCOIN_DEBUG_P2P"),
		requestedBlocks:   make(map[string]time.Time),
		requestedTxs:      make(map[string]time.Time),
		deferredLocalTxs:  make(map[string]struct{}),
	}
}

func (h *Handler) pruneClosedPeersLocked() {
	for nodeID, p := range h.Network.Peers {
		if p == nil || p.IsClosed() {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func (h *Handler) OnMessage(peer *Peer, msg *Message) {
	if h.debugBlockTraffic && msg.Type == MsgBlock {
		fmt.Printf("🕵️ [Debug] TCP 收到 MsgBlock 來自 %s (長度 %v)\n", peer.Addr, msg.Data)
	}
	switch msg.Type {

	case MsgVersion:
		h.handleVersion(peer, msg)

	case MsgVerAck:
		h.handleVerAck(peer, msg)

	case MsgTx:
		h.handleTx(peer, msg)

	case MsgInv:
		h.handleInv(peer, msg)

	case MsgGetData:
		h.handleGetData(peer, msg)

	case MsgBlock:
		h.handleBlock(peer, msg)

	case MsgGetAddr:
		h.handleGetAddr(peer, msg)

	case MsgAddr:
		h.handleAddr(peer, msg)

	case MsgGetHeaders:
		h.handleGetHeaders(peer, msg)

	case MsgHeaders:
		h.handleHeaders(peer, msg)

	case "mempool":
		h.handleMempool(peer, msg)

	default:
		log.Println("unknown msg:", msg.Type)
	}
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func shouldPreferNewPeer(localNodeID uint64, existingPeer, newPeer *Peer) bool {
	if existingPeer == nil {
		return true
	}
	if newPeer == nil {
		return false
	}

	preferredOutbound := localNodeID < newPeer.NodeID
	existingMatches := existingPeer.Outbound == preferredOutbound
	newMatches := newPeer.Outbound == preferredOutbound

	if existingMatches != newMatches {
		return newMatches
	}

	if existingPeer.Outbound != newPeer.Outbound {
		return newPeer.Outbound == preferredOutbound
	}

	if existingPeer.Addr == newPeer.Addr {
		return false
	}

	return newPeer.Addr < existingPeer.Addr
}

func (h *Handler) reserveBlockRequest(hash string) bool {
	if hash == "" {
		return false
	}

	h.requestMu.Lock()
	defer h.requestMu.Unlock()

	now := time.Now()
	if requestedAt, exists := h.requestedBlocks[hash]; exists && now.Sub(requestedAt) < 5*time.Second {
		return false
	}

	h.requestedBlocks[hash] = now
	return true
}

func (h *Handler) releaseBlockRequest(hash string) {
	if hash == "" {
		return
	}

	h.requestMu.Lock()
	delete(h.requestedBlocks, hash)
	h.requestMu.Unlock()
}

func (h *Handler) reserveTxRequest(txid string) bool {
	if txid == "" {
		return false
	}

	h.requestMu.Lock()
	defer h.requestMu.Unlock()

	now := time.Now()
	if requestedAt, exists := h.requestedTxs[txid]; exists && now.Sub(requestedAt) < 5*time.Second {
		return false
	}

	h.requestedTxs[txid] = now
	return true
}

func (h *Handler) releaseTxRequest(txid string) {
	if txid == "" {
		return
	}

	h.requestMu.Lock()
	delete(h.requestedTxs, txid)
	h.requestMu.Unlock()
}

func (h *Handler) handleVersion(peer *Peer, msg *Message) {
	var v VersionPayload
	if err := decode(msg.Data, &v); err != nil {
		log.Println("decode version error:", err)
		return
	}

	peer.NodeID = v.NodeID

	if peer.State == StateInit {
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  h.Node.Best.Height,
				CumWork: h.Node.Best.CumWork,
				NodeID:  h.Node.NodeID,
				Mode:    h.Node.Mode,
			},
		})
		peer.State = StateVersionSent
	}

	peer.Height = v.Height
	peer.CumWork = v.CumWork
	peer.State = StateVersionRecv
	peer.NodeID = v.NodeID
	peer.Mode = node.NormalizeMode(v.Mode)

	peerWork := new(big.Int)
	shouldEvaluateSync := peer.ShouldEvaluateVersion(v.Height, v.CumWork)

	if _, ok := peerWork.SetString(v.CumWork, 16); !ok {
		peerWork.SetInt64(0)
	}

	if shouldEvaluateSync {
		h.Node.EvaluateSyncStatus(v.Height, peerWork)
	}

	peer.Send(Message{Type: MsgVerAck})
}

func (h *Handler) handleVerAck(peer *Peer, msg *Message) {
	if peer.State >= StateVersionRecv {

		h.Network.mu.Lock() // 🔒 上鎖
		h.pruneClosedPeersLocked()
		var peerToClose *Peer

		if peer.NodeID == h.Network.Node.NodeID {
			fmt.Println("❌ 警告：偵測到自我連線 (NodeID 相同)，拒絕加入名單！")
			h.Network.mu.Unlock()
			peer.CloseWithReason("self connection rejected")
			return
		}

		if existingPeer, exists := h.Network.Peers[peer.NodeID]; exists {

			if existingPeer == peer {
				h.Network.mu.Unlock()
				return
			}

			fmt.Printf("🔄 偵測到重複的節點 NodeID: %d，保留舊連線 %s，斷開新連線...\n", peer.NodeID, existingPeer.Addr)
			if shouldPreferNewPeer(h.Network.Node.NodeID, existingPeer, peer) {
				delete(h.Network.Peers, peer.NodeID)
				peerToClose = existingPeer
			} else {
				h.Network.mu.Unlock()
				peer.CloseWithReason(fmt.Sprintf("duplicate node id %d: preferred %s", peer.NodeID, existingPeer.Addr))
				return
			}
		}

		peer.State = StateActive
		log.Printf("✅ peer active: %s (NodeID: %d)\n", peer.Addr, peer.NodeID)

		h.Network.Peers[peer.NodeID] = peer
		currentCount := len(h.Network.Peers)
		h.Network.RecordPeerActive(peer.Addr, currentCount)

		h.Network.mu.Unlock()

		fmt.Printf("🔒 [Network] 已將 NodeID %d 強制加入廣播名單，目前連線數: %d\n", peer.NodeID, currentCount)

		if peerToClose != nil {
			peerToClose.CloseWithReason(fmt.Sprintf("duplicate node id %d: preferred %s", peer.NodeID, peer.Addr))
		}
		peer.Send(Message{Type: MsgGetAddr})

		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{
				Locators: h.buildBlockLocator(),
			},
		})
	}
}

func (h *Handler) handleInv(peer *Peer, msg *Message) {

	if h.debugP2PTraffic {
		fmt.Printf("🕵️ [Kali-Debug] 收到來自 %s 的 Inv 訊息！準備拆封...\n", peer.Addr)
	}

	var inv InvPayload
	if err := decode(msg.Data, &inv); err != nil {

		if h.debugP2PTraffic {
			fmt.Printf("❌ [Kali-Debug] 解碼 InvPayload 失敗！錯誤原因: %v\n", err)
		}

		if h.debugP2PTraffic {
			fmt.Printf("❌ [Kali-Debug] 原始 msg.Data 內容: %+v\n", msg.Data)
		}
		return
	}

	if h.debugP2PTraffic {
		fmt.Printf("✅ [Kali-Debug] 成功拆封 Inv，裡面有 %d 筆 %s 類型的資料\n", len(inv.Hashes), inv.Type)
	}

	switch inv.Type {
	case "block":
		for _, hashHex := range inv.Hashes {
			hashBytes, err := hex.DecodeString(hashHex)
			if err != nil {
				continue
			}
			if !h.Node.HasBlock(hashBytes) {
				h.requestBlock(peer, hashHex)
			}
		}

	case "tx":

		if h.Node.IsSyncing || h.Node.HasMissingBodies() {

			return
		}

		for _, txid := range inv.Hashes {
			if h.Node.Mempool.Has(txid) || !h.reserveTxRequest(txid) {
				continue
			}

			fmt.Printf("📥 [P2P] 看到新交易 %s，準備發送 GetData...\n", txid[:8])
			if !peer.Send(Message{
				Type: MsgGetData,
				Data: GetDataPayload{
					Type: "tx",
					Hash: txid,
				},
			}) {
				h.releaseTxRequest(txid)
			}
		}
	}
}

func (h *Handler) handleGetData(peer *Peer, msg *Message) {
	var req GetDataPayload
	if err := decode(msg.Data, &req); err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("❌ [Windows-Debug] 解碼 GetDataPayload 失敗！錯誤原因: %v\n", err)
		}
		return
	}

	switch req.Type {
	case "block":

		bi := h.Node.Blocks[req.Hash]
		if bi == nil || bi.Block == nil {
			fmt.Printf("🤷 [P2P] 鄰居 %s 索取區塊 %s，但本地已修剪或無實體資料，忽略請求。\n", peer.Addr, req.Hash[:8])
			return
		}

		dto := BlockToDTO(bi.Block, bi)
		peer.Send(Message{
			Type: MsgBlock,
			Data: dto,
		})

	case "tx":

		if h.debugP2PTraffic {
			fmt.Printf("🕵️ [Windows-Debug] 收到來自 %s 的 GetData，索取【交易】: %s\n", peer.Addr, req.Hash[:8])
		}
		tx, ok := h.Node.Mempool.Get(req.Hash)
		if !ok {
			if h.debugP2PTraffic {
				fmt.Printf("⚠️ [Windows-Debug] 找不到交易 %s\n", req.Hash[:8])
			}
			return
		}

		if h.debugP2PTraffic {
			fmt.Printf("📤 [P2P-交貨] 找到交易 %s，正在發送 MsgTx 給 %s...\n", req.Hash[:8], peer.Addr)
		}
		peer.Send(Message{
			Type: MsgTx,
			Data: TxPayload{Tx: tx},
		})
	}
}

// ======================
// block
// ======================

func (h *Handler) handleBlock(peer *Peer, msg *Message) {
	var dto BlockDTO
	if err := decode(msg.Data, &dto); err != nil {
		log.Printf("❌ [Network] Block decode error from %s: %v", peer.Addr, err)

		return
	}

	blk := DTOToBlock(dto)
	hashHex := hex.EncodeToString(blk.Hash)
	prevHex := hex.EncodeToString(blk.PrevHash)
	h.releaseBlockRequest(hashHex)

	h.Node.Lock()
	bi := h.Node.Blocks[hashHex]
	alreadyHasBody := (bi != nil && bi.Block != nil)
	shouldCheckMissing := alreadyHasBody &&
		h.Node.IsSyncing &&
		bi != nil &&
		bi.CumWorkInt != nil &&
		h.Node.Best != nil &&
		h.Node.Best.CumWorkInt != nil &&
		bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0
	h.Node.Unlock()

	if alreadyHasBody {

		if shouldCheckMissing {
			fmt.Printf("🔄 [Sync] 收到已知區塊 %d，但工作量更高，觸發補缺檢查...\n", blk.Height)
			h.requestMissingBlockBodies(peer)
		}

		return
	}

	fmt.Printf("🌐 [Network] 收到區塊: 高度 %d, Hash: %s\n", blk.Height, hashHex)

	h.Node.Lock()
	localBodyHeight := uint64(0)
	chainStartHeight := uint64(0)
	if chainLen := len(h.Node.Chain); chainLen > 0 {
		if h.Node.Chain[0] != nil {
			chainStartHeight = h.Node.Chain[0].Height
		}
		localBodyHeight = h.Node.Chain[chainLen-1].Height
	}
	mainAtHeightHash := ""
	if blk.Height >= chainStartHeight {
		idx := int(blk.Height - chainStartHeight)
		if idx >= 0 && idx < len(h.Node.Chain) {
			if mainBlock := h.Node.Chain[idx]; mainBlock != nil {
				mainAtHeightHash = hex.EncodeToString(mainBlock.Hash)
			}
		}
	}
	parent := h.Node.Blocks[prevHex]
	if parent == nil {
		if bi != nil && bi.Block == nil {
			delete(h.Node.Blocks, hashHex)
			bi = nil
		}
		h.Node.Unlock()
		fmt.Printf("⚠️ 缺少父塊 Header %s，存入孤立池\n", prevHex)
		h.Node.AddOrphan(blk)
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}
	parentHasBody := parent.Block != nil

	work := node.WorkFromTarget(blk.Target)
	if bi == nil {
		bi = &node.BlockIndex{
			Hash:       hashHex,
			PrevHash:   prevHex,
			Height:     blk.Height,
			Timestamp:  blk.Timestamp,
			Bits:       blk.Bits,
			Nonce:      blk.Nonce,
			MerkleRoot: hex.EncodeToString(blk.MerkleRoot),
		}
		if parent.CumWorkInt != nil {
			bi.CumWorkInt = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
		} else {
			bi.CumWorkInt = new(big.Int).Set(work)
		}
		bi.CumWork = bi.CumWorkInt.Text(16)
		bi.Parent = parent
		existsChild := false
		for _, child := range parent.Children {
			if child != nil && child.Hash == bi.Hash {
				existsChild = true
				break
			}
		}
		if !existsChild {
			parent.Children = append(parent.Children, bi)
		}
		h.Node.Blocks[hashHex] = bi
	} else if bi.Parent == nil {
		bi.Parent = parent
		if parent.CumWorkInt != nil {
			bi.CumWorkInt = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
			bi.CumWork = bi.CumWorkInt.Text(16)
		} else if bi.CumWorkInt == nil {
			bi.CumWorkInt = new(big.Int).Set(work)
			bi.CumWork = bi.CumWorkInt.Text(16)
		}
		existsChild := false
		for _, child := range parent.Children {
			if child != nil && child.Hash == bi.Hash {
				existsChild = true
				break
			}
		}
		if !existsChild {
			parent.Children = append(parent.Children, bi)
		}
	}
	blockHasBody := bi != nil && bi.Block != nil
	h.Node.Unlock()
	if !blockHasBody && blk.Height < localBodyHeight && mainAtHeightHash == hashHex {
		if err := h.Node.AttachHistoricalBlock(blk); err != nil {
			fmt.Printf("❌ [Prune] 歷史區塊 %d (%s) 回填失敗: %v\n", blk.Height, hashHex, err)
			return
		}
		fmt.Printf("📚 [Prune] 已回填歷史區塊 %d (%s)\n", blk.Height, hashHex[:8])
		return
	}

	if !parentHasBody {
		h.requestBlock(peer, prevHex)
		fmt.Printf("⚠️ 父塊 %s 只有標頭缺少實體，將區塊 %d 存入孤立池\n", prevHex, blk.Height)
		h.Node.AddOrphan(blk)
		return
	}

	success := h.Node.AddBlock(blk)
	if !success {

		fmt.Printf("❌ 區塊 %d (%s) 驗證失敗，拒絕接收\n", blk.Height, hashHex)
		return
	}

	var (
		isSyncing        bool
		bestHash         string
		hasMissingBodies bool
	)
	h.Node.Lock()
	isSyncing = h.Node.IsSyncing
	bestHash = ""
	if h.Node.Best != nil {
		bestHash = h.Node.Best.Hash
	}
	hasMissingBodies = h.Node.HasMissingBodiesLocked()
	h.Node.Unlock()

	if isSyncing {

		isMainChainTip := false
		isMainChainTip = (hashHex == bestHash)

		if isMainChainTip {
			fmt.Printf("🚨 [Sync] 偵測到全網最強區塊 (%d) 實體已落地！準備結算...\n", blk.Height)

			if h.finishSyncing() {
				fmt.Printf("🎓 [Network] 核心主鏈完美同步！防護罩解除，切換為正常模式！\n")

				h.requestMempool(peer)
			}
		} else {

			h.requestMissingBlockBodies(peer)
		}
	} else {

		h.requestMempool(peer)
	}

	if h.Node.StatusSnapshot().Synced {
		h.broadcastInvExcept(hashHex, peer)
	}

	if isSyncing && !hasMissingBodies {
		fmt.Printf("🚨 [事後雷達] 偵測到所有區塊皆已補齊！準備執行帳本重建...\n")
		if h.finishSyncing() {
			fmt.Printf("🎓 [Network] (雷達觸發) 鷹架與磚塊完美吻合，完成帳本重建，正式畢業！\n")
			h.requestMempool(peer)
		}
	}
}

func (h *Handler) requestMempool(peer *Peer) {
	if peer == nil {
		return
	}
	if h.Node.SyncState != node.SyncSynced || h.Node.IsSyncing || h.Node.HasMissingBodies() {
		return
	}

	fmt.Printf("📢 [P2P] 向 %s 發送 MsgMempool 請求，索取未確認交易...\n", peer.Addr)
	peer.Send(Message{
		Type: "mempool",
		Data: nil,
	})
}

func (h *Handler) handleMempool(peer *Peer, msg *Message) {
	fmt.Printf("📥 [P2P] 收到來自 %s 的 Mempool 請求\n", peer.Addr)

	var txIDs []string

	for txid := range h.Node.Mempool.GetAll() {
		txIDs = append(txIDs, txid)
	}

	if len(txIDs) > 0 {
		fmt.Printf("📤 [P2P] 發現 %d 筆未確認交易，正在打包 Inv 發送給 %s...\n", len(txIDs), peer.Addr)

		peer.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "tx",
				Hashes: txIDs,
			},
		})
	} else {
		fmt.Printf("🤷 [P2P] 我的 Mempool 是空的，無交易可提供給 %s。\n", peer.Addr)
	}
}

func (h *Handler) finishSyncing() bool {
	fmt.Println("📥 所有區塊內容已補齊，準備切換至最新鏈狀態...")

	h.Node.Lock()

	fmt.Println("🩹 執行深度鏈條修復...")
	for {
		changed := false
		for _, bi := range h.Node.Blocks {
			if bi.Height > 0 && bi.Parent == nil {
				if p, exists := h.Node.Blocks[bi.PrevHash]; exists {
					bi.Parent = p
					changed = true
				} else {

					data := h.Node.DB.Get("blocks", bi.PrevHash)

					if len(data) > 0 {
						parentBlock, err := blockchain.DeserializeBlock(data)
						if err == nil {
							pIdx := &node.BlockIndex{
								Hash:      hex.EncodeToString(parentBlock.Hash),
								Height:    bi.Height - 1,
								Block:     parentBlock,
								PrevHash:  hex.EncodeToString(parentBlock.PrevHash),
								Bits:      parentBlock.Bits,
								Timestamp: parentBlock.Timestamp,
							}
							h.Node.Blocks[pIdx.Hash] = pIdx
							bi.Parent = pIdx
							changed = true
							fmt.Printf("💾 從硬碟救援了高度 %d 的區塊指標\n", pIdx.Height)
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	var actualBest *node.BlockIndex
	for _, bi := range h.Node.Blocks {
		if bi.Block != nil && (actualBest == nil || bi.Height > actualBest.Height) {
			actualBest = bi
		}
	}
	if actualBest == nil {
		h.Node.Unlock()
		return false
	}

	oldBest := h.Node.Best
	oldChain := append([]*blockchain.Block(nil), h.Node.Chain...)
	targetBestHash := actualBest.Hash
	newMainChain := []*blockchain.Block{}
	cur := actualBest
	for cur != nil && cur.Block != nil {
		newMainChain = append([]*blockchain.Block{cur.Block}, newMainChain...)
		cur = cur.Parent
	}
	if len(newMainChain) > 0 && newMainChain[0].Height != 0 && h.Node.IsPrunedMode() {
		if err := h.Node.ActivateBestChainFromPrunedSync(actualBest); err != nil {
			fmt.Printf("❌ [Sync] pruned chain activation failed: %v\n", err)
			h.Node.Unlock()
			return false
		}

		fmt.Printf("ℹ️ [Sync] pruned 模式僅保留從高度 %d 開始的區塊實體，沿用持久化 UTXO 完成收尾。\n", newMainChain[0].Height)
		h.Node.Unlock()

		h.Node.Lock()
		if h.Node.Best == nil || h.Node.Best.Hash != targetBestHash {
			currentBestHash := ""
			if h.Node.Best != nil {
				currentBestHash = h.Node.Best.Hash
			}
			fmt.Printf("⚠️ [Sync] pruned 收尾期間鏈頭變化 (%s -> %s)，回到同步模式重試。\n", targetBestHash, currentBestHash)
			h.Node.IsSyncing = true
			h.Node.SyncState = node.SyncBodies
			h.Node.Unlock()
			return false
		}
		h.Node.SyncState = node.SyncSynced
		h.Node.IsSyncing = false
		h.Node.DB.Put("meta", "best", []byte(h.Node.Best.Hash))
		fmt.Printf("✅ 同步完成！高度: %d\n", h.Node.Best.Height)
		h.Node.Unlock()
		if h.Node.IsPrunedMode() {
			go h.Node.PruneBlocks()
		}
		h.broadcastCurrentMempool()
		return true
	}

	if len(newMainChain) == 0 || newMainChain[0].Height != 0 {
		fmt.Printf("⚠️ [Sync] 依然斷鏈！目前起點高度: %d\n",
			func() uint64 {
				if len(newMainChain) > 0 {
					fmt.Printf("🕵️ [Debug] 第 1 塊積木(Height: %d) 紀錄的爸爸 Hash 是: %x\n",
						newMainChain[0].Height, newMainChain[0].PrevHash)
					fmt.Printf("🕵️ [Debug] 我現在記憶體裡的創世塊 Hash 是: %x\n",
						h.Node.Blocks[hex.EncodeToString(blockchain.NewGenesisBlock(h.Node.Target).Hash)].Hash)
					return newMainChain[0].Height
				}
				return 999
			}())
		h.Node.Unlock()
		return false
	}

	h.Node.Chain = newMainChain
	h.Node.Best = actualBest

	h.Node.Unlock()

	fmt.Println("💰 鏈條完整！啟動全局帳本重建...")
	if err := h.Node.RebuildUTXO(); err != nil {
		fmt.Printf("❌ [Sync] full validation failed during rebuild: %v\n", err)
		h.Node.Lock()
		h.Node.Chain = oldChain
		h.Node.Best = oldBest
		h.Node.IsSyncing = true
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		return false
	}

	h.Node.Lock()
	if h.Node.Best == nil || h.Node.Best.Hash != targetBestHash || h.Node.HasMissingBodiesLocked() {
		currentBestHash := ""
		if h.Node.Best != nil {
			currentBestHash = h.Node.Best.Hash
		}
		fmt.Printf("⚠️ [Sync] 鏈頭在重建期間發生變化 (%s -> %s)，回到同步模式重試。\n", targetBestHash, currentBestHash)
		h.Node.IsSyncing = true
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		return false
	}
	h.Node.SyncState = node.SyncSynced
	h.Node.IsSyncing = false
	h.Node.DB.Put("meta", "best", []byte(h.Node.Best.Hash))
	fmt.Printf("✅ 同步完成！高度: %d\n", h.Node.Best.Height)
	h.Node.Unlock()
	if h.Node.IsPrunedMode() {
		go h.Node.PruneBlocks()
	}
	h.broadcastCurrentMempool()
	return true
}
func (h *Handler) broadcastInvExcept(hash string, except *Peer) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	for nodeID, p := range h.Network.Peers {
		if p == nil || p == except || !p.IsActive() {
			continue
		}
		if !p.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "block",
				Hashes: []string{hash},
			},
		}) {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func (h *Handler) broadcastInv(hash string) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {
		if p == nil || !p.IsActive() {
			continue
		}
		if !p.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "block",
				Hashes: []string{hash},
			},
		}) {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func decode(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (h *Handler) handleGetAddr(peer *Peer, msg *Message) {
	addrs := h.Network.PeerManager.AddrMgr.GetAll()

	if len(addrs) > 1000 {
		addrs = addrs[:1000]
	}

	peer.Send(Message{
		Type: MsgAddr,
		Data: addrs,
	})

	log.Printf("📤 Sent %d addrs to %s", len(addrs), peer.Addr)
}
func (h *Handler) handleAddr(peer *Peer, msg *Message) {
	var addrs []string
	if err := decode(msg.Data, &addrs); err != nil {
		log.Println("❌ failed to decode addr payload:", err)
		return
	}

	if len(addrs) == 0 {
		return
	}

	pm := h.Network.PeerManager

	h.Node.Lock()
	addedCount := 0
	for _, addr := range addrs {

		if pm.isSelfDialAddress(addr) {
			continue
		}

		pm.mu.Lock()
		_, exists := pm.Active[addr]
		pm.mu.Unlock()
		if exists {
			continue
		}

		if pm.AddrMgr.Add(addr) {
			addedCount++

			pm.mu.Lock()
			currentActive := len(pm.Active)
			maxPeers := pm.MaxPeers
			pm.mu.Unlock()

			if currentActive < maxPeers {
				log.Printf("🌐 [Network] 發現新鄰居 %s，立即嘗試主動建立直連...", addr)
				go pm.Connect(addr)
			}
		}
	}
	h.Node.Unlock()

	log.Printf("🌍 Received %d new addrs from %s", addedCount, peer.Addr)

	pm.ensurePeers()
}
func (h *Handler) handleTx(peer *Peer, msg *Message) {

	if h.Node.SyncState != node.SyncSynced {

		return
	}

	dataMap, ok := msg.Data.(map[string]interface{})
	if !ok {
		if h.debugP2PTraffic {
			fmt.Println("❌ [Kali-Debug] 封包格式錯誤，不是 map[string]interface{}")
		}
		return
	}

	txBase64Str, ok := dataMap["tx"].(string)
	if !ok {
		if h.debugP2PTraffic {
			fmt.Println("❌ [Kali-Debug] 找不到 'tx' 欄位，或者它不是字串！")
		}
		return
	}

	txBytes, err := base64.StdEncoding.DecodeString(txBase64Str)
	if err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("❌ [Kali-Debug] Base64 解碼失敗！錯誤: %v\n", err)
		}
		return
	}

	tx, err := blockchain.DeserializeTransaction(txBytes)
	if err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("❌ [Kali-Debug] 交易反序列化失敗！錯誤: %v\n", err)
		}
		return
	}

	h.releaseTxRequest(tx.ID)

	if h.Node.Mempool.Has(tx.ID) {

		return
	}

	if h.debugP2PTraffic {
		fmt.Printf("✅ [Kali-Debug] 成功解析交易 %s，準備交給大門保全 (AddTx)...\n", tx.ID[:8])
	}

	if ok := h.Node.AddTx(*tx, peer.NodeID); !ok {
		if h.debugP2PTraffic {
			fmt.Printf("❌ [Kali-Debug] 交易 %s 被 Node.AddTx 拒絕！\n", tx.ID[:8])
		}
		return
	}

	fmt.Printf("📥 ✅ [P2P] 交易 %s 成功從網路進入 Mempool！\n", tx.ID[:8])

	h.broadcastTxInv(tx.ID)
}
func (h *Handler) broadcastTxInv(txid string) {

	if h.debugP2PTraffic {
		fmt.Println("🕵️ [Debug] 進入 broadcastTxInv，準備廣播交易:", txid[:8])
	}

	if h.Node.SyncState != node.SyncSynced {

		if h.debugP2PTraffic {
			fmt.Printf("🚫 [Debug] 廣播被攔截！當前 SyncState 是 %v，不是 Synced!\n", h.Node.SyncState)
		}
		return
	}

	sourceNodeID := h.Node.Mempool.GetSource(txid)

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	if h.debugP2PTraffic {
		fmt.Printf("🕵️ [Debug] 網路中共有 %d 個鄰居，準備逐一檢查...\n", len(h.Network.Peers))
	}

	invMsg := Message{
		Type: MsgInv,
		Data: InvPayload{
			Type:   "tx",
			Hashes: []string{txid},
		},
	}

	count := 0
	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {
		if p == nil || !p.IsActive() {
			continue
		}
		if sourceNodeID != 0 && nodeID == sourceNodeID {
			continue
		}
		if p.Send(invMsg) {
			count++
		} else {
			delete(h.Network.Peers, nodeID)
		}
	}

	if count > 0 {
		fmt.Printf("📢 [P2P] 已向 %d 個鄰居廣播交易清單 (Inv): %s\n", count, txid[:8])
	} else if h.debugP2PTraffic {
		// 🌟 顯影劑 4：鄰居都不理我？
		fmt.Println("⚠️ [Debug] 廣播跑完了，但是 count 是 0！沒有符合條件的鄰居。")
	}
}

func (h *Handler) BroadcastLocalTx(tx blockchain.Transaction) {

	txid := tx.ID

	log.Println("[P2P] broadcast local tx:", txid)

	log.Println("📣 broadcast local tx:", txid)

	if h.Node.SyncState != node.SyncSynced {
		h.deferLocalTxBroadcast(txid)
		log.Println("[P2P] local tx queued until sync completes:", txid)
		log.Println("馃搶 [P2P] local tx queued until sync completes:", txid)
		return
	}

	h.broadcastTxInv(txid)
}

func (h *Handler) deferLocalTxBroadcast(txid string) {
	h.deferredTxMu.Lock()
	defer h.deferredTxMu.Unlock()
	h.deferredLocalTxs[txid] = struct{}{}
}

func (h *Handler) takeDeferredLocalTxs() []string {
	h.deferredTxMu.Lock()
	defer h.deferredTxMu.Unlock()

	if len(h.deferredLocalTxs) == 0 {
		return nil
	}

	txids := make([]string, 0, len(h.deferredLocalTxs))
	for txid := range h.deferredLocalTxs {
		txids = append(txids, txid)
		delete(h.deferredLocalTxs, txid)
	}
	return txids
}

func (h *Handler) broadcastCurrentMempool() {
	broadcasted := make(map[string]struct{})

	deferred := h.takeDeferredLocalTxs()
	if len(deferred) > 0 {
		fmt.Printf("[P2P] Sync finished, replaying %d deferred local txs...\n", len(deferred))
		fmt.Printf("馃摙 [P2P] 鍚屾瀹屾垚寰岃寤ｆ挱寤舵尲鐨?%d 绛嗘湰鍦颁氦鏄?..\n", len(deferred))
		for _, txid := range deferred {
			if !h.Node.Mempool.Has(txid) {
				continue
			}
			h.broadcastTxInv(txid)
			broadcasted[txid] = struct{}{}
		}
	}

	allTxs := h.Node.Mempool.GetAll()
	if len(allTxs) == 0 {
		return
	}

	fmt.Printf("📢 [P2P] 同步完成後補廣播目前 Mempool 的 %d 筆交易...\n", len(allTxs))
	for txid := range allTxs {
		if _, ok := broadcasted[txid]; ok {
			continue
		}
		h.broadcastTxInv(txid)
	}
}

func (h *Handler) handleGetHeaders(peer *Peer, msg *Message) {
	var req GetHeadersPayload
	if err := decode(msg.Data, &req); err != nil {
		log.Println("❌ [Network] 解碼 GetHeaders 失敗 (請檢查結構體標籤):", err)
		return
	}

	h.Node.Lock()
	var startHeight int64 = -1

	for _, hash := range req.Locators {

		if bi, exists := h.Node.Blocks[hash]; exists {

			if h.Node.IsOnMainChain(bi) {
				startHeight = int64(bi.Height)
				break
			}
		}
	}

	if startHeight == -1 {

		startHeight = -1
	}

	h.Node.Unlock()
	_ = startHeight

	var headers []HeaderDTO
	for _, bi := range h.Node.HeadersAfterLocators(req.Locators, 2000) {
		headers = append(headers, BlockIndexToHeaderDTO(bi))
	}

	peer.Send(Message{
		Type: MsgHeaders,
		Data: HeadersPayload{Headers: headers},
	})
}

func (h *Handler) handleHeaders(peer *Peer, msg *Message) {
	var payload HeadersPayload
	if err := decode(msg.Data, &payload); err != nil {
		log.Println("decode headers error:", err)
		return
	}

	headersCount := len(payload.Headers)
	fmt.Printf("📥 [Sync] 收到 %d 個 Headers 來自 %s\n", headersCount, peer.Addr)

	if headersCount == 0 {
		fmt.Println("✅ [Sync] 對方已無新 Headers。")
		h.Node.Lock()
		h.Node.HeadersSynced = true
		needBodies := h.Node.HasMissingBodiesLocked()
		isSyncing := h.Node.IsSyncing
		h.Node.Unlock()

		if needBodies {
			h.requestMissingBlockBodies(peer)
		} else {

			if isSyncing && h.finishSyncing() {
				fmt.Println("🎓 [Network] 鷹架與磚塊皆已完備，同步完成！請求 Mempool...")
				h.requestMempool(peer)
			}
		}
		return
	}

	h.Node.Lock()
	addedCount := 0
	for _, hdr := range payload.Headers {
		headerBlock := HeaderDTOToBlock(hdr)
		headerHash := hex.EncodeToString(headerBlock.CalcHash())
		prevHash := strings.ToLower(hdr.PrevHash)
		if !strings.EqualFold(headerHash, hdr.Hash) {
			fmt.Printf("[Security] rejected header %s: claimed hash does not match recomputed hash\n", hdr.Hash)
			continue
		}

		target := headerBlock.Target
		if target == nil || target.Sign() <= 0 {
			fmt.Printf("[Security] rejected header %s: invalid target\n", headerHash)
			continue
		}

		hashInt := new(big.Int).SetBytes(headerBlock.CalcHash())
		if hashInt.Cmp(target) > 0 {
			fmt.Printf("[Security] rejected header %s: PoW does not satisfy target\n", headerHash)
			continue
		}

		if _, ok := h.Node.Blocks[headerHash]; ok {
			continue
		}

		work := node.WorkFromTarget(target)
		var (
			parent  *node.BlockIndex
			cumWork *big.Int
		)
		if hdr.Height == 0 {
			cumWork = new(big.Int).Set(work)
		} else {
			var ok bool
			parent, ok = h.Node.Blocks[prevHash]
			if !ok {
				continue
			}
			if parent.CumWorkInt != nil {
				cumWork = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
			} else {
				cumWork = new(big.Int).Set(work)
			}
		}

		bi := &node.BlockIndex{
			Hash:       headerHash,
			PrevHash:   prevHash,
			Height:     hdr.Height,
			CumWork:    cumWork.Text(16),
			Bits:       hdr.Bits,
			Timestamp:  hdr.Timestamp,
			Nonce:      hdr.Nonce,
			MerkleRoot: hdr.MerkleRoot,
			CumWorkInt: cumWork,
		}
		h.Node.Blocks[headerHash] = bi
		if parent != nil {
			bi.Parent = parent
			existsChild := false
			for _, child := range parent.Children {
				if child != nil && child.Hash == bi.Hash {
					existsChild = true
					break
				}
			}
			if !existsChild {
				parent.Children = append(parent.Children, bi)
			}
		}

		if h.Node.Best == nil || h.Node.Best.CumWorkInt == nil || bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0 {
			h.Node.Best = bi
		}

		addedCount++
	}
	h.Node.Unlock()

	if addedCount == 0 {
		fmt.Println("✅ [Sync] 收到的 Headers 皆為已知。")
		h.Node.Lock()
		h.Node.HeadersSynced = true
		needBodies := h.Node.HasMissingBodiesLocked()
		isSyncing := h.Node.IsSyncing
		h.Node.Unlock()
		if needBodies {
			h.requestMissingBlockBodies(peer)
		} else {
			if isSyncing && h.finishSyncing() {
				fmt.Println("🎓 [Network] 鷹架與磚塊皆已完備，同步完成！請求 Mempool...")
				h.requestMempool(peer)
			}
		}
		return
	}

	if headersCount >= 500 {
		fmt.Println("🔄 [Sync] Headers 尚未收完，繼續請求下一批...")
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}

	fmt.Printf("✅ [Sync] 成功新增 %d 個 Headers。鷹架搭建完畢，開始索取實體 (Bodies)...\n", addedCount)
	h.Node.Lock()
	h.Node.HeadersSynced = true
	h.Node.Unlock()
	h.requestMissingBlockBodies(peer)
}

func (h *Handler) requestMissingBlockBodies(peer *Peer) {
	missingBlocks := h.Node.MissingBlockBodies(16)

	if len(missingBlocks) > 0 {
		h.Node.Lock()
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		requested := 0

		for i := len(missingBlocks) - 1; i >= 0; i-- {
			target := missingBlocks[i]
			if h.requestBlock(peer, target.Hash) {
				requested++
			}
		}
		if requested > 0 {
			fmt.Printf("📥 發現 %d 個缺塊，本輪新請求 %d 個...\n", len(missingBlocks), requested)
		}
		return
	}

	if h.Node.SyncState != node.SyncSynced {
		fmt.Println("[Sync] All block bodies are ready, finishing sync...")
		if h.finishSyncing() {
			fmt.Println("[Network] Sync finished, requesting mempool...")
			h.requestMempool(peer)
		}
	}
}
func (h *Handler) requestBlock(peer *Peer, hash string) bool {
	if peer == nil || hash == "" {
		return false
	}
	if !h.reserveBlockRequest(hash) {
		return false
	}

	peer.Send(Message{
		Type: MsgGetData,
		Data: GetDataPayload{
			Type: "block",
			Hash: hash,
		},
	})
	return true
}

func (h *Handler) buildBlockLocator() []string {
	var locators []string

	bi := h.Node.Best
	step := 1
	height := 0

	for bi != nil {
		locators = append(locators, bi.Hash)

		if height >= 10 {
			step *= 2
		}

		for i := 0; i < step && bi != nil; i++ {
			bi = bi.Parent
		}
		height++
	}

	return locators
}

func (h *Handler) BroadcastNewBlock(b *blockchain.Block) {

	dto := BlockToDTO(b, nil)

	log.Printf("📣 [強力廣播] 準備發送區塊: 高度 %d, Hash %x", b.Height, b.Hash)

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	activeCount := 0

	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {

		fmt.Printf("   -> 檢查 Peer %s [身分證: %d] (狀態: %d)\n", p.Addr, nodeID, p.State)

		if p != nil && p.IsActive() {
			if !p.Send(Message{
				Type: MsgBlock,
				Data: dto,
			}) {
				delete(h.Network.Peers, nodeID)
				continue
			}
			fmt.Printf("   -> ✅ 已發送 MsgBlock 給 %s [身分證: %d]\n", p.Addr, nodeID)
			activeCount++
		}
	}

	if activeCount == 0 {
		fmt.Println("⚠️ [警告] 廣播失敗：沒有任何活躍的 Peer (StateActive)！")
	}
}

func encode(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (h *Handler) countMissingBlocks() int {
	missingCount := 0

	h.Node.Lock()
	defer h.Node.Unlock()

	for _, bi := range h.Node.Blocks {

		if bi.Block == nil && bi.Height > 0 {
			missingCount++
		}
	}

	return missingCount
}

func (h *Handler) RequestHistoricalBlock(hashHex string) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	fmt.Printf("🔍 [Query] 本地無區塊 %s，正在尋找全網的 archive 節點發出協尋...\n", hashHex[:8])

	requestSent := false
	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {

		if p != nil && p.IsActive() && node.NormalizeMode(p.Mode) == node.ModeArchive {
			if !p.Send(Message{
				Type: MsgGetData,
				Data: GetDataPayload{
					Type: "block",
					Hash: hashHex,
				},
			}) {
				delete(h.Network.Peers, nodeID)
				continue
			}
			fmt.Printf("   -> 📡 已向全節點 %s 發送歷史區塊請求\n", p.Addr)
			requestSent = true
		}
	}

	if !requestSent {
		fmt.Println("⚠️ [警告] 網路上目前找不到任何 archive 全節點！歷史查詢失敗。")
	}
}

func (h *Handler) BroadcastTransaction(txid string) {
	h.broadcastTxInv(txid)
}

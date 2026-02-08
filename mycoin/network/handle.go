package network

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"

	"github.com/mitchellh/mapstructure"
)

type Handler struct {
	Node         *node.Node
	Network      *Network
	LocalVersion VersionPayload
}

func NewHandler(n *node.Node) *Handler {
	return &Handler{
		Node: n,
	}
}

func (h *Handler) OnMessage(peer *Peer, msg *Message) {
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
	default:
		log.Println("unknown msg:", msg.Type)
	}

	// ⭐ Fast Sync 完成检测（补丁 #4）
	if h.Node.IsSyncing && h.Node.HeadersSynced && h.Node.BodiesSynced {
		fmt.Println("🎉 Fast Sync complete! Rebuilding UTXO...")

		h.Node.RebuildUTXO()
		h.Node.IsSyncing = false

		fmt.Println("🎉 Node is now fully synced and valid.")
	}
}

// ======================
// version
// ======================
func (h *Handler) handleVersion(peer *Peer, msg *Message) {
	var v VersionPayload
	if err := mapstructure.Decode(msg.Data, &v); err != nil {
		log.Println("decode version error:", err)
		return
	}

	// 如果我们还未发送 version（说明是 inbound 连接）
	if peer.State == StateInit {
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  h.Node.Best.Height,
				CumWork: h.Node.Best.CumWork,
			},
		})
		peer.State = StateVersionSent
	}

	// 记录对方的版本信息
	peer.Height = v.Height
	peer.CumWork = v.CumWork
	peer.State = StateVersionRecv

	// 发送 verack
	peer.Send(Message{Type: MsgVerAck})
}

// ======================
// verack
// ======================
func (h *Handler) handleVerAck(peer *Peer, msg *Message) {
	if peer.State >= StateVersionRecv {
		peer.State = StateActive
		log.Println("✅ peer active:", peer.Addr)

		// 🌐 地址发现
		peer.Send(Message{Type: MsgGetAddr})

		// 🧱 headers-first 同步启动
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{
				Locators: h.buildBlockLocator(),
			},
		})
	}
}

// ======================
// inv
// ======================
func (h *Handler) handleInv(peer *Peer, msg *Message) {
	var inv InvPayload
	if err := decode(msg.Data, &inv); err != nil {
		return
	}

	switch inv.Type {

	case "block":
		for _, hashHex := range inv.Hashes {

			// 将 hex string → []byte（二进制共识格式）
			hashBytes, err := hex.DecodeString(hashHex)
			if err != nil {
				continue
			}

			// 用 binary hash 检查是否已有区块
			if !h.Node.HasBlock(hashBytes) {
				peer.Send(Message{
					Type: MsgGetData,
					Data: GetDataPayload{
						Type: "block",
						Hash: hashHex, // 网络上传 hex（不会变）
					},
				})
			}
		}

	case "tx":
		for _, txid := range inv.Hashes {
			if !h.Node.Mempool.Has(txid) {
				peer.Send(Message{
					Type: MsgGetData,
					Data: GetDataPayload{
						Type: "tx",
						Hash: txid,
					},
				})
			}
		}
	}
}

// ======================
// getdata
// ======================
func (h *Handler) handleGetData(peer *Peer, msg *Message) {
	var req GetDataPayload
	if err := decode(msg.Data, &req); err != nil {
		return
	}

	switch req.Type {

	case "block":
		bi := h.Node.Blocks[req.Hash]
		if bi == nil {
			return
		}

		dto := BlockToDTO(bi.Block, bi)

		peer.Send(Message{
			Type: MsgBlock,
			Data: dto,
		})

	case "tx":
		tx, ok := h.Node.Mempool.Get(req.Hash)
		if !ok {
			return
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
		return
	}

	// 🕵️‍♂️🕵️‍♂️🕵️‍♂️【監控攝像頭】🕵️‍♂️🕵️‍♂️🕵️‍♂️
	fmt.Println("========================================")
	fmt.Printf("🔍 DEBUG: 收到區塊高度: %d\n", dto.Height)
	fmt.Printf("🔍 DEBUG: DTO裡的 Bits: %d (10進位)\n", dto.Bits) // 關鍵看這裡！
	fmt.Printf("🔍 DEBUG: DTO裡的 Hash: %s\n", dto.Hash)
	fmt.Println("========================================")
	// 🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️🕵️‍♂️

	blk := DTOToBlock(dto)
	hashHex := hex.EncodeToString(blk.Hash)
	prevHex := hex.EncodeToString(blk.PrevHash)

	fmt.Printf("🌐 [Network] 收到區塊: 高度 %d, Hash: %s\n", blk.Height, hashHex)

	// 1. 防止重複處理
	bi := h.Node.Blocks[hashHex]
	if bi != nil && bi.Block != nil {
		return
	}

	// 找到或創建 Index
	if bi == nil {
		bi = &node.BlockIndex{
			Hash:     hashHex,
			PrevHash: prevHex,
			Height:   blk.Height,
		}
		bi.CumWorkInt = node.WorkFromTarget(blk.Target)
		bi.CumWork = bi.CumWorkInt.String()
		h.Node.Blocks[hashHex] = bi
	}

	// 2. 檢查父塊 (Header 與 Body)
	parent := h.Node.Blocks[prevHex]
	if parent == nil {
		fmt.Printf("⚠️ 缺少父塊 Header %s，暫存為孤立塊並補洞\n", prevHex)
		h.Node.AddOrphan(blk)
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}

	if parent.Block == nil {
		fmt.Printf("📦 缺少父塊內容 %d，存入孤立池並觸發補 Body\n", parent.Height)
		h.Node.AddOrphan(blk)
		h.requestMissingBlockBodies(peer)
		return
	}

	// 3. 驗證並接入 (成功才填入 bi.Block)
	success := h.Node.AddBlock(blk)
	if !success {
		fmt.Printf("❌ 區塊 %d 驗證失敗\n", blk.Height)
		return
	}

	// 正式填充資料與樹狀關聯
	bi.Block = blk
	bi.Parent = parent

	// 更新父塊的子節點列表 (確保樹狀結構完整)
	exists := false
	for _, child := range parent.Children {
		if child.Hash == bi.Hash {
			exists = true
			break
		}
	}
	if !exists {
		parent.Children = append(parent.Children, bi)
	}

	// 4. 處理孤立塊 (遞迴)
	if orphans, ok := h.Node.Orphans[hashHex]; ok {
		delete(h.Node.Orphans, hashHex)
		for _, orphan := range orphans {
			h.handleBlock(peer, &Message{Type: MsgBlock, Data: orphan})
		}
	}

	// 5. 同步邏輯判斷
	shouldBroadcast := false

	if h.Node.IsSyncing {
		if !h.Node.AllBodiesDownloaded() {
			// 情況 A: 還在補洞，繼續要下一塊，不廣播
			h.requestMissingBlockBodies(peer)
		} else if h.Node.HeadersSynced {
			// 情況 B: 補完最後一塊了！
			h.finishSyncing()
			shouldBroadcast = true // 同步完成，我們要把最強大的 Tip 告訴大家
		}
	} else {
		// 情況 C: 正常運行狀態下收到新塊，直接廣播
		shouldBroadcast = true
	}

	// 6. 📣 全域唯一廣播點
	if shouldBroadcast {
		// 如果剛完成同步，廣播我們現在的 Best Hash
		// 如果是收到新塊，廣播該塊的 hashHex
		targetHash := hashHex
		if h.Node.SyncState == node.SyncSynced {
			targetHash = h.Node.Best.Hash
		}

		fmt.Printf("📣 正在廣播有效區塊: %s\n", targetHash)
		h.broadcastInv(targetHash)
	}
}

func (h *Handler) finishSyncing() {
	fmt.Println("📥 所有區塊內容已補齊，正在切換至最新鏈狀態...")

	// 1. 更新標誌位
	h.Node.BodiesSynced = true
	h.Node.SyncState = node.SyncSynced
	h.Node.IsSyncing = false

	// 2. 刷新主鏈視角 (n.Chain)
	newMainChain := []*blockchain.Block{}
	cur := h.Node.Best
	for cur != nil && cur.Block != nil {
		newMainChain = append([]*blockchain.Block{cur.Block}, newMainChain...)
		cur = cur.Parent
	}
	h.Node.Chain = newMainChain

	// 3. 全局重建 UTXO (確保同步後的餘額與狀態絕對正確)
	h.Node.RebuildUTXO()

	fmt.Printf("✅ 同步完成！當前高度: %d, Tip: %s\n", h.Node.Best.Height, h.Node.Best.Hash)

}

func (h *Handler) broadcastInvExcept(hash string, except *Peer) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	for _, p := range h.Network.Peers {
		if p != except && p.State == StateActive {
			p.Send(Message{
				Type: MsgInv,
				Data: InvPayload{
					Type:   "block",
					Hashes: []string{hash},
				},
			})
		}
	}
}

// ======================
// 广播新区块
// ======================

func (h *Handler) broadcastInv(hash string) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	for _, p := range h.Network.Peers {
		if p.State == StateActive {
			p.Send(Message{
				Type: MsgInv,
				Data: InvPayload{
					Type:   "block",
					Hashes: []string{hash},
				},
			})
		}
	}
}

// ======================
// 工具：安全解码
// ======================
func decode(src any, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func (h *Handler) handleGetAddr(peer *Peer, msg *Message) {
	addrs := h.Network.PeerManager.AddrMgr.GetAll()

	// 限制 1000 个（Bitcoin Core 做法）
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

	addedCount := 0
	for _, addr := range addrs {

		if addr == pm.ListenOn ||
			addr == h.LocalVersion.NodeID {
			continue
		}

		// 跳过已连接
		pm.mu.Lock()
		_, exists := pm.Active[addr]
		pm.mu.Unlock()
		if exists {
			continue
		}

		// 加入 addrManager
		if pm.AddrMgr.Add(addr) {
			addedCount++
		}
	}

	log.Printf("🌍 Received %d new addrs from %s", addedCount, peer.Addr)

	// ⭐ 自动尝试连接更多 peer（你已有 ensurePeers）
	pm.ensurePeers()
}

func (h *Handler) handleTx(peer *Peer, msg *Message) {
	var payload TxPayload
	if err := decode(msg.Data, &payload); err != nil {
		return
	}

	txBytes := payload.Tx
	txid := blockchain.HashTxBytes(txBytes)

	if h.Node.Mempool.Has(txid) {
		return
	}

	ok := h.Node.Mempool.AddTxRBF(
		txid,
		txBytes,
		h.Node.UTXO,
	)

	if !ok {
		log.Println("❌ tx rejected:", txid)
		return
	}

	log.Println("📥 tx added:", txid)

	h.broadcastTxInv(txid)
}

func (h *Handler) broadcastTxInv(txid string) {
	if h.Node.SyncState != node.SyncSynced {
		return
	}

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	for _, p := range h.Network.Peers {
		if p.State == StateActive {
			p.Send(Message{
				Type: MsgInv,
				Data: InvPayload{
					Type:   "tx",
					Hashes: []string{txid},
				},
			})
		}
	}
}

func (h *Handler) BroadcastLocalTx(tx blockchain.Transaction) {
	txBytes := tx.Serialize()
	txid := blockchain.HashTxBytes(txBytes)

	log.Println("📣 broadcast local tx:", txid)

	h.broadcastTxInv(txid)
}

func (h *Handler) handleGetHeaders(peer *Peer, msg *Message) {
	var req GetHeadersPayload
	if err := decode(msg.Data, &req); err != nil {
		return
	}

	// fmt.Printf("🔍 [Debug] 收到 GetHeaders, Locator數: %d\n", len(req.Locators))

	// ------------------------------------------------------------------
	// 步驟 1: 尋找共同祖先
	// ------------------------------------------------------------------
	var startHeight int64 = -1

	for _, hash := range req.Locators {
		// 1. 檢查 DB 是否有此塊
		if bi, exists := h.Node.Blocks[hash]; exists {
			// 2. 關鍵：只有當這個塊在「主鏈」上時，才認可它
			if h.Node.IsOnMainChain(bi) {
				startHeight = int64(bi.Height)
				break
			}
		}
	}

	// 💡 容錯機制：
	// 如果對方傳來的 Locator 我們完全找不到（例如 Genesis 不匹配），
	// 或者是全新的節點 (Locator 為空)，我們就從頭開始發送。
	if startHeight == -1 {
		// 這裡可以選擇發送 Genesis，或者什麼都不做
		// 為了確保同步，我們從 -1 開始 (下一個就是 0)
		startHeight = -1
	}

	// ------------------------------------------------------------------
	// 步驟 2: 線性讀取主鏈 (陣列遍歷)
	// ------------------------------------------------------------------
	var headers []HeaderDTO
	const MaxHeaders = 2000

	scanHeight := startHeight + 1
	chainLen := int64(len(h.Node.Chain))

	for scanHeight < chainLen && len(headers) < MaxHeaders {
		// 直接從陣列拿，絕對不會錯！
		block := h.Node.Chain[scanHeight]

		// 轉成 HeaderDTO
		hashHex := hex.EncodeToString(block.Hash)
		if bi, ok := h.Node.Blocks[hashHex]; ok {
			headers = append(headers, BlockIndexToHeaderDTO(bi))
		}

		scanHeight++
	}

	// fmt.Printf("📤 回傳 %d 個 Headers (Height %d -> %d)\n", len(headers), startHeight+1, scanHeight-1)

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
	fmt.Printf("📥 Received %d headers from peer\n", headersCount)

	// 1. 如果對方回傳 0 個，直接結束同步
	if headersCount == 0 {
		fmt.Println("✅ Headers fully synced (Peer sent 0 headers)")
		h.Node.HeadersSynced = true
		h.requestMissingBlockBodies(peer)
		return
	}

	// 2. 處理 Header，並統計「新區塊」
	addedCount := 0 // 🔥 這是關鍵計數器！

	for _, hdr := range payload.Headers {
		// 如果資料庫已經有這個塊了，直接跳過！
		if _, ok := h.Node.Blocks[hdr.Hash]; ok {
			continue
		}

		// --- 建立 BlockIndex (保持原本邏輯) ---
		bi := &node.BlockIndex{
			Hash:     hdr.Hash,
			PrevHash: hdr.PrevHash,
			Height:   hdr.Height,
			CumWork:  hdr.CumWork,
		}
		bi.CumWorkInt = new(big.Int)
		if hdr.CumWork != "" {
			bi.CumWorkInt.SetString(hdr.CumWork, 10)
		} else {
			bi.CumWorkInt.SetInt64(0)
		}

		// 寫入內存
		h.Node.Blocks[hdr.Hash] = bi

		// 連結父子關係
		if parent, ok := h.Node.Blocks[hdr.PrevHash]; ok {
			bi.Parent = parent
			parent.Children = append(parent.Children, bi)
		}

		// 更新 Best
		if h.Node.Best == nil || bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0 {
			h.Node.Best = bi
		}

		// 處理孤塊
		if orphans, ok := h.Node.Orphans[hdr.Hash]; ok {
			for _, orphan := range orphans {
				h.handleBlock(peer, &Message{Type: MsgBlock, Data: orphan})
			}
			delete(h.Node.Orphans, hdr.Hash)
		}

		// 🔥 成功加入一個「新」塊，計數器 +1
		addedCount++
	}

	// 3. 🛑 聰明的請求邏輯 (Brake Mechanism)
	// 只有當我們「真的學到了新東西」時，才繼續要！
	if addedCount > 0 {
		fmt.Printf("🔄 收納了 %d 個新 Header (總共 %d)，繼續索取更多...\n", addedCount, headersCount)

		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{
				// 因為加入了新塊，Locator 會更新，指向更後面的位置
				Locators: h.buildBlockLocator(),
			},
		})
	} else {
		// 如果 addedCount == 0，代表對方傳來的 headers 我們全都有了。
		// 這意味著我們已經跟上對方了，不需要再浪費頻寬一直問。
		fmt.Println("✅ 收到的 Headers 都是重複的，認定同步完成！")
		h.Node.HeadersSynced = true
		h.requestMissingBlockBodies(peer)
	}
}

func (h *Handler) requestMissingBlockBodies(peer *Peer) {
	bi := h.Node.Best
	var target *node.BlockIndex

	// 1. 往回走，直到找到「最靠近創世塊」的那個缺口
	for bi != nil && bi.Height > 0 {
		if bi.Block == nil {
			target = bi
		}
		bi = bi.Parent
	}

	// 2. 如果發現還有缺塊，發送請求並返回
	if target != nil {
		fmt.Printf("📥 正在請求最舊的缺塊: 高度 %d, Hash: %s\n", target.Height, target.Hash)
		h.requestBlock(peer, target.Hash)
		return
	}

	// 3. ⭐ 關鍵修正：刪除所有 if 判斷，直接強制完成同步
	// 無論之前狀態為何，只要確認無缺塊，就觸發同步完成 -> 喚醒礦工
	fmt.Println("✅ 所有區塊內容已齊全，觸發同步完成...")
	h.finishSyncing()
}

func (h *Handler) requestBlock(peer *Peer, hash string) {
	peer.Send(Message{
		Type: MsgGetData,
		Data: GetDataPayload{
			Type: "block",
			Hash: hash,
		},
	})
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

func (h *Handler) BroadcastNewBlock(hashHex string) {
	log.Printf("📣 準備廣播新區塊: %s", hashHex) // 加入這行
	h.broadcastInv(hashHex)
}

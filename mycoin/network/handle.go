package network

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"
	"net"

	"github.com/mitchellh/mapstructure"
)

type Handler struct {
	Node         *node.Node
	Network      *Network
	LocalVersion VersionPayload
}

func (p *Peer) Close() {
	if p.Conn != nil {
		p.Conn.Close()
	}
}

func NewHandler(n *node.Node) *Handler {
	return &Handler{
		Node: n,
	}
}

func (h *Handler) OnMessage(peer *Peer, msg *Message) {

	if msg.Type == MsgBlock {
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

		// 1. 提取 IP
		host, _, _ := net.SplitHostPort(peer.Addr)

		h.Network.mu.Lock() // 🔒 上鎖

		// 2. 尋找是否有「舊的」相同 IP 連線
		var oldPeer *Peer
		for addr, existingPeer := range h.Network.Peers {
			// 跳過自己
			if addr == peer.Addr {
				continue
			}

			exHost, _, _ := net.SplitHostPort(existingPeer.Addr)
			if exHost == host {
				oldPeer = existingPeer // 找到了舊連線！
				break
			}
		}

		// 🔥🔥🔥 [關鍵修改]：採取「喜新厭舊」策略 🔥🔥🔥
		if oldPeer != nil {
			log.Printf("🔄 檢測到來自 %s 的重連 (IP 已存在)，正在清理舊連線 %s...\n", host, oldPeer.Addr)

			// 1. 從 Map 中移除舊的 Key
			delete(h.Network.Peers, oldPeer.Addr)

			// 2. 關閉舊連線的 Socket (這會觸發舊連線的 disconnect 清理邏輯)
			// 注意：我們在 Lock 裡面做 delete 是安全的，Close 是異步的
			go oldPeer.Close()

			// 3. ⚠️ 重點：我們不 return！讓程式繼續往下跑，去註冊這個新的連線
		}

		// --- 3. 註冊新連線 (原本的邏輯) ---
		peer.State = StateActive
		log.Println("✅ peer active:", peer.Addr)

		h.Network.Peers[peer.Addr] = peer
		currentCount := len(h.Network.Peers)

		h.Network.mu.Unlock() // 🔓 解鎖

		fmt.Printf("🔒 [Network] 已將 %s 強制加入廣播名單，目前連線數: %d\n", peer.Addr, currentCount)

		// 🌐 地址發現
		peer.Send(Message{Type: MsgGetAddr})

		// 🧱 headers-first 同步啟動
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
		log.Printf("❌ [Network] Block decode error from %s: %v", peer.Addr, err)
		// 為了除錯，甚至可以把原始數據印出來看
		// fmt.Printf("Raw Data: %+v\n", msg.Data)
		return
	}

	blk := DTOToBlock(dto)
	hashHex := hex.EncodeToString(blk.Hash)
	prevHex := hex.EncodeToString(blk.PrevHash)

	// 1. 檢查是否已經擁有此塊 (防止重複處理)
	bi := h.Node.Blocks[hashHex]
	alreadyHasBody := (bi != nil && bi.Block != nil)

	if alreadyHasBody {
		// 只有當我們還在同步模式，且收到這個塊所在的鏈「比我們當前的最強鏈工作量更大」時
		// 才觸發補洞邏輯。這樣可以避免被低難度的長鏈干擾。
		// bi.CumWorkInt.Cmp(...) > 0 代表 bi 的工作量大於 Best
		if h.Node.IsSyncing && bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0 {
			fmt.Printf("🔄 [Sync] 收到已知區塊 %d，但工作量更高，觸發補缺檢查...\n", blk.Height)
			h.requestMissingBlockBodies(peer)
		}

		// 已經有了，且不需要處理，直接返回
		return
	}

	fmt.Printf("🌐 [Network] 收到區塊: 高度 %d, Hash: %s\n", blk.Height, hashHex)

	// 2. 建立 Index (如果只有 Header 會走到這，如果全新的也會走到這)
	if bi == nil {
		bi = &node.BlockIndex{
			Hash:       hashHex,
			PrevHash:   prevHex,
			Height:     blk.Height,
			CumWorkInt: node.WorkFromTarget(blk.Target),
		}
		bi.CumWork = bi.CumWorkInt.Text(16)
		h.Node.Blocks[hashHex] = bi
	}

	// 3. 檢查父塊是否存在
	parent := h.Node.Blocks[prevHex]
	if parent == nil {
		fmt.Printf("⚠️ 缺少父塊 Header %s，存入孤立池\n", prevHex)
		h.Node.AddOrphan(blk)

		locators := h.buildBlockLocator()
		fmt.Printf("🔍 [Debug] 發送 GetHeaders，Locator 第一個 Hash: %s (總數: %d)\n",
			locators[0], len(locators))
		// 觸發 Header 下載
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}

	// 4. 驗證並寫入資料庫
	success := h.Node.AddBlock(blk)
	if !success {
		fmt.Printf("❌ 區塊 %d 驗證失敗\n", blk.Height)
		return
	}

	// 填充內存資料
	bi.Block = blk
	bi.Parent = parent

	// 維護樹狀結構
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

	// 6. [修復問題1] 同步接力邏輯

	// 如果我們原本在同步中
	if h.Node.IsSyncing {
		if !h.Node.AllBodiesDownloaded() {
			// 還有缺塊（Header 有但 Body 沒有），繼續要 Body
			h.requestMissingBlockBodies(peer)
			return // 如果還在要缺塊，就先別廣播了，專心同步
		} else {
			// Body 都齊了，結束同步模式
			h.finishSyncing()
		}
	}

	// 🔥🔥🔥 關鍵新增：主動索取更多區塊！ 🔥🔥🔥
	// 無論是否同步完成，我們都發送一個 GetHeaders，告訴對方我們現在最新的 Hash 是什麼
	// 如果對方有更長的鏈，它就會回傳新的 Headers 給我們
	peer.Send(Message{
		Type: MsgGetHeaders,
		Data: GetHeadersPayload{
			Locators: h.buildBlockLocator(),
		},
	})

	// 8. 廣播 (只在非同步狀態下廣播，避免同步時產生大量流量)
	// 注意：如果是初始同步(IBD)，通常不廣播，但如果是即時挖礦，必須廣播
	if h.Node.SyncState == node.SyncSynced {
		// 使用 broadcastInvExcept 避免發回給來源節點 (雖然你的 broadcastInv 也行，但 Except 更好)
		h.broadcastInvExcept(hashHex, peer)
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

	// 1️⃣ 先把 []byte 反序列化成真正的 Transaction 結構
	tx, err := blockchain.DeserializeTransaction(txBytes)
	if err != nil {
		log.Println("❌ [Network] 無法解析交易資料:", err)
		return
	}

	// ==========================================
	// 🚀 2️⃣ 關鍵修改：統一交給 Node 處理！(走正門)
	// AddTx 裡面已經有 n.mu.Lock() 保護，也有 VerifyTx 驗證，
	// 它會安全地幫你呼叫 Mempool.AddTxRBF
	// ==========================================
	if ok := h.Node.AddTx(*tx); !ok {
		log.Println("❌ tx rejected by node:", tx.ID)
		return
	}

	log.Println("📥 tx added from network:", tx.ID)

	// 3️⃣ 廣播給其他節點
	h.broadcastTxInv(tx.ID)
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

	// 1️⃣ 情況 A：對方完全沒資料 (常見於雙方都是高度 0)
	if headersCount == 0 {
		fmt.Println("✅ Headers fully synced (Peer sent 0 headers)")
		h.Node.HeadersSynced = true

		// 🔥🔥🔥 [關鍵修改]：主動判斷是否該畢業了 🔥🔥🔥
		// 如果目前狀態不是「已同步」，且檢查後發現我們並不缺塊
		// 那就代表我們已經跟對方一樣新了，必須強制結束同步！
		if h.Node.SyncState != node.SyncSynced {
			if !h.Node.HasMissingBodies() {
				fmt.Println("✨ 偵測到雙方高度一致且無缺塊，主動切換至『已同步』狀態...")
				h.finishSyncing() // 👈 這行是讓礦工開工的關鍵鑰匙！
			} else {
				// 如果雖然對方沒新 Header，但我們自己還有舊的 Body 沒抓完
				h.requestMissingBlockBodies(peer)
			}
		}
		return
	}
	addedCount := 0

	for _, hdr := range payload.Headers {
		// 如果資料庫已經有這個塊了，直接跳過
		if _, ok := h.Node.Blocks[hdr.Hash]; ok {
			continue
		}

		// --- 建立 BlockIndex ---
		bi := &node.BlockIndex{
			Hash:      hdr.Hash,
			PrevHash:  hdr.PrevHash,
			Height:    hdr.Height,
			CumWork:   hdr.CumWork,
			Bits:      hdr.Bits,
			Timestamp: hdr.Timestamp,
		}
		bi.CumWorkInt = new(big.Int)
		if hdr.CumWork != "" {
			bi.CumWorkInt.SetString(hdr.CumWork, 16)
		} else {
			bi.CumWorkInt.SetInt64(0)
		}

		h.Node.Blocks[hdr.Hash] = bi

		if parent, ok := h.Node.Blocks[hdr.PrevHash]; ok {
			bi.Parent = parent
			parent.Children = append(parent.Children, bi)
		}

		if h.Node.Best == nil || bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0 {
			h.Node.Best = bi
		}

		addedCount++
	}

	// =================================================================
	// 🔥🔥🔥 [關鍵修正邏輯] 🔥🔥🔥
	// =================================================================

	// 2️⃣ 情況 B：收到了 Header，但「全部都是重複的」 (addedCount == 0)
	if addedCount == 0 && headersCount > 0 {
		fmt.Println("✅ All received headers were already known. Headers sync complete.")
		h.Node.HeadersSynced = true

		// 🔥 同樣檢查是否可以直接進入挖礦狀態
		if !h.Node.HasMissingBodies() {
			fmt.Println("✨ 資料已齊全，切換至已同步狀態...")
			h.finishSyncing()
		} else {
			h.requestMissingBlockBodies(peer)
		}
		return
	}

	// 3️⃣ 情況 C：收到了新 Header，且數量很多，繼續請求下一批
	if addedCount > 0 && headersCount >= 500 {
		fmt.Println("🔄 Still more headers to download, requesting next batch...")
		nextReq := GetHeadersPayload{
			Locators: h.buildBlockLocator(),
		}
		data, _ := json.Marshal(nextReq)
		peer.Send(Message{Type: MsgGetHeaders, Data: data})
		return
	}

	// 4️⃣ 情況 D：最後一批新 Header
	if addedCount > 0 {
		fmt.Printf("✅ Added %d new headers. Entering body sync phase...\n", addedCount)
		h.Node.HeadersSynced = true
		h.requestMissingBlockBodies(peer)
	}
}

func (h *Handler) requestMissingBlockBodies(peer *Peer) {
	bi := h.Node.Best
	missingBlocks := []*node.BlockIndex{}

	// 1. 收集缺口，限制一次請求的數量（例如 16 個）
	for bi != nil && bi.Height > 0 {
		if bi.Block == nil {
			// 注意：我們是往回走，所以收集到的順序是 [新 -> 舊]
			missingBlocks = append(missingBlocks, bi)
		}
		bi = bi.Parent

		// 達到批量上限就停止搜尋
		if len(missingBlocks) >= 16 {
			break
		}
	}

	// 2. 如果有缺塊，按「從舊到新」的順序請求
	if len(missingBlocks) > 0 {
		fmt.Printf("📥 發現 %d 個缺塊，正在請求最舊的一批...\n", len(missingBlocks))

		// 倒序遍歷，讓請求順序變成「舊 -> 新」
		for i := len(missingBlocks) - 1; i >= 0; i-- {
			target := missingBlocks[i]
			h.requestBlock(peer, target.Hash)
		}
		return
	}

	// =================================================================
	// 🔥🔥🔥 [關鍵修改]：移除舊的阻擋條件，改用 SyncState 判斷 🔥🔥🔥
	// =================================================================

	// 舊代碼（刪除）：
	// if !h.Node.IsSyncing {
	//     return
	// }

	// 3. 檢查：如果我們現在還不是「已同步」狀態，且上面已經確認沒缺塊了
	// 那麼我們必須強制切換狀態，讓礦工開工！
	if h.Node.SyncState != node.SyncSynced {
		fmt.Println("✅ 所有區塊內容已齊全，觸發同步完成...")
		h.finishSyncing() // 👈 這裡執行後，SyncState 變成 2，礦工就會醒來
	} else {
		// 如果已經是 Synced 狀態，就什麼都不用做
		// fmt.Println("✅ 檢查完畢，區塊完整，無需動作。")
	}
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

// mycoin/network/handle.go

func (h *Handler) BroadcastNewBlock(b *blockchain.Block) {
	// 準備數據 (這裡假設你的 BlockToDTO 已經修正)
	dto := BlockToDTO(b, nil)

	log.Printf("📣 [強力廣播] 準備發送區塊: 高度 %d, Hash %x", b.Height, b.Hash)

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	activeCount := 0
	for _, p := range h.Network.Peers {
		// 🔥 除錯：印出所有 Peer 的狀態
		fmt.Printf("   -> 檢查 Peer %s (狀態: %d)\n", p.Addr, p.State)

		if p.State == StateActive {
			p.Send(Message{
				Type: MsgBlock,
				Data: dto,
			})
			fmt.Printf("   -> ✅ 已發送 MsgBlock 給 %s\n", p.Addr)
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

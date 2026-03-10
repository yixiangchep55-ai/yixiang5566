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

	case "mempool":
		h.handleMempool(peer, msg)

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

	// ==========================================================
	// 🚨 探長急救 1：抄下對方的身分證！(超級重要)
	// ==========================================================
	peer.NodeID = v.NodeID // 👈 沒抄這行，等一下 VerAck 保全會全把他們當成 0 號踢掉！

	// 如果我们还未发送 version（说明是 inbound 连接，對方主動敲門）
	if peer.State == StateInit {
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version: 1,
				Height:  h.Node.Best.Height,
				CumWork: h.Node.Best.CumWork,
				NodeID:  h.Node.NodeID, // 👈 🚨 探長急救 2：遞名片時，記得填上自己的身分證！
			},
		})
		peer.State = StateVersionSent
	}

	// 记录对方的版本信息
	peer.Height = v.Height
	peer.CumWork = v.CumWork
	peer.State = StateVersionRecv

	// ==========================================================
	// 🚨 探長智商升級包：轉換工作量並啟動動態切換！
	// ==========================================================
	peerWork := new(big.Int)
	// 假設你的 CumWork 是 16 進位字串，如果解析失敗會回傳 false，這裡做個小保護
	if _, ok := peerWork.SetString(v.CumWork, 16); !ok {
		peerWork.SetInt64(0) // 如果對方傳來爛資料，當作 0 處理
	}

	// 呼叫我們的心血結晶，讓節點決定是不是該「畢業」了！
	h.Node.EvaluateSyncStatus(v.Height, peerWork)
	// ==========================================================

	// 发送 verack
	peer.Send(Message{Type: MsgVerAck})
}

// ======================
// verack
// ======================
func (h *Handler) handleVerAck(peer *Peer, msg *Message) {
	if peer.State >= StateVersionRecv {

		h.Network.mu.Lock() // 🔒 上鎖

		// =================================================================
		// 🛑 企業級防禦 1：這是不是我自己？(防自我連線)
		// =================================================================
		// 假設你的 NodeID 存在 h.Network.Node.NodeID 裡面
		if peer.NodeID == h.Network.Node.NodeID {
			fmt.Println("❌ 警告：偵測到自我連線 (NodeID 相同)，拒絕加入名單！")
			peer.Close()
			h.Network.mu.Unlock()
			return
		}

		// =================================================================
		// 🛑 企業級防禦 2：這個身分證是不是已經在裡面了？(防重複連線)
		// =================================================================
		if existingPeer, exists := h.Network.Peers[peer.NodeID]; exists {
			fmt.Printf("🔄 偵測到重複的節點 NodeID: %d，保留舊連線 %s，斷開新連線...\n", peer.NodeID, existingPeer.Addr)
			peer.Close()
			h.Network.mu.Unlock()
			return
		}

		// =================================================================
		// ✅ 3. 註冊新連線 (🌟 探長升級：現在用 NodeID 當鑰匙了！)
		// =================================================================
		peer.State = StateActive
		log.Printf("✅ peer active: %s (NodeID: %d)\n", peer.Addr, peer.NodeID)

		// 🔥 關鍵修改：用 NodeID 存進 Map！
		h.Network.Peers[peer.NodeID] = peer
		currentCount := len(h.Network.Peers)

		h.Network.mu.Unlock() // 🔓 解鎖

		fmt.Printf("🔒 [Network] 已將 NodeID %d 強制加入廣播名單，目前連線數: %d\n", peer.NodeID, currentCount)

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
		log.Println("❌ [Network] 解碼 GetData 失敗:", err)
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
			log.Println("⚠️ [Network] 對方索取交易，但 Mempool 找不到:", req.Hash)
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
			Bits:       blk.Bits,
		}
		bi.CumWork = bi.CumWorkInt.Text(16)
		h.Node.Blocks[hashHex] = bi
	}

	// ---------------------------------------------------------
	// 3. 檢查父塊是否存在 (終極孤塊檢查)
	// ---------------------------------------------------------
	parent := h.Node.Blocks[prevHex]

	// 情況 A：完全不認識爸爸 (連 Header 都沒有)
	if parent == nil {
		fmt.Printf("⚠️ 缺少父塊 Header %s，存入孤立池\n", prevHex)
		h.Node.AddOrphan(blk)
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}

	// 情況 B：認識爸爸，但爸爸只有頭沒有身體 (半孤塊)
	if parent.Block == nil {
		fmt.Printf("⚠️ 父塊 %s 只有標頭缺少實體，將區塊 %d 存入孤立池\n", prevHex, blk.Height)
		h.Node.AddOrphan(blk)

		// 既然我們已經有 Header 了，我們不需要 GetHeaders，我們直接要他的身體！
		peer.Send(Message{
			Type: MsgGetData, // 呼叫你要區塊資料的指令 (你應該有定義這個)
			Data: GetDataPayload{
				Type: "block",
				Hash: prevHex, // 跟對方要爸爸的實體資料
			},
		})
		return
	}

	// ---------------------------------------------------------
	// 4. 驗證並寫入資料庫
	// ---------------------------------------------------------
	// 能走到這裡，代表 parent 絕對存在，而且 parent.Block 絕對不是 nil！
	success := h.Node.AddBlock(blk)
	if !success {
		// 這裡的失敗就是真的失敗了 (比如雙花、簽名錯誤等惡意攻擊)
		fmt.Printf("❌ 區塊 %d (%s) 驗證失敗，拒絕接收\n", blk.Height, hashHex)
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

	// ---------------------------------------------------------
	// 6. 同步接力與狀態切換邏輯 (探長除錯版)
	// ---------------------------------------------------------

	if h.Node.IsSyncing {
		// 檢查是否還有任何「已知 Header 但缺少 Body」的區塊
		if !h.Node.HasMissingBodies() {
			// ==========================================================
			// 🚨 探長的精準攔截點：只有「畢業成功」才准去要 Mempool！
			// ==========================================================
			if h.finishSyncing() {
				// 只有當這條鏈完整連回創世塊 (高度 0)，才會執行這裡
				h.requestMempool(peer)
			} else {
				// 斷鏈了，拒絕發放畢業證書。
				// 下面的 MsgGetHeaders 會繼續去追討剩下的區塊。
				fmt.Println("🕵️ [Debug] 同步尚未完全，攔截 Mempool 請求以防報錯。")
			}
			// ==========================================================
		} else {
			// 還有缺塊，繼續要「下一批」Body。
			h.requestMissingBlockBodies(peer)
		}
	}

	// 🔥🔥🔥 探長的引擎升級：永遠保持對新區塊的渴望 🔥🔥🔥
	// 只要收到新的區塊並成功上鏈，我們就順便問問對方：「還有更新的嗎？」
	// 這能確保我們不會卡在高度 80！
	peer.Send(Message{
		Type: MsgGetHeaders,
		Data: GetHeadersPayload{
			Locators: h.buildBlockLocator(),
		},
	})

	// ---------------------------------------------------------
	// 8. 廣播新區塊 (只在已同步狀態下進行)
	// ---------------------------------------------------------
	if h.Node.SyncState == node.SyncSynced {
		h.broadcastInvExcept(hashHex, peer)
	}
}

// ======================
// Mempool 同步機制
// ======================

// 1. 發送 Mempool 請求
func (h *Handler) requestMempool(peer *Peer) {
	fmt.Printf("📢 [P2P] 向 %s 發送 MsgMempool 請求，索取未確認交易...\n", peer.Addr)
	peer.Send(Message{
		Type: "mempool", // 定義一個新的指令字串
		Data: nil,       // 只需要一個信號，不需要 Payload
	})
}

// 2. 處理收到的 Mempool 請求
func (h *Handler) handleMempool(peer *Peer, msg *Message) {
	fmt.Printf("📥 [P2P] 收到來自 %s 的 Mempool 請求\n", peer.Addr)

	var txIDs []string

	// 透過你原本就有的 GetAll() 函數取得所有交易
	for txid := range h.Node.Mempool.GetAll() {
		txIDs = append(txIDs, txid)
	}

	if len(txIDs) > 0 {
		fmt.Printf("📤 [P2P] 發現 %d 筆未確認交易，正在打包 Inv 發送給 %s...\n", len(txIDs), peer.Addr)

		// 呼叫你原本就寫好的 MsgInv 格式，告訴對方我們有哪些交易
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

	h.Node.Lock() // 🔒 拿鎖，開始動大手術

	fmt.Println("🩹 執行深度鏈條修復...")
	for {
		changed := false
		for _, bi := range h.Node.Blocks {
			if bi.Height > 0 && bi.Parent == nil {
				if p, exists := h.Node.Blocks[bi.PrevHash]; exists {
					bi.Parent = p
					changed = true
				} else {
					// 從硬碟救援指標
					data := h.Node.DB.Get("blocks", bi.PrevHash)

					// 直接檢查長度即可，nil 也會回傳 0
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

	// 重新尋找最強鏈頭
	var actualBest *node.BlockIndex
	for _, bi := range h.Node.Blocks {
		if bi.Block != nil && (actualBest == nil || bi.Height > actualBest.Height) {
			actualBest = bi
		}
	}
	if actualBest != nil {
		h.Node.Best = actualBest
	}

	// 組裝主鏈
	newMainChain := []*blockchain.Block{}
	cur := h.Node.Best
	for cur != nil && cur.Block != nil {
		newMainChain = append([]*blockchain.Block{cur.Block}, newMainChain...)
		cur = cur.Parent
	}

	// 檢查斷鏈
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
		h.Node.Unlock() // 🔓 失敗也要解鎖！
		return false
	}

	// 數據寫入正式狀態
	h.Node.Chain = newMainChain
	h.Node.SyncState = node.SyncSynced
	h.Node.IsSyncing = false
	h.Node.DB.Put("meta", "best", []byte(h.Node.Best.Hash))

	// ==========================================
	// 🏆 探長關鍵點：手術做完了，先解鎖！
	// ==========================================
	h.Node.Unlock() // 🔓 把鎖放開，讓 RebuildUTXO 可以自己拿鎖

	fmt.Println("💰 鏈條完整！啟動全局帳本重建...")
	h.Node.RebuildUTXO() // 👈 這行會自己去處理它的鎖

	fmt.Printf("✅ 同步完成！高度: %d\n", h.Node.Best.Height)
	return true
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
		// 1. 基礎過濾：不連自己
		// 1. 基礎過濾：不連自己 (只檢查 IP 就好，身分證等連上了再給大門保全去查)
		// 🚨 探長修正：把 addr == h.LocalVersion.NodeID 刪掉！
		if addr == pm.ListenOn {
			continue
		}

		// 2. 檢查是否已經在 Active 名單中
		pm.mu.Lock()
		_, exists := pm.Active[addr]
		pm.mu.Unlock()
		if exists {
			continue
		}

		// 3. 加入地址管理器
		if pm.AddrMgr.Add(addr) {
			addedCount++

			// 🔥🔥🔥 [偵探加強邏輯] 🔥🔥🔥
			// 不要等 ensurePeers，只要目前連線數還沒滿，就直接開 Goroutine 去連！
			pm.mu.Lock()
			currentActive := len(pm.Active)
			maxPeers := pm.MaxPeers
			pm.mu.Unlock()

			if currentActive < maxPeers {
				log.Printf("🌐 [Network] 發現新鄰居 %s，立即嘗試主動建立直連...", addr)
				go pm.Connect(addr) // 直接發起連線
			}
		}
	}

	log.Printf("🌍 Received %d new addrs from %s", addedCount, peer.Addr)

	// 依然保留原有的確保邏輯作為備援
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
	// ✅ 直接使用交易原本的 ID！保證跟 Mempool 的 Key 一模一樣！
	txid := tx.ID

	log.Println("📣 broadcast local tx:", txid)

	h.broadcastTxInv(txid)
}

func (h *Handler) handleGetHeaders(peer *Peer, msg *Message) {
	var req GetHeadersPayload
	if err := decode(msg.Data, &req); err != nil {
		log.Println("❌ [Network] 解碼 GetHeaders 失敗 (請檢查結構體標籤):", err)
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

	// 1️⃣ 情況 A：對方完全沒資料 (常見於雙方都是高度 0，或重連後無新 Header)
	if headersCount == 0 {
		fmt.Println("✅ Headers fully synced (Peer sent 0 headers)")
		h.Node.HeadersSynced = true

		// 🚨 探長暴力查帳
		missingCount := 0
		h.Node.Lock()
		for _, bi := range h.Node.Blocks {
			if bi.Block == nil && bi.Height > 0 {
				missingCount++
			}
		}
		h.Node.Unlock()

		if missingCount > 0 {
			fmt.Printf("🔍 [防呆機制] 發現資料庫中仍有 %d 個缺塊，強制喚醒下載！\n", missingCount)
			h.Node.IsSyncing = true
			h.requestMissingBlockBodies(peer)
		} else {
			if !h.Node.IsSyncing {
				fmt.Println("✨ 偵測到雙方高度一致且無缺塊，嘗試切換至『已同步』狀態...")
				if h.finishSyncing() {
					h.requestMempool(peer)
				}
			}
		}
		return
	}

	// =================================================================
	// 🚨 嫌疑犯就在這！這就是節點的「胃」！絕對不能刪掉！
	// =================================================================
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
	// 🔥🔥🔥 [探長終極暴力修正] 🔥🔥🔥
	// =================================================================

	// 2️⃣ 情況 B：收到了 Header，但「全部都是重複的」 (addedCount == 0)
	if addedCount == 0 && headersCount > 0 {
		fmt.Println("✅ All received headers were already known. Headers sync complete.")
		h.Node.HeadersSynced = true

		// 🚨 探長暴力查帳
		missingCount := 0
		h.Node.Lock()
		for _, bi := range h.Node.Blocks {
			if bi.Block == nil && bi.Height > 0 {
				missingCount++
			}
		}
		h.Node.Unlock()

		if missingCount > 0 {
			fmt.Printf("🔍 [暴力防呆] 抓到了！資料庫中仍有 %d 個缺塊，強制喚醒下載！\n", missingCount)
			h.Node.IsSyncing = true
			h.requestMissingBlockBodies(peer)
		} else {
			if !h.Node.IsSyncing {
				fmt.Println("✨ 資料已齊全，嘗試切換至已同步狀態...")
				if h.finishSyncing() {
					h.requestMempool(peer)
				}
			}
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
	// 🌟 探長升級：把底線 '_' 換成 'nodeID'，把這張身分證拿出來秀！
	for nodeID, p := range h.Network.Peers {
		// 🔥 除錯：印出所有 Peer 的狀態 (加上超帥的 NodeID)
		fmt.Printf("   -> 檢查 Peer %s [身分證: %d] (狀態: %d)\n", p.Addr, nodeID, p.State)

		if p.State == StateActive {
			p.Send(Message{
				Type: MsgBlock,
				Data: dto,
			})
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

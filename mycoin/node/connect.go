package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/utils"
)

// --------------------
// 連接區塊 (核心共識邏輯)
// --------------------
func (n *Node) connectBlock(block *blockchain.Block, parent *BlockIndex) bool {

	// 🛡️ 終極防禦裝甲：確保傳進來的 parent 絕對是完整且合法的！
	if parent == nil {
		log.Panic("嚴重錯誤：connectBlock 收到了 nil 的 parent！這是系統邏輯漏洞。")
		return false
	}
	if parent.Block == nil {
		log.Panic("嚴重錯誤：connectBlock 收到了沒有 Block 實體的 parent！這不該發生。")
		return false
	}

	// ----------------------------------------------------
	// 1️⃣ 驗證難度 (Bits Check)
	// ----------------------------------------------------
	// 確保區塊頭裡的 Bits 符合協議要求
	if (parent.Height+1)%blockchain.DifficultyInterval == 0 {
		// 🔴 調整週期：計算新難度
		expectedTarget := n.retargetDifficulty(parent)
		expectedBits := utils.BigToCompact(expectedTarget)

		if expectedBits != block.Bits {
			fmt.Printf("❌ [Consensus] 難度驗證失敗 (Retarget)！預期: %d, 實際: %d\n", expectedBits, block.Bits)
			return false
		}
	} else {
		// 🔴 非調整週期：必須跟父塊難度一模一樣
		if block.Bits != parent.Bits {
			fmt.Printf("❌ [Consensus] 難度驗證失敗 (Fixed)！預期: %d, 實際: %d\n", parent.Bits, block.Bits)
			return false
		}
	}

	// 計算累積工作量
	work := computeWork(block.Target)
	cumWork := new(big.Int).Add(parent.CumWorkInt, work)

	// ----------------------------------------------------
	// 2️⃣ 驗證區塊 (UTXO & Transaction) - 僅在非同步模式下嚴格檢查
	// ----------------------------------------------------
	// 注意：如果你還沒有實作 VerifyBlockWithUTXO，請保持註解，以免編譯失敗。
	// 等你 UTXO 邏輯穩定了再開。
	if !n.IsSyncing {
		err := VerifyBlockWithUTXO(block, parent.Block, n.UTXO)
		if err != nil {
			log.Println("❌ Block validation failed:", err)
			return false
		}
	}

	// ----------------------------------------------------
	// 3️⃣ 創建或更新 BlockIndex
	// ----------------------------------------------------
	hashHex := hex.EncodeToString(block.Hash)
	bi, exists := n.Blocks[hashHex]

	if exists {
		// 情況 A: 索引已存在
		bi.Block = block
		bi.Bits = block.Bits
		bi.Timestamp = block.Timestamp
		bi.Parent = parent // 確保父子關係正確

		// 🔥 修正：強制更新工作量，不要用 if bi.CumWorkInt == nil 判斷
		// 因為 Header 同步時算的可能不準，或當時沒拿到 parent
		bi.CumWorkInt = cumWork
		bi.CumWork = cumWork.Text(16)

	} else {
		// 情況 B: 全新區塊
		bi = &BlockIndex{
			Hash:       hashHex,
			PrevHash:   parent.Hash,
			Height:     parent.Height + 1,
			Timestamp:  block.Timestamp,
			Bits:       block.Bits,
			CumWork:    cumWork.Text(16),
			CumWorkInt: cumWork,
			Block:      block,
			Parent:     parent,
			Children:   []*BlockIndex{},
		}
		n.Blocks[hashHex] = bi
	}

	// 建立父子連結（不論 exists 與否都確保一下）

	// 檢查是否已經在 Children 裡，避免重複添加
	alreadyChild := false
	for _, child := range parent.Children {
		if child.Hash == hashHex {
			alreadyChild = true
			break
		}
	}
	if !alreadyChild {
		parent.Children = append(parent.Children, bi)
	}

	// ----------------------------------------------------
	// 4️⃣ 持久化 (先存 DB，確保重啟不丟失)
	// ----------------------------------------------------
	n.DB.Put("blocks", hashHex, block.Serialize())
	idxBytes, _ := json.Marshal(bi)
	n.DB.Put("index", hashHex, idxBytes)

	if bi.Height >= n.Best.Height { // 只在高度接近時印出，避免洗版
		fmt.Printf("⚖️ [Chain Selection] Local Best: %d (Work: %s) vs New Block: %d (Work: %s)\n",
			n.Best.Height,
			n.Best.CumWorkInt.Text(16), // 印出 16 進制工作量
			bi.Height,
			bi.CumWorkInt.Text(16), // 印出 16 進制工作量
		)
	}

	// ----------------------------------------------------
	// 5️⃣ 鏈選擇邏輯 (Chain Selection)
	// ----------------------------------------------------
	chainSwitched := false

	// ==========================================================
	// 🚨 探長的終極綠色通道：同步期間只存積木，不接主鏈，不算帳！
	// ==========================================================
	if n.IsSyncing {
		// 在同步期間，我們只負責把區塊存進 n.Blocks 和 DB，建立父子關係。
		// 絕對不要去更新 n.Best 或是呼叫 n.updateUTXO()！
		// 所有的結算，都會在 handle.go 的 finishSyncing 裡面一次搞定！
		return true
	}
	// ==========================================================

	// 下面這些，只有在「非同步狀態（平常挖礦、日常接收新塊）」時才會執行！
	if parent == n.Best {
		n.Best = bi
		n.Chain = append(n.Chain, block)
		n.updateUTXO(block) // 增量更新帳本

		log.Printf("⛏️ Main chain extended to height: %d (Hash: %s)\n", bi.Height, hashHex)
		chainSwitched = true

	} else if bi.CumWorkInt.Cmp(n.Best.CumWorkInt) > 0 {
		log.Printf("🔁 REORG DETECTED! Current Best: %d, New Best: %d\n", n.Best.Height, bi.Height)

		oldChain, newChain := n.reorgTo(bi)
		n.rebuildChain(oldChain, newChain, bi)

		// 🏆 Kali 救星：在切換主鏈後，徹底重新掃描帳本
		fmt.Println("🔄 執行核彈級動態鏈重組 (Full UTXO Rebuild)...")
		n.RebuildUTXO()

		chainSwitched = true
	}

	if chainSwitched {
		// 1. 持久化最新鏈頭 (Tip)
		n.DB.Put("meta", "best", []byte(n.Best.Hash))

		// 2. 🚀 強制同步：確保 Kali 這種快速同步的節點，硬碟裡的帳本與記憶體完全一致
		n.UTXO.FlushToDB()

		// 3. 🧹 清理 Mempool
		txCount := 0
		for _, tx := range block.Transactions {
			if !tx.IsCoinbase {
				n.Mempool.Remove(tx.ID)
				txCount++
			}
		}
		fmt.Printf("🧹 [Mempool] 已清理區塊 %d 中的 %d 筆交易\n", block.Height, txCount)

		// 4. 🚀 發送中斷信號給礦工 (若當前正在挖礦)
		select {
		case n.MinerResetChan <- true:
			fmt.Println("⚡ [Consensus] 鏈頭更新，已通知礦工重新計算")
		default:
			// 頻道已滿，代表信號已在處理中，安全跳過
		}
	}

	return true

}
func (n *Node) attachOrphans(parentHash string) {
	n.mu.Lock() // 🔒 短暫上鎖，安全提取孤塊名單
	orphans := n.Orphans[parentHash]
	if len(orphans) == 0 {
		n.mu.Unlock()
		return
	}
	delete(n.Orphans, parentHash)
	n.mu.Unlock() // 🔓 拿完名單立刻解鎖！

	// 解鎖後再慢慢加入區塊，完美避開死鎖！
	for _, blk := range orphans {
		n.AddBlock(blk)
	}
}

// 安全版的 reorgTo，防止 nil pointer panic
func (n *Node) reorgTo(newTip *BlockIndex) (oldChain []*BlockIndex, newChain []*BlockIndex) {
	oldTip := n.Best

	// 1. 防禦性檢查：如果任一端點為空，無法重組
	if oldTip == nil || newTip == nil {
		return nil, nil
	}

	a := oldTip
	b := newTip

	// 2. 尋找共同祖先 (加入 nil 檢查防止崩潰)
	// 讓高度較高的指針先往回退
	for a.Height > b.Height {
		a = a.Parent
		if a == nil {
			return nil, nil
		} // 🔥 安全檢查移到這裡
	}

	for b.Height > a.Height {
		b = b.Parent
		if b == nil {
			return nil, nil
		} // 🔥 安全檢查移到這裡
	}

	// 3. 兩者同時往回退，直到 Hash 相同
	for a != nil && b != nil && a != b {
		a = a.Parent
		b = b.Parent
	}

	// 如果找不到共同祖先（斷鏈），直接返回
	if a == nil || b == nil {
		return nil, nil
	}

	commonAncestor := a

	// 4. 構建 oldChain (回滾路徑)
	cur := oldTip
	for cur != nil && cur != commonAncestor {
		oldChain = append(oldChain, cur)
		cur = cur.Parent
	}

	// 5. 構建 newChain (前進路徑)
	var tmp []*BlockIndex
	cur = newTip
	for cur != nil && cur != commonAncestor {
		tmp = append(tmp, cur)
		cur = cur.Parent
	}

	// 反轉 newChain
	for i := len(tmp) - 1; i >= 0; i-- {
		newChain = append(newChain, tmp[i])
	}

	return oldChain, newChain
}

func (n *Node) indexTransactions(block *blockchain.Block, bi *BlockIndex) {
	blockHashHex := hex.EncodeToString(block.Hash) // 因为区块哈希是 binary

	for i, tx := range block.Transactions {

		// tx.ID 已经是 hex string，所以直接用
		txidHex := tx.ID

		idx := blockchain.TxIndexEntry{
			BlockHash: blockHashHex, // hex
			Height:    bi.Height,
			TxOffset:  i,
		}

		data, _ := json.Marshal(idx)

		// key 必须是字符串（hex）
		n.DB.Put("txindex", txidHex, data)
	}
}

func (n *Node) removeTxIndex(block *blockchain.Block) {
	for _, tx := range block.Transactions {
		n.DB.Delete("txindex", tx.ID)
	}
}

func (n *Node) removeConfirmedTxs(block *blockchain.Block) {
	for _, tx := range block.Transactions {
		if !tx.IsCoinbase {
			n.DB.Delete("mempool", tx.ID)
			n.Mempool.Remove(tx.ID)
		}
	}
}

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
	bi, exists := n.Blocks[hashHex] // 改名 exists 比較直觀

	if exists {
		// 情況 A: 索引已存在 (Header 同步過，或重複收到)
		// 我們必須補齊所有可能的缺漏數據
		bi.Block = block
		bi.Bits = block.Bits           // 🔥 強制更新 Bits (修復 0 的關鍵)
		bi.Timestamp = block.Timestamp // 🔥 強制更新時間戳

		// 如果之前沒有 CumWork (例如孤塊亂序)，現在補上
		if bi.CumWorkInt == nil {
			bi.CumWorkInt = cumWork
			bi.CumWork = cumWork.String()
		}
	} else {
		// 情況 B: 全新區塊 (自己挖的，或沒經過 Header 同步)
		bi = &BlockIndex{
			Hash:       hashHex,
			PrevHash:   parent.Hash,
			Height:     parent.Height + 1,
			Timestamp:  block.Timestamp, // ✅ 正確
			Bits:       block.Bits,      // ✅ 正確
			CumWork:    cumWork.String(),
			CumWorkInt: cumWork,
			Block:      block,
			Parent:     parent,
			Children:   []*BlockIndex{},
		}
		n.Blocks[hashHex] = bi
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

	// 情況 A: 正常延伸主鏈 (Extend)
	if parent == n.Best {
		n.Best = bi

		// 1. 更新內存 Chain 視圖
		n.Chain = append(n.Chain, block)

		// 2. 更新 UTXO (增量更新)
		n.updateUTXO(block)

		// 3. 清理 Mempool
		n.removeConfirmedTxs(block)

		log.Printf("⛏️ Main chain extended to height: %d (Hash: %s)\n", bi.Height, hashHex)
		chainSwitched = true

		// 剪枝邏輯 (可選)
		// if n.Mode == "pruned" ...

	} else if bi.CumWorkInt.Cmp(n.Best.CumWorkInt) > 0 {
		// 情況 B: 觸發重組 (Reorg) - 工作量 > 當前主鏈
		log.Printf("🔁 REORG DETECTED! Current Best: %d, New Best: %d\n", n.Best.Height, bi.Height)

		// 1. 計算路徑 (需下方的輔助函數)
		oldChain, newChain := n.reorgTo(bi)

		// 2. 執行重組 (利用你 node.go 已有的 rebuildChain)
		// 你的 rebuildChain 已經包含了 UTXO 重建和 Mempool 處理
		n.rebuildChain(oldChain, newChain, bi)

		chainSwitched = true
	} else {
		// 情況 C: 側鏈 (Side Chain)
		// log.Printf("ℹ️ 收到側鏈區塊 高度 %d (未切換)\n", bi.Height)
	}

	// 只有當主鏈變更時，才更新 meta 中的 best
	if chainSwitched {
		n.DB.Put("meta", "best", []byte(n.Best.Hash))
	}

	// ----------------------------------------------------
	// 6️⃣ 處理孤塊
	// ----------------------------------------------------
	n.attachOrphans(hashHex)

	return true
}
func (n *Node) attachOrphans(parentHash string) {
	orphans := n.Orphans[parentHash]
	if len(orphans) == 0 {
		return
	}
	delete(n.Orphans, parentHash)

	for _, blk := range orphans {
		n.AddBlock(blk) // 尝试看 orphan 是否能加入
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

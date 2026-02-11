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

func (n *Node) connectBlock(block *blockchain.Block, parent *BlockIndex) bool {

	// ----------------------------------------------------
	// 1️⃣ 驗證難度 (🔴 修正：絕對不要修改 block.Target)
	// ----------------------------------------------------
	if (parent.Height+1)%blockchain.DifficultyInterval == 0 {

		// 🔥 修改 1：使用 := (短宣告)，直接在這裡定義並賦值
		expectedTarget := n.retargetDifficulty(parent)

		// 2. 將 Target 轉回 Bits
		expectedBits := utils.BigToCompact(expectedTarget)

		// 3. 比較 Bits
		if expectedBits != block.Bits {
			fmt.Printf("❌ [Consensus] 難度驗證失敗！預期 Bits: %d, 實際 Bits: %d\n", expectedBits, block.Bits)
			return false
		}
	} else {
		// 非調整週期，難度應該與父塊相同
		// 如果你的 BlockIndex 結構裡有 Bits，可以直接比：
		// if parent.Bits != block.Bits { return false }

		// 如果沒有存 Bits，暫時可以不做檢查，或者假設它是對的
		// 因為我們把 expectedTarget 的宣告拿掉了，這裡的 else 就不用做任何事了
	}

	// ✅ 計算工作量時，必須使用區塊原本的 Target
	work := computeWork(block.Target)
	cumWork := new(big.Int).Add(parent.CumWorkInt, work)

	// ----------------------------------------------------
	// 2️⃣ 驗證區塊 (UTXO)
	// ----------------------------------------------------
	if !n.IsSyncing {
		// 注意：如果是 Reorg 發生的分支區塊，這裡基於當前 UTXO 驗證可能會失敗
		// 但通常為了安全，還是先驗證。如果 Reorg 邏輯夠強，可以移到 Reorg 內部做二次驗證。
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
	bi, ok := n.Blocks[hashHex]

	if ok {
		// FastSync 補洞：填入 Body
		bi.Block = block
	} else {
		bi = &BlockIndex{
			Hash:       hashHex,
			PrevHash:   parent.Hash,
			Height:     parent.Height + 1,
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
	// 4️⃣ 鏈選擇邏輯 (Chain Selection)
	// ----------------------------------------------------
	chainSwitched := false // 標記是否切換了主鏈

	// 情況 A: 正常延伸主鏈
	if parent == n.Best {
		n.Best = bi
		n.appendBlock(block) // 寫入區塊檔
		n.indexTransactions(block, bi)
		n.updateUTXO(block)         // 🟢 確保你有這個函數來更新 UTXO 集合！
		n.removeConfirmedTxs(block) // 從 Mempool 移除

		log.Printf("⛏️ Main chain extended to height: %d (Hash: %s)\n", bi.Height, hashHex)
		chainSwitched = true

		// 剪枝邏輯
		if n.Mode == "pruned" && bi.Height > PruneDepth {
			n.PruneBlocks(bi.Height - PruneDepth)
		}

	} else if bi.CumWorkInt.Cmp(n.Best.CumWorkInt) > 0 {
		// 情況 B: 觸發重組 (Reorg)
		log.Printf("🔁 REORG DETECTED! Current Best: %d, New Best: %d\n", n.Best.Height, bi.Height)

		// 1. 執行重組：回滾舊鏈，應用新鏈
		// 你的 reorgTo 應該負責處理 UTXO 的 Revert 和 Apply
		oldChain, newChain := n.reorgTo(bi)

		n.rebuildChain(oldChain, newChain, bi)

		// 2. 🔴 Mempool 修正：
		// 舊鏈被遺棄 -> 交易復活 (加回 Mempool)
		for _, o := range oldChain {
			if o.Block != nil {
				n.addTxsToMempool(o.Block.Transactions)
			}
		}

		// 新鏈被確認 -> 交易移除 (從 Mempool 刪除)
		for _, nBlock := range newChain {
			if nBlock.Block != nil {
				n.removeConfirmedTxs(nBlock.Block)
			}
		}

		chainSwitched = true
	} else {
		// 情況 C: 側鏈 (Side Chain)
		// 雖然是有效區塊，但工作量沒贏過主鏈，所以只存 Index，不切換 Best
		// log.Printf("💡 收到側鏈區塊 高度 %d (未切換)\n", bi.Height)
	}

	// ----------------------------------------------------
	// 5️⃣ 持久化
	// ----------------------------------------------------
	n.DB.Put("blocks", hashHex, block.Serialize())

	idxBytes, _ := json.Marshal(bi)
	n.DB.Put("index", hashHex, idxBytes)

	// 只有當主鏈變更時，才更新 meta 中的 best
	if chainSwitched {
		n.DB.Put("meta", "best", []byte(n.Best.Hash))
	}

	// ----------------------------------------------------
	// 6️⃣ 處理孤塊
	// ----------------------------------------------------
	n.attachOrphans(hashHex)

	// 返回是否成功接入 (只要驗證通過就算 true，不管有沒有切換主鏈)
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

func (n *Node) reorgTo(newTip *BlockIndex) (oldChain []*BlockIndex, newChain []*BlockIndex) {

	oldTip := n.Best

	// 1️⃣ 定位共同祖先（common ancestor）
	a := oldTip
	b := newTip

	for a.Height > b.Height {
		a = a.Parent
	}
	for b.Height > a.Height {
		b = b.Parent
	}

	// 直到找到共同祖先
	for a.Hash != b.Hash {
		a = a.Parent
		b = b.Parent
	}
	commonAncestor := a

	// 2️⃣ oldChain = 从旧主链 tip 回滚到 common ancestor
	cur := oldTip
	for cur != commonAncestor {
		oldChain = append(oldChain, cur)
		cur = cur.Parent
	}

	// 3️⃣ newChain = 从 newTip 向上回溯到 common ancestor
	// 但顺序是反的，需要反转
	tmp := []*BlockIndex{}
	cur = newTip
	for cur != commonAncestor {
		tmp = append(tmp, cur)
		cur = cur.Parent
	}

	// 反转使顺序变成 commonAncestor → newTip
	for i := len(tmp) - 1; i >= 0; i-- {
		newChain = append(newChain, tmp[i])
	}

	// 4️⃣ 更新主链 tip
	n.Best = newTip

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

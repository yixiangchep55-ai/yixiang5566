package node

import (
	"encoding/json"
	"fmt"
	"mycoin/blockchain"
)

// PruneDepth 定義修剪節點要保留的安全距離 (最近 2000 個區塊)
const PruneDepth = 2000

// PruneBlocks 刪除高度 < beforeHeight 的區塊實體 (Body)
func (n *Node) PruneBlocks() {
	// ==========================================
	// 🌟 探長防護 1：全節點 (Archival) 絕對不能修剪！
	// ==========================================
	if n.IsArchiveMode() {
		// Archival 節點要當歷史老師，不能刪資料
		return
	}

	n.mu.Lock()
	bestHeight := uint64(0)
	if n.Best != nil {
		bestHeight = n.Best.Height
	}
	n.mu.Unlock()

	// ==========================================
	// 🌟 探長防護 2：還沒長大，不需要修剪
	// ==========================================
	if bestHeight <= PruneDepth {
		return
	}

	// 計算修剪基準線：只保留最近 PruneDepth 個區塊
	beforeHeight := bestHeight - PruneDepth

	fmt.Printf("🧹 [Prune] 啟動瘦身！模式: %s, 準備刪除高度 < %d 的區塊實體...\n", n.Mode, beforeHeight)

	// 先收集待刪除的 block hashes
	var toPrune []string

	n.DB.Iterate("index", func(k, v []byte) {
		var bi BlockIndex
		if err := json.Unmarshal(v, &bi); err != nil {
			fmt.Println("⚠️ [Prune] 發現損壞的 BlockIndex 紀錄:", err)
			return
		}

		// 🛡️ 永遠保留創世區塊 (Genesis)
		if bi.Height == 0 {
			return
		}

		// 🎯 核心邏輯：只要高度小於基準線，不管是不是主鏈，全部列入刪除名單！
		// (這取代了原本超級吃效能的 isAncestor)
		if bi.Height < beforeHeight {
			toPrune = append(toPrune, bi.Hash)
		}
	})

	// -----------------------------------------------------
	// 第二階段：統一物理刪除區塊實體 (不會破壞 Iterator)
	// -----------------------------------------------------
	if len(toPrune) == 0 {
		return
	}

	prunedCount := 0
	for _, hash := range toPrune {
		var blockToPrune *blockchain.Block

		n.mu.Lock()
		if bi, exists := n.Blocks[hash]; exists && bi.Block != nil {
			blockToPrune = bi.Block
			bi.Timestamp = blockToPrune.Timestamp
			bi.Bits = blockToPrune.Bits
			bi.Nonce = blockToPrune.Nonce
			bi.MerkleRoot = fmt.Sprintf("%x", blockToPrune.MerkleRoot)
			bi.Block = nil // 👈 探長關鍵：這樣才算真的釋放記憶體！
			if idxBytes, err := json.Marshal(bi); err == nil {
				n.DB.Put("index", hash, idxBytes)
			}
			prunedCount++
		}
		n.mu.Unlock()

		if blockToPrune != nil {
			n.markBlockTransactionsPruned(hash, blockToPrune, true)
		}

		// 1. 刪除硬碟中的實體數據
		n.DB.Delete("blocks", hash)

		// ⭐ 注意：我們沒有刪除 DB 裡的 "index" 和 "meta"，
		// 所以 bi.Hash, bi.PrevHash, bi.Height 等鷹架都還在！
	}

	n.UpdateChainFromBest()

	fmt.Printf("✅ [Prune] 瘦身完成！共清理了 %d 個舊區塊實體，釋放了大量空間。\n", prunedCount)
}

// ⚠️ 原本的 isAncestor 函數已經不需要了，請直接刪除，避免拖垮效能！

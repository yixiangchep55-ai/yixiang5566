package node

import (
	"encoding/json"
	"fmt"
)

const PruneDepth = 2000

// 删除高度 < beforeHeight 的区块 body
func (n *Node) PruneBlocks(beforeHeight uint64) {

	fmt.Printf("🧹 Pruning blocks < %d (safe prune)\n", beforeHeight)

	// 先收集待删除的 block hashes（不能边遍历边删）
	toPrune := []string{}

	n.DB.Iterate("index", func(k, v []byte) {
		var bi BlockIndex
		if err := json.Unmarshal(v, &bi); err != nil {
			fmt.Println("⚠️ Corrupted BlockIndex entry:", err)
			return
		}

		// 永远保留 genesis
		if bi.Height == 0 {
			return
		}

		// 永远保留 best 及其所有 ancestor（完整主链）
		// 你可以用 Parent 链判断，简单写法是：
		if n.Best != nil && bi.Height > n.Best.Height {
			return
		}
		if n.isAncestor(n.Best, bi.Hash) {
			return
		}

		// height-based prune
		if bi.Height < beforeHeight {
			toPrune = append(toPrune, bi.Hash)
		}
	})

	// -----------------------------------------------------
	// 第二阶段：统一删除 block bodies（不会破坏 iterator）
	// -----------------------------------------------------
	for _, hash := range toPrune {
		n.DB.Delete("blocks", hash)
		// ⭐ 不删除 index（关键）
		// ⭐ 不删除 BlockIndex（关键）
		// ⭐ 不删除 parent/children（关键）
		fmt.Println("🗑️ pruned block body:", hash)
	}

	fmt.Printf("✅ Safe prune complete. Pruned %d block bodies.\n", len(toPrune))
}

// -----------------------------------------------------
// 辅助函数：判断 b 是否是 a 的 ancestor
// -----------------------------------------------------
func (n *Node) isAncestor(tip *BlockIndex, targetHash string) bool {
	cur := tip
	for cur != nil {
		if cur.Hash == targetHash {
			return true
		}
		cur = cur.Parent
	}
	return false
}

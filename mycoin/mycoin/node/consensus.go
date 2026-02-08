package node

import (
	"fmt"
	"math/big"
	"mycoin/utils"
)

// 這裡定義你的難度參數
const (
	DifficultyInterval = 10
	TargetSpacing      = 30 // seconds (注意：我改回 10 秒方便測試，你原本寫 1 秒也可以)
	IntervalTimespan   = DifficultyInterval * TargetSpacing
)

func (n *Node) retargetDifficulty(last *BlockIndex) *big.Int {
	// 1. 找到舊週期的第一個區塊
	firstHeight := last.Height - DifficultyInterval + 1
	if int(firstHeight) < 0 {
		firstHeight = 0
	}

	first := last
	// 安全檢查：防止 first 為 nil
	for first.Parent != nil && first.Height > firstHeight {
		first = first.Parent
	}

	// ---------------------------------------------------------
	// ⭐ 關鍵修改：從 BlockIndex 讀取 Timestamp，而不是 Block
	// ---------------------------------------------------------
	// 原本是: actualTimespan := last.Block.Timestamp - first.Block.Timestamp
	// 現在改為:
	actualTimespan := last.Timestamp - first.Timestamp

	// 限制上下限（/4 ～ ×4）
	minTimespan := int64(IntervalTimespan / 4)
	maxTimespan := int64(IntervalTimespan * 4)

	if actualTimespan < minTimespan {
		actualTimespan = minTimespan
	}
	if actualTimespan > maxTimespan {
		actualTimespan = maxTimespan
	}

	// ---------------------------------------------------------
	// ⭐ 關鍵修改：安全獲取 oldTarget
	// ---------------------------------------------------------
	var oldTarget *big.Int
	if last.Block != nil {
		oldTarget = last.Block.Target
	} else {
		// 如果沒有 Block 體，我們暫時沿用當前節點的 Target
		// (為了更精確，建議以後在 BlockIndex 裡也存 Target/Bits)
		// 這裡做一個防崩潰處理：
		oldTarget = n.Target
		// 更好的做法是去讀 last.Bits 解碼，但暫時先這樣防止 Panic
	}

	// newTarget = oldTarget * actualTimespan / IntervalTimespan
	newTarget := new(big.Int).Mul(oldTarget, big.NewInt(actualTimespan))
	newTarget.Div(newTarget, big.NewInt(int64(IntervalTimespan)))

	// 最大目標（最小難度）檢查
	// 這裡直接用 n.Target (創世難度)，它通常就是 MaxTarget
	if newTarget.Cmp(n.Target) > 0 {
		newTarget.Set(n.Target)
	}

	fmt.Printf("⏱ Difficulty retarget:\n actualTimespan = %d\n old = %s\n new = %s\n",
		actualTimespan, utils.FormatTargetHex(oldTarget), utils.FormatTargetHex(newTarget),
	)

	return newTarget
}

func (n *Node) GetCurrentTarget() *big.Int {
	// 防禦性檢查
	if n.Best == nil {
		return new(big.Int).Set(n.Target)
	}

	last := n.Best

	// 創世塊
	if last.Height == 0 {
		return new(big.Int).Set(n.Target)
	}

	// 不到週期 → 返回當前區塊難度
	if (last.Height+1)%DifficultyInterval != 0 {
		if last.Block != nil {
			return new(big.Int).Set(last.Block.Target)
		}
		// 如果只有 Header (Block==nil)，暫時返回 n.Target 或上一級難度
		// 這裡為了不崩潰，返回 n.Target (理想情況應該從 Bits 還原)
		return new(big.Int).Set(n.Target)
	}

	// 到週期 → 調 difficulty retarget
	return n.retargetDifficulty(last)
}

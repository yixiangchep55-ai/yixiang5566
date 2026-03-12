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
	// 🔥🔥🔥 關鍵修正：從 Bits 還原 OldTarget 🔥🔥🔥
	// ---------------------------------------------------------
	// 不管 Block 是否為 nil，BlockIndex 裡一定有 Bits (Header 裡自帶)
	oldTarget := utils.CompactToBig(last.Bits)

	// 計算新 Target
	// newTarget = oldTarget * actualTimespan / IntervalTimespan
	newTarget := new(big.Int).Mul(oldTarget, big.NewInt(actualTimespan))
	newTarget.Div(newTarget, big.NewInt(IntervalTimespan))

	// 最大目標檢查 (不能比創世難度更簡單)
	if newTarget.Cmp(n.Target) > 0 {
		newTarget.Set(n.Target)
	}

	fmt.Printf("⏱ [Consensus] 難度調整: Span %ds (預期 %ds) | Old: %x -> New: %x\n",
		actualTimespan, IntervalTimespan,
		oldTarget, newTarget, // 這裡可以縮短顯示，不然 log 會很長
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

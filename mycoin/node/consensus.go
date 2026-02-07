package node

import (
	"fmt"
	"math/big"
	"mycoin/utils"
)

const (
	DifficultyInterval = 10
	TargetSpacing      = 1 // seconds
	IntervalTimespan   = DifficultyInterval * TargetSpacing
)

func (n *Node) retargetDifficulty(last *BlockIndex) *big.Int {

	// 找到旧周期的第一个区块
	firstHeight := last.Height - DifficultyInterval + 1
	first := last
	for first.Height > firstHeight {
		first = first.Parent
	}

	// ⭐ 从 Block 拿 timestamp（最重要的改动）
	actualTimespan := last.Block.Timestamp - first.Block.Timestamp

	// 限制上下限（/4 ～ ×4）
	minTimespan := int64(IntervalTimespan / 4)
	maxTimespan := int64(IntervalTimespan * 4)

	if actualTimespan < minTimespan {
		actualTimespan = minTimespan
	}
	if actualTimespan > maxTimespan {
		actualTimespan = maxTimespan
	}

	// ⭐ oldTarget 用的是 Block 里的 target
	oldTarget := last.Block.Target

	// newTarget = oldTarget * actualTimespan / IntervalTimespan
	newTarget := new(big.Int).Mul(oldTarget, big.NewInt(actualTimespan))
	newTarget.Div(newTarget, big.NewInt(IntervalTimespan))

	// 最大目标（最小难度）
	maxTarget := new(big.Int)
	maxTarget.SetString(
		"0000000fffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		16,
	)

	if newTarget.Cmp(maxTarget) > 0 {
		newTarget = maxTarget
	}

	fmt.Println("⏱ Difficulty retarget:",
		"\n actualTimespan =", actualTimespan,
		"\n old =", utils.FormatTargetHex(oldTarget),
		"\n new =", utils.FormatTargetHex(newTarget),
	)

	return newTarget
}

func (n *Node) GetCurrentTarget() *big.Int {
	last := n.Best

	// 创世块
	if last.Height == 0 {
		return new(big.Int).Set(n.Target)
	}

	// 不到周期 → 返回当前区块难度
	if (last.Height+1)%DifficultyInterval != 0 {
		return new(big.Int).Set(last.Block.Target)
	}

	// 到周期 → 调 difficulty retarget
	return n.retargetDifficulty(last)
}

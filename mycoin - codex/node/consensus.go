package node

import (
	"fmt"
	"math/big"
	"mycoin/blockchain"
	"mycoin/utils"
	"sort"
	"time"
)

const (
	TargetSpacing          = int64(30) // seconds
	maxMedianTimeBlocks    = 11
	allowedFutureTimeDrift = 2 * time.Minute
)

func (n *Node) retargetDifficulty(last *BlockIndex) *big.Int {
	firstHeight := uint64(0)
	if last.Height+1 > uint64(blockchain.DifficultyInterval) {
		firstHeight = last.Height - uint64(blockchain.DifficultyInterval-1)
	}

	first := last
	for first.Parent != nil && first.Height > firstHeight {
		first = first.Parent
	}

	actualTimespan := last.Timestamp - first.Timestamp
	expectedTimespan := retargetTimespan()

	minTimespan := expectedTimespan / 4
	maxTimespan := expectedTimespan * 4
	if actualTimespan < minTimespan {
		actualTimespan = minTimespan
	}
	if actualTimespan > maxTimespan {
		actualTimespan = maxTimespan
	}

	oldTarget := utils.CompactToBig(last.Bits)
	newTarget := new(big.Int).Mul(oldTarget, big.NewInt(actualTimespan))
	newTarget.Div(newTarget, big.NewInt(expectedTimespan))

	if newTarget.Cmp(n.Target) > 0 {
		newTarget.Set(n.Target)
	}

	fmt.Printf("⏱ [Consensus] 難度調整: Span %ds (預期 %ds) | Old: %x -> New: %x\n",
		actualTimespan, expectedTimespan, oldTarget, newTarget,
	)

	return newTarget
}

func (n *Node) GetCurrentTarget() *big.Int {
	if n.Best == nil {
		return new(big.Int).Set(n.Target)
	}

	last := n.Best
	if last.Height == 0 {
		return new(big.Int).Set(n.Target)
	}

	if (last.Height+1)%blockchain.DifficultyInterval != 0 {
		if last.Block != nil {
			return new(big.Int).Set(last.Block.Target)
		}
		return utils.CompactToBig(last.Bits)
	}

	return n.retargetDifficulty(last)
}

func retargetTimespan() int64 {
	intervals := blockchain.DifficultyInterval - 1
	if intervals < 1 {
		return TargetSpacing
	}
	return int64(intervals) * TargetSpacing
}

func (n *Node) medianTimePast(last *BlockIndex) int64 {
	if last == nil {
		return 0
	}

	timestamps := make([]int64, 0, maxMedianTimeBlocks)
	for cur := last; cur != nil && len(timestamps) < maxMedianTimeBlocks; cur = cur.Parent {
		timestamps = append(timestamps, cur.Timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	return timestamps[len(timestamps)/2]
}

func (n *Node) validateBlockTimestamp(block *blockchain.Block, parent *BlockIndex) error {
	if block == nil || parent == nil {
		return nil
	}

	median := n.medianTimePast(parent)
	if median > 0 && block.Timestamp <= median {
		return fmt.Errorf("block timestamp %d must be greater than median time past %d", block.Timestamp, median)
	}

	maxTimestamp := time.Now().Add(allowedFutureTimeDrift).Unix()
	if block.Timestamp > maxTimestamp {
		return fmt.Errorf("block timestamp %d too far in the future (max %d)", block.Timestamp, maxTimestamp)
	}

	return nil
}

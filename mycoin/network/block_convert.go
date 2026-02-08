package network

import (
	"encoding/hex"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"
	"mycoin/utils"
)

// Block → BlockDTO（发到网络）
func BlockToDTO(b *blockchain.Block, bi *node.BlockIndex) BlockDTO {
	return BlockDTO{
		Height:    b.Height,
		PrevHash:  hex.EncodeToString(b.PrevHash),
		Hash:      hex.EncodeToString(b.Hash),
		Timestamp: b.Timestamp,
		Nonce:     b.Nonce,

		// 🔥🔥🔥 關鍵修正：必須傳輸 Bits！ 🔥🔥🔥
		Bits: b.Bits,

		Target:       b.Target.Text(16),
		CumWork:      bi.CumWorkInt.Text(16),
		Transactions: TxListToDTO(b.Transactions),
		Miner:        b.Miner,
		Reward:       b.Reward,
	}
}

// BlockDTO → Block（从网络接收）
func DTOToBlock(d BlockDTO) *blockchain.Block {
	// 1. 還原 Target (從 Hex 字串) - 這是給人類看的
	target := new(big.Int)
	target.SetString(d.Target, 16)

	// 2. 解碼 Hash
	prevHashBytes, _ := hex.DecodeString(d.PrevHash)
	hashBytes, _ := hex.DecodeString(d.Hash)

	// 3. 🔥🔥 關鍵修正：從 Bits 還原 Target (共識規則) 🔥🔥
	// 我們更信任 Bits，因為它是參與 Hash 計算的源頭
	// 如果 d.Bits 是 0 (舊版節點)，則退化使用上面的 target
	if d.Bits != 0 {
		target = utils.CompactToBig(d.Bits)
	}

	return &blockchain.Block{
		Height:    d.Height,
		PrevHash:  prevHashBytes,
		Timestamp: d.Timestamp,
		Nonce:     d.Nonce,

		// 🔥🔥🔥 關鍵修正：必須填入 Bits！ 🔥🔥🔥
		Bits: d.Bits,

		// 使用還原後的 Target
		Target: target,

		Transactions: TxListFromDTO(d.Transactions),
		Miner:        d.Miner,
		Reward:       d.Reward,
		Hash:         hashBytes,
	}
}

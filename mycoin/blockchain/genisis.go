package blockchain

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"mycoin/utils"
)

func NewGenesisBlock(target *big.Int) *Block {
	genesisTx := NewCoinbase("GENESIS", 1000000)

	// binary prev hash (all zero)
	prev := make([]byte, 32)

	merkle := ComputeMerkleRoot([]Transaction{*genesisTx})

	// 確保 target 不為 nil
	if target == nil {
		// 預設難度 (如果沒傳入的話)
		target = big.NewInt(1)
		target.Lsh(target, 256-24) // 範例
	}

	block := &Block{
		Height:       0,
		PrevHash:     prev,
		Timestamp:    1700000000,
		Nonce:        0,
		Transactions: []Transaction{*genesisTx},
		MerkleRoot:   merkle,
		Target:       new(big.Int).Set(target), // 複製一份，避免外部修改影響
		TargetHex:    target.Text(16),
		Miner:        "GENESIS",
		Reward:       1000000,
		Bits:         utils.BigToCompact(target),
	}

	block.Hash = block.CalcHash()

	fmt.Println("GENESIS HASH =", hex.EncodeToString(block.Hash))

	return block
}

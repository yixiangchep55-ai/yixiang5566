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
	prev := make([]byte, 32) // 全 0 的 prevhash（和 Bitcoin 一样）

	merkle := ComputeMerkleRoot([]Transaction{*genesisTx})

	block := &Block{
		Height:       0,
		PrevHash:     prev, // ✔ binary
		Timestamp:    1700000000,
		Nonce:        0,
		Transactions: []Transaction{*genesisTx},
		MerkleRoot:   merkle,                   // binary
		Target:       new(big.Int).Set(target), // 共识难度
		TargetHex:    target.Text(16),          // 展示用 hex
		Miner:        "GENESIS",
		Reward:       1000000,
		Bits:         utils.BigToCompact(target),
	}

	block.Hash = block.CalcHash() // ✔ binary hash

	fmt.Println("GENESIS HASH =", hex.EncodeToString(block.Hash))

	return block
}

package blockchain

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"mycoin/utils"
)

func NewGenesisBlock(target *big.Int) *Block {
	// 🚀 關鍵修復：加上第三個參數！這是一段固定的創世名言
	// （你可以改成任何你喜歡的句子，但主機和 VM 執行的程式碼裡這句必須完全一樣！）
	genesisTx := NewCoinbase(
		"GENESIS",
		1000000,
		"The Times 03/Jan/2009 Chancellor on brink of second bailout for banks", // 👈 就是這個固定字串！
	)
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

package network

import (
	"encoding/hex"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"
)

// Block → BlockDTO（发到网络）
func BlockToDTO(b *blockchain.Block, bi *node.BlockIndex) BlockDTO {
	return BlockDTO{
		Height:       b.Height,                       // uint64
		PrevHash:     hex.EncodeToString(b.PrevHash), // []byte → hex
		Hash:         hex.EncodeToString(b.Hash),     // []byte → hex
		Timestamp:    b.Timestamp,                    // int64
		Nonce:        b.Nonce,                        // uint64
		Target:       b.Target.Text(16),              // difficulty
		CumWork:      bi.CumWorkInt.Text(16),         // big int → hex
		Transactions: TxListToDTO(b.Transactions),    // ok
		Miner:        b.Miner,
		Reward:       b.Reward,
	}
}

// BlockDTO → Block（从网络接收）
func DTOToBlock(d BlockDTO) *blockchain.Block {
	target := new(big.Int)
	target.SetString(d.Target, 16)

	// 🔥 HEX → BYTES
	prevHashBytes, _ := hex.DecodeString(d.PrevHash)
	hashBytes, _ := hex.DecodeString(d.Hash)

	return &blockchain.Block{
		Height:       d.Height,
		PrevHash:     prevHashBytes, // []byte
		Timestamp:    d.Timestamp,
		Nonce:        d.Nonce,
		Target:       target,
		Transactions: TxListFromDTO(d.Transactions),
		Miner:        d.Miner,
		Reward:       d.Reward,
		Hash:         hashBytes, // []byte
	}
}

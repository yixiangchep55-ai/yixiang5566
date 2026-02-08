package network

import (
	"encoding/hex"
	"math/big"
	"mycoin/blockchain"
	"mycoin/node"
)

type HeaderDTO struct {
	Hash       string `json:"hash"`
	PrevHash   string `json:"prev_hash"`
	Height     uint64 `json:"height"`
	Target     string `json:"target"`   // hex
	CumWork    string `json:"cum_work"` // hex
	Timestamp  int64  `json:"timestamp"`
	Nonce      uint64 `json:"nonce"`
	MerkleRoot string `json:"merkle_root"`
}

type HeadersPayload struct {
	Headers []HeaderDTO `json:"headers"`
}

func HeaderDTOToBlock(h HeaderDTO) *blockchain.Block {
	target := new(big.Int)
	target.SetString(h.Target, 16)

	// 🔥 必须 hex → bytes
	prevHashBytes, _ := hex.DecodeString(h.PrevHash)
	hashBytes, _ := hex.DecodeString(h.Hash)

	return &blockchain.Block{
		Height:    h.Height,
		PrevHash:  prevHashBytes, // []byte
		Timestamp: h.Timestamp,
		Nonce:     h.Nonce,
		Target:    target,
		Hash:      hashBytes, // []byte
	}
}

func BlockIndexToHeaderDTO(bi *node.BlockIndex) HeaderDTO {
	dto := HeaderDTO{
		Hash:     bi.Hash,
		PrevHash: bi.PrevHash,
		Height:   bi.Height,
		CumWork:  bi.CumWork,
	}

	if bi.Block != nil { // body downloaded
		dto.Target = bi.Block.Target.Text(16)
		dto.Timestamp = bi.Block.Timestamp
		dto.Nonce = bi.Block.Nonce
		dto.MerkleRoot = hex.EncodeToString(bi.Block.MerkleRoot)
	}

	return dto
}

package node

import (
	"math/big"
	"mycoin/blockchain"
)

type BlockIndex struct {
	Hash     string `json:"hash"`
	Height   uint64 `json:"height"`
	CumWork  string `json:"cumwork"`
	PrevHash string `json:"prevhash"`

	Timestamp int64  `json:"timestamp"`
	Bits      uint32 `json:"bits"`

	CumWorkInt *big.Int `json:"-"`
	// 重启后重新填充
	Block    *blockchain.Block `json:"-"`
	Parent   *BlockIndex       `json:"-"`
	Children []*BlockIndex     `json:"-"`
}

func WorkFromTarget(target *big.Int) *big.Int {
	if target == nil {
		return big.NewInt(0)
	}
	maxTarget := new(big.Int).Lsh(big.NewInt(1), 256)
	t := new(big.Int).Add(target, big.NewInt(1))
	work := new(big.Int).Div(maxTarget, t)
	return work
}

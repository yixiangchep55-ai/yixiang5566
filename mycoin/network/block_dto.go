package network

type BlockDTO struct {
	Height    uint64 `json:"height"`
	PrevHash  string `json:"prev_hash"`
	Timestamp int64  `json:"timestamp"`
	Nonce     uint64 `json:"nonce"`
	Bits      uint32 `json:"bits"` // 之前加的

	// 🔥🔥🔥 這次加這個！ 🔥🔥🔥
	MerkleRoot string `json:"merkle_root"`

	Target       string           `json:"target"`
	CumWork      string           `json:"cum_work"`
	Transactions []TransactionDTO `json:"txs"`
	Miner        string           `json:"miner"`
	Reward       int              `json:"reward"`
	Hash         string           `json:"hash"`
}

package network

// 只用于网络传输（JSON）
type BlockDTO struct {
	Height    uint64 `json:"height"`
	PrevHash  string `json:"prev_hash"`
	Timestamp int64  `json:"timestamp"`
	Nonce     uint64 `json:"nonce"`

	// 🔥🔥🔥 必須補上這個！ 🔥🔥🔥
	Bits uint32 `json:"bits"`

	Target  string `json:"target"`   // hex string
	CumWork string `json:"cum_work"` // hex string

	Transactions []TransactionDTO `json:"txs"`

	Miner  string `json:"miner"`
	Reward int    `json:"reward"`
	Hash   string `json:"hash"`
}

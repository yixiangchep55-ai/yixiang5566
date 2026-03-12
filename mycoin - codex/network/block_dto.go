package network

type BlockDTO struct {
	Height       uint64           `json:"height" mapstructure:"height"`
	PrevHash     string           `json:"prev_hash" mapstructure:"prev_hash"`
	Timestamp    int64            `json:"timestamp" mapstructure:"timestamp"`
	Nonce        uint64           `json:"nonce" mapstructure:"nonce"`
	Bits         uint32           `json:"bits" mapstructure:"bits"`
	MerkleRoot   string           `json:"merkle_root" mapstructure:"merkle_root"`
	Target       string           `json:"target" mapstructure:"target"`
	CumWork      string           `json:"cum_work" mapstructure:"cum_work"`
	Transactions []TransactionDTO `json:"txs" mapstructure:"txs"` // 👈 確保 TransactionDTO 也有標籤！
	Miner        string           `json:"miner" mapstructure:"miner"`
	Reward       int              `json:"reward" mapstructure:"reward"`
	Hash         string           `json:"hash" mapstructure:"hash"`
}

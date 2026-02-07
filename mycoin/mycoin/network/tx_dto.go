package network

// 网络层交易 DTO（不含任何逻辑）

type TransactionDTO struct {
	ID         string     `json:"id"`
	Inputs     []TxInDTO  `json:"inputs"`
	Outputs    []TxOutDTO `json:"outputs"`
	IsCoinbase bool       `json:"is_coinbase"`
}

type TxInDTO struct {
	TxID   string `json:"txid"`
	Index  int    `json:"index"`
	Sig    string `json:"sig"`    // hex string
	PubKey string `json:"pubkey"` // hex string
}

type TxOutDTO struct {
	Value string `json:"value"` // 数值 → string
	To    string `json:"to"`    // 收款公钥 hex
}

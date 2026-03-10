package network

// 网络层交易 DTO（不含任何逻辑）

type TransactionDTO struct {
	ID         string     `json:"id" mapstructure:"id"`
	Inputs     []TxInDTO  `json:"inputs" mapstructure:"inputs"`   // 👈 嵌套結構，標籤必備！
	Outputs    []TxOutDTO `json:"outputs" mapstructure:"outputs"` // 👈 同上
	IsCoinbase bool       `json:"is_coinbase" mapstructure:"is_coinbase"`
}

type TxInDTO struct {
	TxID   string `json:"txid" mapstructure:"txid"`
	Index  int    `json:"index" mapstructure:"index"`
	Sig    string `json:"sig" mapstructure:"sig"`       // hex string
	PubKey string `json:"pubkey" mapstructure:"pubkey"` // hex string
}

type TxOutDTO struct {
	Value string `json:"value" mapstructure:"value"` // 🌟 探長提醒：用 string 傳輸大數是正確的！
	To    string `json:"to" mapstructure:"to"`       // 收款公鑰 hex
}

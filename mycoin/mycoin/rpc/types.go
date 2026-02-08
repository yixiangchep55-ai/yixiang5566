package rpc

type RPCBlock struct {
	Hash         string  `json:"hash"`
	PrevHash     string  `json:"prev_hash"`
	Height       uint64  `json:"height"`
	Timestamp    int64   `json:"timestamp"`
	Nonce        uint64  `json:"nonce"`
	Target       string  `json:"target"`
	CumWork      string  `json:"cum_work"`
	Transactions []RPCTx `json:"tx"`
}

type RPCTx struct {
	TxID    string        `json:"txid"`
	Inputs  []RPCTxInput  `json:"vin"`
	Outputs []RPCTxOutput `json:"vout"`
}

type RPCTxInput struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	From  string `json:"from"`
}

type RPCTxOutput struct {
	Amount int    `json:"amount"`
	To     string `json:"to"`
}

type RPCUTXO struct {
	TxID   string `json:"txid"`
	Index  int    `json:"index"`
	Amount int    `json:"amount"`
	To     string `json:"to"`
}

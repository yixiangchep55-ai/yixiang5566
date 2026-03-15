package rpc

import (
	"mycoin/network"
	"mycoin/node"
	"mycoin/wallet"
)

type RPCBlock struct {
	Hash         string  `json:"hash"`
	PrevHash     string  `json:"prevhash"`
	Height       uint64  `json:"height"`
	Timestamp    int64   `json:"timestamp"`
	Nonce        uint64  `json:"nonce"`
	Miner        string  `json:"miner"`
	Target       string  `json:"target"`
	CumWork      string  `json:"cumwork"`
	Transactions []RPCTx `json:"transactions"`
	Reward       float64 `json:"reward"`
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
	Amount float64 `json:"amount"` //  ?float64
	To     string  `json:"to"`
}

type RPCUTXO struct {
	TxID   string `json:"txid"`
	Index  int    `json:"index"`
	Amount int    `json:"amount"`
	To     string `json:"to"`
}

// JSON-RPC
type RPCRequest struct {
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
	ID     interface{}   `json:"id"`
}

type RPCResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  interface{} `json:"error,omitempty"`
	ID     interface{} `json:"id,omitempty"`
}

// RPC ?
type RPCServer struct {
	Node    *node.Node
	Handler *network.Handler
	Wallet  *wallet.Wallet
}

type TxOutputJSON struct {
	To     string  `json:"to"`
	Amount float64 `json:"amount"` //  ?
}

type TxInputJSON struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	// ?Signature/PubKey
}

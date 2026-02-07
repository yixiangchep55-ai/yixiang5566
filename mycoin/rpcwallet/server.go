package rpcwallet

import (
	"encoding/json"
	"log"
	"mycoin/network"
	"mycoin/node"
	"mycoin/wallet"
	"net/http"
)

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

// Wallet RPC Server
type RPCServer struct {
	Node    *node.Node
	Wallet  *wallet.Wallet
	Handler *network.Handler
}

type RPCUTXO struct {
	TxID   string `json:"txid"`
	Index  int    `json:"index"`
	Amount int    `json:"amount"`
	To     string `json:"to"`
}

func (s *RPCServer) Start(addr string) {
	http.HandleFunc("/wallet", s.handleRPC)
	log.Println("🟩 Wallet RPC listening at", addr)
	go http.ListenAndServe(addr, nil)
}

func (s *RPCServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req RPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, req.ID, "invalid json")
		return
	}

	switch req.Method {

	case "getbalance":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "address required")
			return
		}

		addr, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid address")
			return
		}

		// 1️⃣ 通过地址索引找到该地址的所有 utxo key
		keys := s.Node.UTXO.AddrIndex[addr]
		if keys == nil {
			s.writeResult(w, req.ID, 0)
			return
		}

		// 2️⃣ 累加金额
		total := 0
		for _, key := range keys {
			utxo := s.Node.UTXO.Set[key]
			total += utxo.Amount
		}

		s.writeResult(w, req.ID, total)

	case "listutxos":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "address required")
			return
		}

		addr, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid address")
			return
		}

		keys := s.Node.UTXO.AddrIndex[addr]
		if keys == nil {
			s.writeResult(w, req.ID, []RPCUTXO{})
			return
		}

		// 1️⃣ 将 UTXO 填入列表
		var list []RPCUTXO

		for _, key := range keys {
			utxo := s.Node.UTXO.Set[key]

			list = append(list, RPCUTXO{
				TxID:   utxo.TxID,
				Index:  utxo.Index,
				Amount: utxo.Amount,
				To:     utxo.To,
			})
		}

		s.writeResult(w, req.ID, list)

	case "sendtoaddress":

		if len(req.Params) != 2 {
			s.writeError(w, req.ID, "usage: sendtoaddress <to> <amount>")
			return
		}

		toAddr, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid to address")
			return
		}

		amountFloat, ok := req.Params[1].(float64)
		if !ok {
			s.writeError(w, req.ID, "invalid amount")
			return
		}
		amount := int(amountFloat)

		// 1️⃣ 构造未签名交易
		tx, err := wallet.BuildTransaction(
			s.Wallet.Address, // from
			toAddr,
			amount,
			s.Node.UTXO,
		)
		if err != nil {
			s.writeError(w, req.ID, err.Error())
			return
		}

		// 2️⃣ 签名交易
		if err := wallet.SignTransaction(tx, s.Wallet); err != nil {
			s.writeError(w, req.ID, "sign tx failed: "+err.Error())
			return
		}

		// 3️⃣ 节点验证（必须是 value）
		if err := s.Node.VerifyTx(*tx); err != nil {
			s.writeError(w, req.ID, "tx rejected: "+err.Error())
			return
		}

		// 4️⃣ 加入 mempool（必须是 AddTx）
		txBytes := tx.Serialize()

		ok = s.Node.Mempool.AddTxRBF(
			tx.ID,
			txBytes,
			s.Node.UTXO,
		)

		if !ok {
			s.writeError(w, req.ID, "mempool rejected tx (RBF / conflict / low fee)")
			return
		}
		// 5️⃣ 广播交易（Node 不负责广播，Handler 才负责）
		if s.Handler != nil {
			s.Handler.BroadcastLocalTx(*tx)
		}

		// 6️⃣ 返回 txid
		s.writeResult(w, req.ID, tx.ID)

	default:
		s.writeError(w, req.ID, "unknown method")
	}
}

func (s *RPCServer) writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := RPCResponse{Result: result, ID: id}
	out, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func (s *RPCServer) writeError(w http.ResponseWriter, id interface{}, msg string) {
	resp := RPCResponse{Error: msg, ID: id}
	out, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

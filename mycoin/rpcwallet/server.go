package rpcwallet

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"mycoin/blockchain"
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

		if len(req.Params) < 2 {
			s.writeError(w, req.ID, "usage: sendtoaddress <to> <amount> [fee]")
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

		fee := 0
		if len(req.Params) >= 3 {
			feeFloat, ok := req.Params[2].(float64)
			if ok {
				fee = int(feeFloat)
			}
		}

		s.Node.Lock()

		// 1️⃣ 构造未签名交易
		tx, err := wallet.BuildTransaction(
			s.Wallet.Address, // from
			toAddr,
			amount,
			fee,
			s.Node.UTXO,
		)

		s.Node.Unlock() // 🔓 呼叫公開的 Unlock()

		if err != nil {
			s.writeError(w, req.ID, err.Error())
			return
		}

		// 2️⃣ 签名交易
		if err := wallet.SignTransaction(tx, s.Wallet); err != nil {
			s.writeError(w, req.ID, "sign tx failed: "+err.Error())
			return
		}

		// 3️⃣ 節點驗證並加入 mempool (呼叫我們寫好的門口保全！)
		// 注意這裡傳入的是 *tx (解指標取值)
		if ok := s.Node.AddTx(*tx); !ok {
			s.writeError(w, req.ID, "tx rejected: validation or mempool error")
			return
		}

		// 4️⃣ 广播交易（Node 不负责广播，Handler 才负责）
		if s.Handler != nil {
			s.Handler.BroadcastLocalTx(*tx)
		}

		// 5️⃣ 返回 txid
		s.writeResult(w, req.ID, tx.ID)

	case "sendcpfpchild":
		// 🕵️ 大偵探專屬外掛：手動指定要花費的未確認 UTXO！
		// 參數: <to> <amount> <fee> <parentTxID> <parentIndex>
		if len(req.Params) != 5 {
			s.writeError(w, req.ID, "usage: sendcpfpchild <to> <amount> <fee> <parentTxID> <parentIndex>")
			return
		}

		toAddr := req.Params[0].(string)
		amount := int(req.Params[1].(float64))
		_ = int(req.Params[2].(float64))
		parentTxID := req.Params[3].(string)
		parentIndex := int(req.Params[4].(float64))

		// 1️⃣ 手動捏造 Input (使用 blockchain 套件)
		// 💡 注意：如果你的結構叫 TxInput，請把 TX 改成 Tx
		in := blockchain.TxInput{
			TxID:  parentTxID,
			Index: parentIndex,
			Sig:   "", // 準備讓錢包簽名
			// 取得錢包的公鑰並轉成字串 (依照你錢包的寫法微調)
			PubKey: hex.EncodeToString(s.Wallet.PublicKey),
		}

		// 2️⃣ 手動捏造 Output
		out := blockchain.TxOutput{
			Amount: amount,
			To:     toAddr,
		}

		// 3️⃣ 組裝未簽名交易
		tx := &blockchain.Transaction{
			ID:         "",
			Inputs:     []blockchain.TxInput{in},
			Outputs:    []blockchain.TxOutput{out},
			IsCoinbase: false,
		}

		// 產生 TxID (假設你有 Hash 方法，如果沒有，可以用 tx.SetID() 類似的方法)
		// 如果這行報錯，請換成 tx.ID = hex.EncodeToString(tx.Hash()) 視你的代碼而定
		tx.ID = tx.Hash()

		// 4️⃣ 簽名交易
		if err := wallet.SignTransaction(tx, s.Wallet); err != nil {
			s.writeError(w, req.ID, "sign tx failed: "+err.Error())
			return
		}

		// 5️⃣ 塞入 Mempool
		if ok := s.Node.AddTx(*tx); !ok {
			s.writeError(w, req.ID, "tx rejected: validation or mempool error")
			return
		}

		// 6️⃣ 廣播給全網
		if s.Handler != nil {
			s.Handler.BroadcastLocalTx(*tx)
		}

		s.writeResult(w, req.ID, "CPFP 富兒子發送成功！TxID: "+tx.ID)

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

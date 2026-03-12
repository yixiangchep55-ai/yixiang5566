package rpcwallet

import (
	"encoding/json"
	"fmt"
	"log"
	"mycoin/blockchain"
	"mycoin/network"
	"mycoin/node"
	"mycoin/wallet"
	"net/http"
)

type TxSummary struct {
	TxID       string  `json:"txid"`
	Sender     string  `json:"sender"`
	Receiver   string  `json:"receiver"`
	AmountSent float64 `json:"amount_sent"`
	NetworkFee float64 `json:"network_fee"`
	Change     float64 `json:"change"`
	IsCoinbase bool    `json:"is_coinbase"`
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

	case "estimatefee":
		// 🕵️ 大偵探的手續費預測雷達
		baseFee := 1
		mempoolSize := 0

		// 這裡的 s.Node 是實體 struct，可以直接讀取 Mempool
		if s.Node != nil && s.Node.Mempool != nil {
			mempoolSize = len(s.Node.Mempool.Txs)
		}

		// 套用跟礦工一模一樣的「擁堵漲價公式」
		congestionPremium := (mempoolSize / 5) * 2
		recommendedFee := baseFee + congestionPremium

		// 回報給前台
		s.writeResult(w, req.ID, float64(recommendedFee)/100.0)

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

		s.writeResult(w, req.ID, float64(total)/100.0)

	case "getwallettransaction":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "usage: gettransaction <txid>")
			return
		}

		txID, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid txid")
			return
		}

		// 1. 找這筆交易 (先在 Mempool 找，找不到再去 Chain 找)
		var targetTx *blockchain.Transaction
		s.Node.Lock()

		// 搜 Mempool
		for _, txBytes := range s.Node.Mempool.Txs {
			var mTx blockchain.Transaction
			if err := json.Unmarshal(txBytes, &mTx); err == nil && mTx.ID == txID {
				targetTx = &mTx
				break
			}
		}

		// 搜 Chain
		if targetTx == nil {
			for _, block := range s.Node.Chain {
				for _, bTx := range block.Transactions {
					if bTx.ID == txID {
						targetTx = &bTx
						break
					}
				}
			}
		}
		s.Node.Unlock()

		if targetTx == nil {
			s.writeError(w, req.ID, "transaction not found")
			return
		}

		// 2. 🕵️ 呼叫翻譯官！
		summary := s.ParseToHumanSummary(*targetTx)

		// 3. 回傳漂亮的收據
		s.writeResult(w, req.ID, summary)

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
		amount := int(amountFloat * 100)

		fee := 0
		if len(req.Params) >= 3 {
			feeFloat, ok := req.Params[2].(float64)
			if ok {
				fee = int(feeFloat * 100)
			}
		}

		s.Node.Lock()

		var currentMempoolTxs []blockchain.Transaction
		for _, txBytes := range s.Node.Mempool.Txs {
			var tx blockchain.Transaction
			// 這裡假設你有一個 Deserialize 方法，或者直接用 json.Unmarshal
			// 如果你的 Transaction 支援 Gob 或 Json，請根據你的專案微調：
			if err := json.Unmarshal(txBytes, &tx); err == nil {
				currentMempoolTxs = append(currentMempoolTxs, tx)
			}
		}

		// 1️⃣ 构造未签名交易
		tx, err := wallet.BuildTransaction(
			s.Wallet.Address, // from
			toAddr,
			amount,
			fee,
			s.Node.UTXO,
			currentMempoolTxs,
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
		if ok := s.Node.AddTx(*tx, s.Node.NodeID); !ok {
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

		feeFloat, ok := req.Params[2].(float64)
		if !ok {
			s.writeError(w, req.ID, "invalid fee")
			return
		}

		parentTxID, ok := req.Params[3].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid parent txid")
			return
		}

		parentIndexFloat, ok := req.Params[4].(float64)
		if !ok {
			s.writeError(w, req.ID, "invalid parent index")
			return
		}

		parentIndex := int(parentIndexFloat)
		amount := int(amountFloat * 100)
		fee := int(feeFloat * 100)

		if amount <= 0 {
			s.writeError(w, req.ID, "amount must be positive")
			return
		}
		if fee < 0 {
			s.writeError(w, req.ID, "fee must be non-negative")
			return
		}

		{
			parentOut, err := s.lookupOutput(parentTxID, parentIndex)
			if err != nil {
				s.writeError(w, req.ID, err.Error())
				return
			}
			if amount+fee > parentOut.Amount {
				s.writeError(w, req.ID, "amount plus fee exceeds parent output")
				return
			}
			if parentOut.To != s.Wallet.Address {
				s.writeError(w, req.ID, "parent output does not belong to wallet")
				return
			}

			inputs := []blockchain.TxInput{{
				TxID:  parentTxID,
				Index: parentIndex,
			}}

			outputs := []blockchain.TxOutput{{
				Amount: amount,
				To:     toAddr,
			}}

			if change := parentOut.Amount - amount - fee; change > 0 {
				outputs = append(outputs, blockchain.TxOutput{
					Amount: change,
					To:     s.Wallet.Address,
				})
			}

			newTx := blockchain.NewTransaction(inputs, outputs)

			if err := wallet.SignTransaction(newTx, s.Wallet); err != nil {
				s.writeError(w, req.ID, "sign tx failed: "+err.Error())
				return
			}

			if ok := s.Node.AddTx(*newTx, s.Node.NodeID); !ok {
				s.writeError(w, req.ID, "tx rejected: validation or mempool error")
				return
			}

			if s.Handler != nil {
				s.Handler.BroadcastLocalTx(*newTx)
			}

			s.writeResult(w, req.ID, "CPFP child sent successfully. TxID: "+newTx.ID)
			return
		}

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

// ParseToHumanSummary RPC專屬的超級翻譯官！
func (s *RPCServer) lookupOutput(txID string, index int) (*blockchain.TxOutput, error) {
	if index < 0 {
		return nil, fmt.Errorf("invalid parent index")
	}

	s.Node.Lock()
	defer s.Node.Unlock()

	if txBytes, exists := s.Node.Mempool.Txs[txID]; exists {
		tx, err := blockchain.DeserializeTransaction(txBytes)
		if err == nil && index < len(tx.Outputs) {
			out := tx.Outputs[index]
			return &blockchain.TxOutput{
				Amount: out.Amount,
				To:     out.To,
			}, nil
		}
	}

	if out, ok := s.Node.UTXO.Get(txID, index); ok {
		cloned := *out
		return &cloned, nil
	}

	for _, block := range s.Node.Chain {
		for _, tx := range block.Transactions {
			if tx.ID != txID {
				continue
			}
			if index >= len(tx.Outputs) {
				return nil, fmt.Errorf("parent output index out of range")
			}
			out := tx.Outputs[index]
			return &blockchain.TxOutput{
				Amount: out.Amount,
				To:     out.To,
			}, nil
		}
	}

	return nil, fmt.Errorf("parent output not found")
}

func (s *RPCServer) ParseToHumanSummary(tx blockchain.Transaction) TxSummary {
	summary := TxSummary{
		TxID:       tx.ID,
		IsCoinbase: tx.IsCoinbase,
	}

	// 1️⃣ 系統發錢 (Coinbase) 模式
	if tx.IsCoinbase {
		summary.Sender = "System Reward (Coinbase)"
		if len(tx.Outputs) > 0 {
			summary.Receiver = tx.Outputs[0].To
			summary.AmountSent = float64(tx.Outputs[0].Amount) / 100.0
		}
		return summary
	}

	// 2️⃣ 一般交易算帳模式
	var totalInputAmount int = 0
	var totalOutputAmount int = 0
	var actualSent int = 0
	var changeAmount int = 0

	// 【抓出輸入金額】
	// ⚠️ 探長提醒：這裡我們需要去區塊鏈找上一筆交易。
	// 這裡示範暴力掃描 Chain (如果你的系統有更快找 Tx 的方法，可以替換)
	for _, in := range tx.Inputs {
		// 去找出 in.TxID 這筆交易的原本金額
		var prevTx *blockchain.Transaction

		// 簡單的尋找邏輯 (建議未來可以在 DB 加索引)
		s.Node.Lock()
		for _, block := range s.Node.Chain {
			for _, bTx := range block.Transactions {
				if bTx.ID == in.TxID {
					prevTx = &bTx
					break
				}
			}
		}
		s.Node.Unlock()

		if prevTx != nil && in.Index < len(prevTx.Outputs) {
			prevOut := prevTx.Outputs[in.Index]
			totalInputAmount += prevOut.Amount
			summary.Sender = prevOut.To // 把錢的來源當作發送者
		}
	}

	// 【抓出輸出金額與找零】
	for _, out := range tx.Outputs {
		totalOutputAmount += out.Amount

		// 🕵️ 探長斷案：這筆錢是不是回到我的錢包？
		if out.To == s.Wallet.Address {
			changeAmount += out.Amount // 是我的，這是找零！
		} else {
			actualSent += out.Amount  // 不是我的，這是真實轉出！
			summary.Receiver = out.To // 紀錄收款人
		}
	}

	// 3️⃣ 結算數字並轉換為小數點格式 (除以 100.0)
	summary.AmountSent = float64(actualSent) / 100.0
	summary.Change = float64(changeAmount) / 100.0

	fee := totalInputAmount - totalOutputAmount
	if fee > 0 {
		summary.NetworkFee = float64(fee) / 100.0
	} else {
		summary.NetworkFee = 0
	}

	return summary
}

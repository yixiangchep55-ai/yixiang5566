package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"mycoin/blockchain"
	"mycoin/network"
)

// 启动 RPC 服务
func (s *RPCServer) Start(addr string) {
	http.HandleFunc("/rpc", s.handleRPC)

	log.Println("🔌 RPC server listening at", addr)
	go http.ListenAndServe(addr, nil)
}

// 处理所有 RPC 请求
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

	// ================================
	//   这是示例 API：ping
	// ================================
	case "ping":
		s.writeResult(w, req.ID, "pong")

	case "getblockcount":
		if s.Node == nil || s.Node.Best == nil {
			s.writeError(w, req.ID, "node not ready")
			return
		}
		s.writeResult(w, req.ID, s.Node.Best.Height)

	case "getbestblockhash":
		if s.Node == nil || s.Node.Best == nil {
			s.writeError(w, req.ID, "node not ready")
			return
		}
		s.writeResult(w, req.ID, s.Node.Best.Hash)

	case "getblockhash":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "height required")
			return
		}

		height, ok := req.Params[0].(float64) // JSON 数字默认是 float64
		if !ok {
			s.writeError(w, req.ID, "invalid height")
			return
		}

		h := int(height)

		if h < 0 || h >= len(s.Node.Chain) {
			s.writeError(w, req.ID, "height out of range")
			return
		}

		s.writeResult(w, req.ID, s.Node.Chain[h].Hash)

	case "getblock":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "block hash required")
			return
		}

		hash, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid block hash")
			return
		}

		// 1️⃣ 先从 BlockIndex 查
		bi, ok := s.Node.Blocks[hash]
		if !ok || bi.Block == nil {
			s.writeError(w, req.ID, "block not found")
			return
		}

		b := bi.Block

		// 2️⃣ 構造 RPC Block (建議增加 Reward 欄位)
		rpcBlock := RPCBlock{
			Hash:      hex.EncodeToString(b.Hash),
			PrevHash:  hex.EncodeToString(b.PrevHash),
			Height:    b.Height,
			Timestamp: b.Timestamp,
			Nonce:     b.Nonce,
			Target:    b.Target.Text(16),
			CumWork:   bi.CumWorkInt.Text(16),
			Reward:    float64(b.Reward) / 100.0, // 🚀 修正：500 -> 5.00
		}

		// 3️⃣ 填充交易
		for _, tx := range b.Transactions {
			rpcTx := RPCTx{
				TxID: tx.ID, // 確保這裡是 hex 字串
			}

			for _, in := range tx.Inputs {
				fromAddr := ""
				if in.TxID == "" {
					fromAddr = "coinbase"
				} else {
					// 🕵️ 探長提醒：這裡如果 UTXO 沒了，會變 unknown。
					// 暫時維持現狀，但 UI 上要有心裡準備
					key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
					if utxo, ok := s.Node.UTXO.Set[key]; ok {
						fromAddr = utxo.To
					} else {
						fromAddr = "spent / unknown"
					}
				}

				rpcTx.Inputs = append(rpcTx.Inputs, RPCTxInput{
					TxID:  in.TxID,
					Index: in.Index,
					From:  fromAddr,
				})
			}

			for _, out := range tx.Outputs {
				rpcTx.Outputs = append(rpcTx.Outputs, RPCTxOutput{
					// 🚀 關鍵修正：將底層整數轉換為小數顯示
					Amount: float64(out.Amount) / 100.0,
					To:     out.To,
				})
			}

			rpcBlock.Transactions = append(rpcBlock.Transactions, rpcTx)
		}

		s.writeResult(w, req.ID, rpcBlock)

	case "getrawtransaction":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "txid required")
			return
		}

		txid, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid txid")
			return
		}

		// 1️⃣ 查 mempool
		txBytes, ok := s.Node.Mempool.Get(txid)
		if ok {
			s.writeResult(w, req.ID, string(txBytes))
			return
		}

		// 2️⃣ 查区块链
		for _, blk := range s.Node.Chain {
			for _, tx := range blk.Transactions {
				if tx.ID == txid {
					s.writeResult(w, req.ID, string(tx.Serialize()))
					return
				}
			}
		}

		s.writeError(w, req.ID, "tx not found")

	case "sendrawtransaction":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "rawtx required")
			return
		}

		// 1️⃣ 取得 raw tx JSON（DTO 格式）
		rawtx, ok := req.Params[0].(map[string]interface{})
		if !ok {
			s.writeError(w, req.ID, "rawtx must be JSON object")
			return
		}

		// 转 bytes
		rawBytes, _ := json.Marshal(rawtx)

		// 2️⃣ JSON → DTO
		var dto network.TransactionDTO
		if err := json.Unmarshal(rawBytes, &dto); err != nil {
			s.writeError(w, req.ID, "invalid tx format")
			return
		}

		// 3️⃣ DTO → Transaction（你的转换函数）
		txObj := network.DTOToTx(dto)

		// 4️⃣ 驗證並加入 mempool (呼叫門口保全)
		if ok := s.Node.AddTx(txObj); !ok {
			s.writeError(w, req.ID, "tx rejected: validation or mempool error")
			return
		}

		// 5️⃣ 广播
		s.Handler.BroadcastLocalTx(txObj)
		s.writeResult(w, req.ID, txObj.ID)

	case "gettransaction":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "txid required")
			return
		}

		txid, ok := req.Params[0].(string)
		if !ok {
			s.writeError(w, req.ID, "invalid txid")
			return
		}

		// 1️⃣ Node查询 tx + block
		tx, block, err := s.Node.GetTransaction(txid)
		if err != nil {
			s.writeError(w, req.ID, err.Error())
			return
		}

		// 2️⃣ 再查 txindex 获取高度
		idx, err := s.Node.GetTxIndex(txid)
		if err != nil {
			s.writeError(w, req.ID, "txindex missing")
			return
		}

		if idx.Pruned {
			s.writeError(w, req.ID, "This transaction is in a pruned block. Please query an archive node.")
			return
		}

		var displayOutputs []TxOutputJSON
		for _, out := range tx.Outputs {
			displayOutputs = append(displayOutputs, TxOutputJSON{
				To:     out.To,
				Amount: float64(out.Amount) / 100.0, // 👈 關鍵：150 -> 1.50
			})
		}

		// 轉換 Inputs (可選，主要是為了美觀)
		var displayInputs []TxInputJSON
		for _, in := range tx.Inputs {
			displayInputs = append(displayInputs, TxInputJSON{
				// 🚀 修正點：直接使用 in.TxID，不需要 hex.EncodeToString
				TxID:  in.TxID,
				Index: in.Index,
			})
		}

		// ⭐ 最終回傳給前端的結果
		result := map[string]interface{}{
			"txid":   txid,
			"block":  hex.EncodeToString(block.Hash),
			"height": idx.Height,
			"amount": float64(tx.GetTotalAmount()) / 100.0, // 如果你有這個方法的話
			"details": map[string]interface{}{
				"vin":  displayInputs,
				"vout": displayOutputs,
			},
			"raw_tx": tx, // 如果你還需要原始數據可以留著，但前端顯示應使用上面處理過的
		}

		s.writeResult(w, req.ID, result)

	case "getmempool":
		// 1. 準備一個空陣列，這很重要！讓 Vue 收到 [] 而不是 null
		mempoolList := make([]map[string]interface{}, 0)

		// 2. 呼叫你 mempool.go 裡寫好的 GetAll() 方法
		allTxs := s.Node.Mempool.GetAll()

		// 3. 遍歷拿到的所有交易
		// 3. 遍歷拿到的所有交易
		// 3. 遍歷拿到的所有交易
		for txid, txBytes := range allTxs {
			// 🕵️ 探長防彈衣：先從記憶體拿拿看時間
			enterTime := s.Node.Mempool.Times[txid]

			// ==========================================
			// 🕵️ 探長的「惰性載入 (Lazy Load)」與自我修復魔法
			// ==========================================
			// 如果發現時間是 0 (代表這是剛重啟，記憶體失憶了)，我們就去查資料庫！
			if enterTime == 0 && s.Node.Mempool.DB != nil {
				timeBytes := s.Node.Mempool.DB.Get("mempool_times", txid)
				if len(timeBytes) > 0 {
					// 情況 A：資料庫裡有紀錄！立刻解碼恢復
					parsedTime, err := strconv.ParseInt(string(timeBytes), 10, 64)
					if err == nil {
						enterTime = parsedTime
					} else {
						enterTime = time.Now().Unix() // 防呆機制
					}
				} else {
					// 情況 B：連資料庫都沒有 (這是最古老的那幾筆幽靈交易)
					// 直接給它現在的時間，並且「永久封裝」進資料庫，以後就不會忘了！
					enterTime = time.Now().Unix()
					timeStr := strconv.FormatInt(enterTime, 10)
					s.Node.Mempool.DB.Put("mempool_times", txid, []byte(timeStr))
				}

				// 🧠 記憶體修復：把找回來的時間寫回記憶體！
				// 這樣 3 秒後 Vue 再來問的時候，就不用再查資料庫了，速度飛快！
				s.Node.Mempool.Times[txid] = enterTime
			}
			// ==========================================

			// 解析 bytes 回交易物件
			tx, err := blockchain.DeserializeTransaction(txBytes)

			if err == nil {
				// 成功解析時的處理
				displayAmount := 0.0
				if len(tx.Outputs) > 0 {
					displayAmount = float64(tx.Outputs[0].Amount) / 100.0
				}

				mempoolList = append(mempoolList, map[string]interface{}{
					"txid":   txid,
					"amount": displayAmount,
					"time":   enterTime, // 👈 現在這個時間絕對不會是 0 了！
				})
			} else {
				// 如果解析失敗，至少把 txid 跟時間傳給前端
				mempoolList = append(mempoolList, map[string]interface{}{
					"txid":   txid,
					"amount": 0.0,
					"time":   enterTime, // 👈 失敗時也能帶著修復好的時間
				})
			}
		}

		// 4. 回傳精美的 JSON 給 Vue
		s.writeResult(w, req.ID, mempoolList)

	default:
		s.writeError(w, req.ID, fmt.Sprintf("unknown method: %s", req.Method))
	}
}

// 写响应：成功
func (s *RPCServer) writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := RPCResponse{Result: result, ID: id}
	out, _ := json.Marshal(resp)

	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// 写响应：错误
func (s *RPCServer) writeError(w http.ResponseWriter, id interface{}, msg string) {
	resp := RPCResponse{Error: msg, ID: id}
	out, _ := json.Marshal(resp)

	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

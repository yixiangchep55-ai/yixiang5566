package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"mycoin/network"
	"mycoin/node"
	"mycoin/wallet"
)

// JSON-RPC 标准结构
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

// RPC 服务器本体
type RPCServer struct {
	Node    *node.Node
	Handler *network.Handler
	Wallet  *wallet.Wallet
}

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

		// 2️⃣ 构造 RPC Block
		rpcBlock := RPCBlock{
			Hash:      hex.EncodeToString(b.Hash),
			PrevHash:  hex.EncodeToString(b.PrevHash),
			Height:    b.Height,
			Timestamp: b.Timestamp,
			Nonce:     b.Nonce,
			Target:    b.Target.Text(16),
			CumWork:   bi.CumWorkInt.Text(16),
		}

		// 3️⃣ 填充交易
		for _, tx := range b.Transactions {
			rpcTx := RPCTx{
				TxID: tx.ID,
			}

			for _, in := range tx.Inputs {

				fromAddr := ""

				// ⭐ Coinbase 交易的特殊处理
				if in.TxID == "" {
					fromAddr = "coinbase"
				} else {
					// ⭐ 普通交易：从 UTXO Set 查来源地址
					key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
					if utxo, ok := s.Node.UTXO.Set[key]; ok {
						fromAddr = utxo.To
					} else {
						fromAddr = "unknown"
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
					Amount: out.Amount,
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

		// 4️⃣ 验证交易
		if err := s.Node.VerifyTx(txObj); err != nil {
			s.writeError(w, req.ID, "tx reject: "+err.Error())
			return
		}

		// 5️⃣ 加入 mempool（这里必须能处理序列化）
		ok = s.Node.Mempool.AddTxRBF(txObj.ID, txObj.Serialize(), s.Node.UTXO)
		if !ok {
			s.writeError(w, req.ID, "tx rejected: mempool add failed")
			return
		}

		// 6️⃣ 广播
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

		// ⭐ 使用到了 block（不会 unused）
		result := map[string]interface{}{
			"txid":   txid,
			"block":  block.Hash, // 这里使用 block
			"height": idx.Height,
			"tx":     tx,
		}

		s.writeResult(w, req.ID, result)

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

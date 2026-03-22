package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"mycoin/blockchain"
	"mycoin/network"
	nodepkg "mycoin/node"
)

func syncStateLabel(state nodepkg.SyncState) string {
	switch state {
	case nodepkg.SyncIdle:
		return "idle"
	case nodepkg.SyncIBD:
		return "ibd"
	case nodepkg.SyncHeaders:
		return "headers"
	case nodepkg.SyncBodies:
		return "bodies"
	case nodepkg.SyncSynced:
		return "synced"
	default:
		return "unknown"
	}
}

// RPC
func (s *RPCServer) Start(addr string) {
	http.HandleFunc("/rpc", s.handleRPC)

	log.Println(" RPC server listening at", addr)
	go http.ListenAndServe(addr, nil)
}

// ?RPC
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
	//    APIing
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

	case "getnodeinfo":
		if s.Node == nil {
			s.writeError(w, req.ID, "node not ready")
			return
		}

		peerCount := 0
		if s.Handler != nil && s.Handler.Network != nil {
			peerCount = s.Handler.Network.PeerCount()
		}

		bestHeight := uint64(0)
		bestHash := ""
		if s.Node.Best != nil {
			bestHeight = s.Node.Best.Height
			bestHash = s.Node.Best.Hash
		}

		mempoolCount := 0
		if s.Node.Mempool != nil {
			mempoolCount = len(s.Node.Mempool.GetAll())
		}

		s.writeResult(w, req.ID, map[string]interface{}{
			"node_id":        s.Node.NodeID,
			"mode":           s.Node.Mode,
			"best_height":    bestHeight,
			"best_hash":      bestHash,
			"synced":         s.Node.IsSynced(),
			"sync_state":     syncStateLabel(s.Node.SyncState),
			"is_syncing":     s.Node.IsSyncing,
			"peer_count":     peerCount,
			"mempool_count":  mempoolCount,
			"orphan_count":   len(s.Node.GetOrphanBlocks()),
			"mining_enabled": s.Node.IsMiningEnabled(),
			"mining_address": s.Node.MiningAddress,
		})

	case "setminingenabled":
		if s.Node == nil {
			s.writeError(w, req.ID, "node not ready")
			return
		}
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "enabled flag required")
			return
		}

		enabled, ok := req.Params[0].(bool)
		if !ok {
			s.writeError(w, req.ID, "enabled flag must be boolean")
			return
		}

		s.Node.SetMiningEnabled(enabled)
		s.writeResult(w, req.ID, map[string]interface{}{
			"mining_enabled": s.Node.IsMiningEnabled(),
			"message":        fmt.Sprintf("mining %s", map[bool]string{true: "enabled", false: "disabled"}[enabled]),
		})

	case "getblockhash":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "height required")
			return
		}

		height, ok := req.Params[0].(float64) // JSON ?float64
		if !ok {
			s.writeError(w, req.ID, "invalid height")
			return
		}

		h := int(height)

		if h < 0 {
			s.writeError(w, req.ID, "height out of range")
			return
		}

		bi := s.Node.GetMainChainIndexByHeight(uint64(h))
		if bi == nil {
			s.writeError(w, req.ID, "height out of range")
			return
		}

		s.writeResult(w, req.ID, bi.Hash)

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

		bi, ok := s.Node.Blocks[hash]
		if !ok {
			s.writeError(w, req.ID, "block not found")
			return
		}

		b := bi.Block
		if b == nil {
			b = s.Node.GetBlockForQuery(hash)
			if b == nil {
				if s.Node.IsPrunedMode() {
					s.writeError(w, req.ID, "block body is pruned locally; requested from an archive node, retry later")
				} else {
					s.writeError(w, req.ID, "block body not available locally")
				}
				return
			}
		}

		// 2 ?RPC Block ( Reward )
		rpcBlock := RPCBlock{
			Hash:      hex.EncodeToString(b.Hash),
			PrevHash:  hex.EncodeToString(b.PrevHash),
			Height:    b.Height,
			Timestamp: b.Timestamp,
			Nonce:     b.Nonce,
			Miner:     b.Miner,
			Target:    b.Target.Text(16),
			CumWork:   bi.CumWorkInt.Text(16),
			Reward:    float64(b.Reward) / 100.0, //  ?00 -> 5.00
		}

		// 3
		for _, tx := range b.Transactions {
			rpcTx := RPCTx{
				TxID: tx.ID, // ?hex
			}

			for _, in := range tx.Inputs {
				fromAddr := ""
				if in.TxID == "" {
					fromAddr = "coinbase"
				} else {
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

		// 1 ?mempool
		txBytes, ok := s.Node.Mempool.Get(txid)
		if ok {
			s.writeResult(w, req.ID, string(txBytes))
			return
		}

		// 2
		tx, _, err := s.Node.GetTransaction(txid)
		if err != nil {
			if errors.Is(err, nodepkg.ErrPrunedData) {
				if s.Node.IsPrunedMode() {
					s.writeError(w, req.ID, "transaction is in a pruned block; requested from an archive node, retry later")
				} else {
					s.writeError(w, req.ID, "transaction data not available locally")
				}
				return
			}
			s.writeError(w, req.ID, err.Error())
			return
		}

		s.writeResult(w, req.ID, string(tx.Serialize()))

	case "sendrawtransaction":
		if len(req.Params) != 1 {
			s.writeError(w, req.ID, "rawtx required")
			return
		}

		rawtx, ok := req.Params[0].(map[string]interface{})
		if !ok {
			s.writeError(w, req.ID, "rawtx must be JSON object")
			return
		}

		// ?bytes
		rawBytes, _ := json.Marshal(rawtx)

		// 2 JSON ?DTO
		var dto network.TransactionDTO
		if err := json.Unmarshal(rawBytes, &dto); err != nil {
			s.writeError(w, req.ID, "invalid tx format")
			return
		}

		// 3 DTO ?Transaction
		txObj := network.DTOToTx(dto)

		// 4 ?mempool (?
		if ok := s.Node.AddTx(txObj, s.Node.NodeID); !ok {
			s.writeError(w, req.ID, "tx rejected: validation or mempool error")
			return
		}

		// 5
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

		idx, err := s.Node.GetTxIndex(txid)
		if err != nil {
			s.writeError(w, req.ID, "txindex missing")
			return
		}

		// 2 Node tx + block
		tx, block, err := s.Node.GetTransaction(txid)
		if err != nil {
			if errors.Is(err, nodepkg.ErrPrunedData) || idx.Pruned {
				if s.Node.IsPrunedMode() {
					s.writeError(w, req.ID, "transaction is in a pruned block; requested from an archive node, retry later")
				} else {
					s.writeError(w, req.ID, "transaction data not available locally")
				}
				return
			}
			s.writeError(w, req.ID, err.Error())
			return
		}

		var displayOutputs []TxOutputJSON
		for _, out := range tx.Outputs {
			displayOutputs = append(displayOutputs, TxOutputJSON{
				To:     out.To,
				Amount: float64(out.Amount) / 100.0, //  ?50 -> 1.50
			})
		}

		//  Inputs ()
		var displayInputs []TxInputJSON
		for _, in := range tx.Inputs {
			displayInputs = append(displayInputs, TxInputJSON{
				//   in.TxID?hex.EncodeToString
				TxID:  in.TxID,
				Index: in.Index,
			})
		}

		result := map[string]interface{}{
			"txid":   txid,
			"block":  hex.EncodeToString(block.Hash),
			"height": idx.Height,
			"amount": float64(tx.GetTotalAmount()) / 100.0,
			"details": map[string]interface{}{
				"vin":  displayInputs,
				"vout": displayOutputs,
			},
			"raw_tx": tx,
		}

		s.writeResult(w, req.ID, result)

	case "getmempool":
		mempoolList := make([]map[string]interface{}, 0)
		allTxs := s.Node.Mempool.GetAll()

		for txid, txBytes := range allTxs {
			enterTime := s.Node.Mempool.Times[txid]

			if enterTime == 0 && s.Node.Mempool.DB != nil {
				timeBytes := s.Node.Mempool.DB.Get("mempool_times", txid)
				if len(timeBytes) > 0 {
					parsedTime, err := strconv.ParseInt(string(timeBytes), 10, 64)
					if err == nil {
						enterTime = parsedTime
					} else {
						enterTime = time.Now().Unix()
					}
				} else {
					enterTime = time.Now().Unix()
					timeStr := strconv.FormatInt(enterTime, 10)
					s.Node.Mempool.DB.Put("mempool_times", txid, []byte(timeStr))
				}

				s.Node.Mempool.Times[txid] = enterTime
			}

			tx, err := blockchain.DeserializeTransaction(txBytes)
			if err == nil {
				displayAmount := 0.0
				if len(tx.Outputs) > 0 {
					displayAmount = float64(tx.Outputs[0].Amount) / 100.0
				}

				mempoolList = append(mempoolList, map[string]interface{}{
					"txid":   txid,
					"amount": displayAmount,
					"time":   enterTime,
				})
			} else {
				mempoolList = append(mempoolList, map[string]interface{}{
					"txid":   txid,
					"amount": 0.0,
					"time":   enterTime,
				})
			}
		}

		s.writeResult(w, req.ID, mempoolList)

	case "getorphans":
		type orphanSummary struct {
			Height    uint64 `json:"Height"`
			Hash      string `json:"Hash"`
			Miner     string `json:"Miner"`
			Timestamp int64  `json:"Timestamp"`
			TxCount   int    `json:"TxCount"`
		}

		orphans := s.Node.GetOrphanBlocks()
		summaries := make([]orphanSummary, 0, len(orphans))

		for _, blk := range orphans {
			summaries = append(summaries, orphanSummary{
				Height:    blk.Height,
				Hash:      hex.EncodeToString(blk.Hash),
				Miner:     blk.Miner,
				Timestamp: blk.Timestamp,
				TxCount:   len(blk.Transactions),
			})

			if len(summaries) >= 15 {
				break
			}
		}

		s.writeResult(w, req.ID, summaries)

	default:
		s.writeError(w, req.ID, fmt.Sprintf("unknown method: %s", req.Method))
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

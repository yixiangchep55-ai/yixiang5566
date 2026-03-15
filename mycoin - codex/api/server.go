package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mycoin/indexer"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	localNodeRPCURL   = "http://localhost:8081/rpc"
	localWalletRPCURL = "http://localhost:8082/wallet"
	defaultRPCPort    = "8081"
)

type RemoteNode struct {
	Name string `json:"name"`
	Host string `json:"host"`
	RPC  string `json:"rpc"`
}

type RemoteNodeStatus struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Online   bool   `json:"online"`
	LastSeen string `json:"last_seen"`
}

type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  interface{}     `json:"error"`
}

var (
	apiHTTPClient = &http.Client{Timeout: 3 * time.Second}
	explorerNodes = loadExplorerNodes()
)

func StartServer(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/blocks", getMainBlocks)
	mux.HandleFunc("/api/orphans", getOrphanBlocks)
	mux.HandleFunc("/api/address/", getAddressBalance)
	mux.HandleFunc("/api/transaction", sendTransaction)
	mux.HandleFunc("/api/estimatefee", getEstimateFee)
	mux.HandleFunc("/api/mempool", getMempool)
	mux.HandleFunc("/api/block/", getBlockDetails)
	mux.HandleFunc("/api/tx/", getTransactionDetails)
	mux.HandleFunc("/api/nodes", getNodes)

	fmt.Printf("[API] Explorer API listening at http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Println("[API] server failed:", err)
	}
}

func getMainBlocks(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	countResult, rpcErr, err := rpcCall(localNodeRPCURL, "getblockcount", []interface{}{})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "node RPC is unavailable",
		})
		return
	}
	if rpcErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("%v", rpcErr),
		})
		return
	}

	var bestHeight int
	if err := json.Unmarshal(countResult, &bestHeight); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "node RPC returned an invalid block height",
		})
		return
	}

	type blockSummary struct {
		Height    int     `json:"Height"`
		Hash      string  `json:"Hash"`
		Miner     string  `json:"Miner"`
		Timestamp int64   `json:"Timestamp"`
		TxCount   int     `json:"TxCount"`
		Target    string  `json:"Target"`
		Reward    float64 `json:"Reward"`
	}

	type rpcBlock struct {
		Height       int             `json:"height"`
		Hash         string          `json:"hash"`
		Miner        string          `json:"miner"`
		Timestamp    int64           `json:"timestamp"`
		Target       string          `json:"target"`
		Reward       float64         `json:"reward"`
		Transactions json.RawMessage `json:"transactions"`
	}

	startHeight := bestHeight - 14
	if startHeight < 0 {
		startHeight = 0
	}

	blocks := make([]blockSummary, 0, bestHeight-startHeight+1)
	for height := bestHeight; height >= startHeight; height-- {
		hashResult, rpcErr, err := rpcCall(localNodeRPCURL, "getblockhash", []interface{}{height})
		if err != nil || rpcErr != nil {
			continue
		}

		var blockHash string
		if err := json.Unmarshal(hashResult, &blockHash); err != nil || blockHash == "" {
			continue
		}

		blockResult, rpcErr, err := rpcCall(localNodeRPCURL, "getblock", []interface{}{blockHash})
		if err != nil || rpcErr != nil {
			continue
		}

		var block rpcBlock
		if err := json.Unmarshal(blockResult, &block); err != nil {
			continue
		}

		var txs []json.RawMessage
		_ = json.Unmarshal(block.Transactions, &txs)

		blocks = append(blocks, blockSummary{
			Height:    block.Height,
			Hash:      block.Hash,
			Miner:     block.Miner,
			Timestamp: block.Timestamp,
			TxCount:   len(txs),
			Target:    block.Target,
			Reward:    block.Reward,
		})
	}

	writeJSON(w, http.StatusOK, blocks)
}

func getOrphanBlocks(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	result, rpcErr, err := rpcCall(localNodeRPCURL, "getorphans", []interface{}{})
	if err != nil || rpcErr != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	if len(result) == 0 || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	var payload interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	writeJSON(w, http.StatusOK, payload)
}

func getAddressBalance(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	if indexer.DB == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "indexer is not enabled",
		})
		return
	}

	address := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/address/"))
	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "address is required",
		})
		return
	}

	var totalIn uint64
	if err := indexer.DB.Model(&indexer.AddressLedger{}).
		Where("address = ? AND type = ?", address, "IN").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalIn).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to query incoming value",
		})
		return
	}

	var totalOut uint64
	if err := indexer.DB.Model(&indexer.AddressLedger{}).
		Where("address = ? AND type = ?", address, "OUT").
		Select("COALESCE(SUM(amount), 0)").
		Scan(&totalOut).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to query outgoing value",
		})
		return
	}

	message := ""
	if totalIn == 0 && totalOut == 0 {
		message = "This address has no transaction history yet."
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"Address": address,
		"Balance": float64(totalIn-totalOut) / 100.0,
		"Message": message,
	})
}

func sendTransaction(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}

	var req struct {
		To     string  `json:"to"`
		Amount float64 `json:"amount"`
		Fee    float64 `json:"fee"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid transaction payload",
		})
		return
	}

	req.To = strings.TrimSpace(req.To)
	if req.To == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "recipient and positive amount are required",
		})
		return
	}

	result, rpcErr, err := rpcCall(localWalletRPCURL, "sendtoaddress", []interface{}{req.To, req.Amount, req.Fee})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "wallet RPC is unavailable",
		})
		return
	}
	if rpcErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("wallet rejected transaction: %v", rpcErr),
		})
		return
	}

	var txID string
	if err := json.Unmarshal(result, &txID); err != nil || txID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "wallet RPC returned an invalid txid",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"txid": txID})
}

func getEstimateFee(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	result, rpcErr, err := rpcCall(localWalletRPCURL, "estimatefee", []interface{}{})
	if err != nil || rpcErr != nil {
		writeJSON(w, http.StatusOK, map[string]float64{"fee": 0.01})
		return
	}

	var fee float64
	if err := json.Unmarshal(result, &fee); err != nil {
		fee = 0.01
	}

	writeJSON(w, http.StatusOK, map[string]float64{"fee": fee})
}

func getMempool(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	result, rpcErr, err := rpcCall(localNodeRPCURL, "getmempool", []interface{}{})
	if err != nil || rpcErr != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	if len(result) == 0 || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	var payload interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	writeJSON(w, http.StatusOK, payload)
}

func getTransactionDetails(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	txID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/tx/"))
	if len(txID) != 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid transaction id",
		})
		return
	}

	result, rpcErr, err := rpcCall(localWalletRPCURL, "getwallettransaction", []interface{}{txID})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "wallet RPC is unavailable",
		})
		return
	}
	if rpcErr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("%v", rpcErr),
		})
		return
	}

	var payload interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "invalid wallet transaction payload",
		})
		return
	}

	writeJSON(w, http.StatusOK, payload)
}

func getBlockDetails(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	query := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/block/"))
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "block height or hash is required",
		})
		return
	}

	blockHash := query
	if isNumericQuery(query) {
		result, rpcErr, err := rpcCall(localNodeRPCURL, "getblockhash", []interface{}{mustParseHeight(query)})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "node RPC is unavailable",
			})
			return
		}
		if rpcErr != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": fmt.Sprintf("%v", rpcErr),
			})
			return
		}
		if err := json.Unmarshal(result, &blockHash); err != nil || blockHash == "" {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "node RPC returned an invalid block hash",
			})
			return
		}
	}

	if len(blockHash) != 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid block height or hash",
		})
		return
	}

	result, rpcErr, err := rpcCall(localNodeRPCURL, "getblock", []interface{}{blockHash})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "node RPC is unavailable",
		})
		return
	}
	if rpcErr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("%v", rpcErr),
		})
		return
	}

	var payload interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "invalid block payload",
		})
		return
	}

	writeJSON(w, http.StatusOK, payload)
}

func getNodes(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	statuses := make([]RemoteNodeStatus, 0, len(explorerNodes))
	for _, node := range explorerNodes {
		status := RemoteNodeStatus{
			Name:     node.Name,
			Host:     node.Host,
			Online:   false,
			LastSeen: "Never",
		}

		if probeNode(node) {
			status.Online = true
			status.LastSeen = time.Now().Format(time.RFC3339)
		}

		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return strings.ToLower(statuses[i].Name) < strings.ToLower(statuses[j].Name)
	})

	writeJSON(w, http.StatusOK, statuses)
}

func loadExplorerNodes() []RemoteNode {
	raw := strings.TrimSpace(os.Getenv("MYCOIN_EXPLORER_NODES"))
	if raw == "" {
		return []RemoteNode{
			{
				Name: "Local Node",
				Host: "127.0.0.1:" + defaultRPCPort,
				RPC:  "http://127.0.0.1:" + defaultRPCPort + "/rpc",
			},
		}
	}

	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == '\n'
	})

	nodes := make([]RemoteNode, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}

		name := ""
		host := entry
		if before, after, ok := strings.Cut(entry, "@"); ok {
			name = strings.TrimSpace(before)
			host = strings.TrimSpace(after)
		}

		if host == "" {
			continue
		}
		if !strings.Contains(host, ":") {
			host += ":" + defaultRPCPort
		}
		if name == "" {
			name = host
		}

		nodes = append(nodes, RemoteNode{
			Name: name,
			Host: host,
			RPC:  "http://" + host + "/rpc",
		})
	}

	if len(nodes) == 0 {
		return []RemoteNode{
			{
				Name: "Local Node",
				Host: "127.0.0.1:" + defaultRPCPort,
				RPC:  "http://127.0.0.1:" + defaultRPCPort + "/rpc",
			},
		}
	}

	return nodes
}

func probeNode(node RemoteNode) bool {
	result, rpcErr, err := rpcCall(node.RPC, "ping", []interface{}{})
	if err != nil || rpcErr != nil {
		return false
	}

	var pong string
	return json.Unmarshal(result, &pong) == nil && pong == "pong"
}

func rpcCall(url, method string, params []interface{}) (json.RawMessage, interface{}, error) {
	payload := map[string]interface{}{
		"method": method,
		"params": params,
		"id":     1,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}

	resp, err := apiHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, nil, fmt.Errorf("rpc status %d", resp.StatusCode)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(rawBody, &rpcResp); err != nil {
		return nil, nil, err
	}

	return rpcResp.Result, rpcResp.Error, nil
}

func isNumericQuery(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func mustParseHeight(value string) int {
	height := 0
	for _, r := range value {
		height = height*10 + int(r-'0')
	}
	return height
}

func allowAPIRequest(w http.ResponseWriter, r *http.Request, method string) bool {
	setCommonHeaders(w)

	if r.Method == http.MethodOptions {
		return false
	}
	if r.Method != method {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"error": method + " only",
		})
		return false
	}
	return true
}

func setCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mycoin/indexer"
	uiembed "mycoin/mycoin-explorer"
	"net/http"
	"net/url"
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

type DormantAddressSummary struct {
	Address     string    `json:"address"`
	Balance     float64   `json:"balance"`
	LastActive  time.Time `json:"last_active"`
	DormantDays int       `json:"dormant_days"`
	TxCount     int64     `json:"tx_count"`
	TotalIn     float64   `json:"total_in"`
	TotalOut    float64   `json:"total_out"`
}

type DashboardNodeStatus struct {
	NodeID        uint64 `json:"node_id"`
	Mode          string `json:"mode"`
	BestHeight    uint64 `json:"best_height"`
	BestHash      string `json:"best_hash"`
	Synced        bool   `json:"synced"`
	SyncState     string `json:"sync_state"`
	IsSyncing     bool   `json:"is_syncing"`
	PeerCount     int    `json:"peer_count"`
	MempoolCount  int    `json:"mempool_count"`
	OrphanCount   int    `json:"orphan_count"`
	MiningEnabled bool   `json:"mining_enabled"`
	MiningAddress string `json:"mining_address"`
}

type DashboardWalletStatus struct {
	Address       string  `json:"address"`
	Balance       float64 `json:"balance"`
	PendingTxs    int     `json:"pending_txs"`
	MiningAddress string  `json:"mining_address"`
}

type DashboardWalletActivity struct {
	Status        string  `json:"status"`
	Type          string  `json:"type"`
	Amount        float64 `json:"amount"`
	Confirmations int     `json:"confirmations"`
	TxID          string  `json:"txid"`
	Timestamp     int64   `json:"timestamp"`
}

type DashboardStatusResponse struct {
	Node             *DashboardNodeStatus      `json:"node"`
	Wallet           *DashboardWalletStatus    `json:"wallet"`
	SpendableBalance float64                   `json:"spendable_balance"`
	PendingAmount    float64                   `json:"pending_amount"`
	Activity         []DashboardWalletActivity `json:"activity"`
	Indexer          *DashboardIndexerStatus   `json:"indexer,omitempty"`
	Timestamp        string                    `json:"timestamp"`
}

type DashboardIndexerStatus struct {
	Requested    bool   `json:"requested"`
	Running      bool   `json:"running"`
	UsingDefault bool   `json:"using_default"`
	Status       string `json:"status"`
	Message      string `json:"message"`
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
	mux.HandleFunc("/api/dormant-addresses", getDormantAddresses)
	mux.HandleFunc("/api/dashboard/status", getDashboardStatus)
	mux.HandleFunc("/api/miner/control", setMiningControl)
	mux.Handle("/", explorerUIHandler())

	fmt.Printf("[API] Dashboard API listening at http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Println("[API] server failed:", err)
	}
}

func explorerUIHandler() http.Handler {
	distFS, err := uiembed.DistFS()
	if err != nil {
		externalExplorer := strings.TrimSpace(os.Getenv("MYCOIN_EXPLORER_URL"))
		if externalExplorer != "" {
			return explorerRedirectHandler(externalExplorer)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("Explorer UI is not bundled in this node build. Build with -tags explorerui after generating mycoin-explorer/dist, or set MYCOIN_EXPLORER_URL to your shared explorer."))
		})
	}

	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if _, err := fs.Stat(distFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		indexHTML, err := fs.ReadFile(distFS, "index.html")
		if err != nil {
			http.Error(w, "embedded frontend is unavailable", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexHTML)
	})
}

func explorerRedirectHandler(base string) http.Handler {
	target, err := url.Parse(base)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "configured explorer URL is invalid", http.StatusInternalServerError)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		ref := &url.URL{Path: r.URL.Path, RawQuery: r.URL.RawQuery, Fragment: r.URL.Fragment}
		http.Redirect(w, r, target.ResolveReference(ref).String(), http.StatusTemporaryRedirect)
	})
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

func getDormantAddresses(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	if indexer.DB == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "indexer is not enabled",
		})
		return
	}

	type dormantAddressRow struct {
		Address     string    `gorm:"column:address"`
		BalanceRaw  int64     `gorm:"column:balance_raw"`
		LastActive  time.Time `gorm:"column:last_active"`
		TxCount     int64     `gorm:"column:tx_count"`
		TotalInRaw  int64     `gorm:"column:total_in_raw"`
		TotalOutRaw int64     `gorm:"column:total_out_raw"`
	}

	var rows []dormantAddressRow
	query := `
		SELECT
			address,
			COALESCE(SUM(CASE WHEN type = 'IN' THEN amount ELSE 0 END), 0) -
				COALESCE(SUM(CASE WHEN type = 'OUT' THEN amount ELSE 0 END), 0) AS balance_raw,
			MAX(created_at) AS last_active,
			COUNT(DISTINCT tx_id) AS tx_count,
			COALESCE(SUM(CASE WHEN type = 'IN' THEN amount ELSE 0 END), 0) AS total_in_raw,
			COALESCE(SUM(CASE WHEN type = 'OUT' THEN amount ELSE 0 END), 0) AS total_out_raw
		FROM address_ledgers
		GROUP BY address
		HAVING
			COALESCE(SUM(CASE WHEN type = 'IN' THEN amount ELSE 0 END), 0) -
				COALESCE(SUM(CASE WHEN type = 'OUT' THEN amount ELSE 0 END), 0) > 0
		ORDER BY MAX(created_at) ASC
		LIMIT 25;
	`

	if err := indexer.DB.Raw(query).Scan(&rows).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to query dormant addresses",
		})
		return
	}

	now := time.Now()
	results := make([]DormantAddressSummary, 0, len(rows))
	for _, row := range rows {
		dormantDays := 0
		if !row.LastActive.IsZero() {
			dormantDays = int(now.Sub(row.LastActive).Hours() / 24)
		}

		results = append(results, DormantAddressSummary{
			Address:     row.Address,
			Balance:     float64(row.BalanceRaw) / 100.0,
			LastActive:  row.LastActive,
			DormantDays: dormantDays,
			TxCount:     row.TxCount,
			TotalIn:     float64(row.TotalInRaw) / 100.0,
			TotalOut:    float64(row.TotalOutRaw) / 100.0,
		})
	}

	writeJSON(w, http.StatusOK, results)
}

func getDashboardStatus(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}

	response := DashboardStatusResponse{
		Timestamp: time.Now().Format(time.RFC3339),
		Indexer:   currentIndexerStatus(),
	}

	nodeResult, nodeRPCErr, nodeErr := rpcCall(localNodeRPCURL, "getnodeinfo", []interface{}{})
	if nodeErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "node RPC is unavailable",
		})
		return
	}
	if nodeRPCErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("%v", nodeRPCErr),
		})
		return
	}

	var nodeStatus DashboardNodeStatus
	if err := json.Unmarshal(nodeResult, &nodeStatus); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "invalid node status payload",
		})
		return
	}
	response.Node = &nodeStatus

	walletResult, walletRPCErr, walletErr := rpcCall(localWalletRPCURL, "getwalletinfo", []interface{}{})
	if walletErr == nil && walletRPCErr == nil {
		var walletStatus DashboardWalletStatus
		if err := json.Unmarshal(walletResult, &walletStatus); err == nil {
			response.Wallet = &walletStatus
			response.SpendableBalance = walletStatus.Balance
		}
	}

	activityResult, activityRPCErr, activityErr := rpcCall(localWalletRPCURL, "listwalletactivity", []interface{}{8})
	if activityErr == nil && activityRPCErr == nil {
		var activity []DashboardWalletActivity
		if err := json.Unmarshal(activityResult, &activity); err == nil {
			response.Activity = activity
			pendingOutgoing := 0.0
			pendingTotal := 0.0
			for _, item := range activity {
				if !strings.EqualFold(item.Status, "Pending") {
					continue
				}
				if item.Amount < 0 {
					pendingOutgoing += -item.Amount
					pendingTotal += -item.Amount
				} else {
					pendingTotal += item.Amount
				}
			}
			response.PendingAmount = pendingTotal
			if response.Wallet != nil {
				response.SpendableBalance = response.Wallet.Balance - pendingOutgoing
				if response.SpendableBalance < 0 {
					response.SpendableBalance = 0
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, response)
}

func currentIndexerStatus() *DashboardIndexerStatus {
	requested := strings.EqualFold(strings.TrimSpace(os.Getenv("INDEXER_ENABLED")), "true")
	usingDefault := strings.TrimSpace(os.Getenv("DATABASE_URL")) == ""
	running := requested && indexer.Enabled && indexer.DB != nil

	status := "Disabled"
	message := "Indexer was not enabled for this node session."

	if running {
		status = "Running"
		if usingDefault {
			message = "Indexer is running with the backend default PostgreSQL settings."
		} else {
			message = "Indexer is running with the configured PostgreSQL connection."
		}
	} else if requested {
		status = "DB error"
		if usingDefault {
			message = "Indexer was requested, but PostgreSQL did not connect using the backend default settings."
		} else {
			message = "Indexer was requested, but PostgreSQL did not connect using the configured database URL."
		}
	}

	return &DashboardIndexerStatus{
		Requested:    requested,
		Running:      running,
		UsingDefault: usingDefault,
		Status:       status,
		Message:      message,
	}
}

func setMiningControl(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid miner control payload",
		})
		return
	}

	result, rpcErr, err := rpcCall(localNodeRPCURL, "setminingenabled", []interface{}{req.Enabled})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "node RPC is unavailable",
		})
		return
	}
	if rpcErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("%v", rpcErr),
		})
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "invalid miner control payload",
		})
		return
	}

	writeJSON(w, http.StatusOK, payload)
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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type App struct {
	ctx          context.Context
	httpClient   *http.Client
	mu           sync.Mutex
	settings     DashboardSettings
	settingsPath string
	lastSnapshot *dashboardStatusResponse
	events       []DashboardEvent
	history      []HeightPoint
	backendCmd   *exec.Cmd
	backendPath  string
	backendMode  string
	backendLogs  []string
}

type DashboardSettings struct {
	APIBase         string `json:"apiBase"`
	ExplorerBaseURL string `json:"explorerBaseURL"`
	EnableIndexer   bool   `json:"enableIndexer"`
	DatabaseURL     string `json:"databaseURL"`
}

type IndexerStatusView struct {
	Enabled      bool   `json:"enabled"`
	Requested    bool   `json:"requested"`
	Reachable    bool   `json:"reachable"`
	Running      bool   `json:"running"`
	UsingDefault bool   `json:"using_default"`
	NeedsRestart bool   `json:"needs_restart"`
	Status       string `json:"status"`
	Message      string `json:"message"`
}

type DashboardPayload struct {
	Connected  bool                `json:"connected"`
	Timestamp  string              `json:"timestamp"`
	Node       dashboardNodeStatus `json:"node"`
	Wallet     dashboardWalletView `json:"wallet"`
	Events     []DashboardEvent    `json:"events"`
	History    []HeightPoint       `json:"history"`
	BackendLog []string            `json:"backend_log"`
	Settings   DashboardSettings   `json:"settings"`
	Indexer    IndexerStatusView   `json:"indexer"`
	Message    string              `json:"message"`
}

type HeightPoint struct {
	Label  string `json:"label"`
	Height uint64 `json:"height"`
}

type DashboardEvent struct {
	Time        string `json:"time"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type SendTransactionRequest struct {
	To     string  `json:"to"`
	Amount float64 `json:"amount"`
	Fee    float64 `json:"fee"`
}

type SendTransactionResult struct {
	Success bool   `json:"success"`
	TxID    string `json:"txid"`
	Message string `json:"message"`
}

type MiningControlResult struct {
	MiningEnabled bool   `json:"mining_enabled"`
	Message       string `json:"message"`
}

type BackendLaunchResult struct {
	Started        bool   `json:"started"`
	AlreadyRunning bool   `json:"alreadyRunning"`
	Mode           string `json:"mode"`
	Executable     string `json:"executable"`
	Message        string `json:"message"`
}

type dashboardNodeStatus struct {
	NodeID         uint64 `json:"node_id"`
	Mode           string `json:"mode"`
	BestHeight     uint64 `json:"best_height"`
	BestHash       string `json:"best_hash"`
	Synced         bool   `json:"synced"`
	SyncState      string `json:"sync_state"`
	IsSyncing      bool   `json:"is_syncing"`
	PeerCount      int    `json:"peer_count"`
	LastPeerEvent  string `json:"last_peer_event"`
	LastPeerAddr   string `json:"last_peer_addr"`
	LastPeerError  string `json:"last_peer_error"`
	LastPeerSeenAt string `json:"last_peer_seen_at"`
	MempoolCount   int    `json:"mempool_count"`
	OrphanCount    int    `json:"orphan_count"`
	MiningEnabled  bool   `json:"mining_enabled"`
	MiningAddress  string `json:"mining_address"`
}

type dashboardWalletStatus struct {
	Address       string  `json:"address"`
	Balance       float64 `json:"balance"`
	PendingTxs    int     `json:"pending_txs"`
	MiningAddress string  `json:"mining_address"`
}

type dashboardWalletActivity struct {
	Status        string  `json:"status"`
	Type          string  `json:"type"`
	Amount        float64 `json:"amount"`
	Confirmations int     `json:"confirmations"`
	TxID          string  `json:"txid"`
	Timestamp     int64   `json:"timestamp"`
}

type dashboardWalletView struct {
	Address          string                    `json:"address"`
	Balance          float64                   `json:"balance"`
	SpendableBalance float64                   `json:"spendable_balance"`
	PendingAmount    float64                   `json:"pending_amount"`
	PendingTxs       int                       `json:"pending_txs"`
	Activity         []dashboardWalletActivity `json:"activity"`
}

type dashboardStatusResponse struct {
	Node             *dashboardNodeStatus      `json:"node"`
	Wallet           *dashboardWalletStatus    `json:"wallet"`
	SpendableBalance float64                   `json:"spendable_balance"`
	PendingAmount    float64                   `json:"pending_amount"`
	Activity         []dashboardWalletActivity `json:"activity"`
	Indexer          *IndexerStatusView        `json:"indexer"`
	Timestamp        string                    `json:"timestamp"`
}

type backendLogSink struct {
	app *App
}

func (s *backendLogSink) Write(p []byte) (int, error) {
	if s != nil && s.app != nil {
		s.app.appendBackendLogChunk(string(p))
	}
	return len(p), nil
}

func NewApp() *App {
	configDir, _ := os.UserConfigDir()
	app := &App{
		httpClient: &http.Client{Timeout: 3 * time.Second},
		settings: DashboardSettings{
			APIBase:         "http://127.0.0.1:8080",
			ExplorerBaseURL: "",
			EnableIndexer:   false,
			DatabaseURL:     "",
		},
		settingsPath: filepath.Join(configDir, "mycoin-dashboard-wails", "settings.json"),
		events:       make([]DashboardEvent, 0, 24),
		history:      make([]HeightPoint, 0, 16),
		backendLogs:  make([]string, 0, 48),
	}
	app.loadSettingsFromDisk()
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
	a.stopManagedBackend()
}

func (a *App) StartBackend(mode string) (*BackendLaunchResult, error) {
	normalizedMode := normalizeChainMode(mode)
	if normalizedMode == "" {
		return nil, errors.New("invalid node mode")
	}

	a.mu.Lock()
	settings := a.settings
	a.mu.Unlock()

	executable, err := a.resolveBackendExecutable()
	if err != nil {
		return nil, err
	}

	if a.apiReachable() {
		a.mu.Lock()
		a.backendPath = executable
		a.backendMode = normalizedMode
		a.mu.Unlock()

		return &BackendLaunchResult{
			Started:        false,
			AlreadyRunning: true,
			Mode:           normalizedMode,
			Executable:     executable,
			Message:        "Local node is already running.",
		}, nil
	}

	a.mu.Lock()
	if a.backendCmd != nil && a.backendCmd.Process != nil {
		currentPath := a.backendPath
		currentMode := a.backendMode
		a.mu.Unlock()
		return &BackendLaunchResult{
			Started:        false,
			AlreadyRunning: true,
			Mode:           currentMode,
			Executable:     currentPath,
			Message:        "Local node is already starting.",
		}, nil
	}
	a.mu.Unlock()

	cmd := exec.Command(executable, "-mode", normalizedMode)
	cmd.Dir = filepath.Dir(executable)
	a.resetBackendLogs()
	logSink := &backendLogSink{app: a}
	cmd.Stdout = logSink
	cmd.Stderr = logSink
	cmd.SysProcAttr = backendSysProcAttr()
	cmd.Env = append([]string{}, os.Environ()...)

	indexerStatus := evaluateIndexerStatus(settings)
	launchMessage := "Starting local node in the background."
	if settings.EnableIndexer {
		cmd.Env = withEnv(cmd.Env, "INDEXER_ENABLED", "true")
		databaseURL := strings.TrimSpace(settings.DatabaseURL)
		if databaseURL != "" {
			cmd.Env = withEnv(cmd.Env, "DATABASE_URL", databaseURL)
		}

		if databaseURL == "" {
			launchMessage = "Starting local node with indexer enabled using the backend default PostgreSQL settings."
		} else {
			launchMessage = "Starting local node with indexer enabled."
		}

		if !indexerStatus.Reachable {
			launchMessage = fmt.Sprintf("%s %s", launchMessage, indexerStatus.Message)
		}
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("unable to start local node: %w", err)
	}

	a.mu.Lock()
	a.backendCmd = cmd
	a.backendPath = executable
	a.backendMode = normalizedMode
	a.mu.Unlock()

	go func(started *exec.Cmd) {
		_ = started.Wait()
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.backendCmd == started {
			a.backendCmd = nil
		}
	}(cmd)

	return &BackendLaunchResult{
		Started:        true,
		AlreadyRunning: false,
		Mode:           normalizedMode,
		Executable:     executable,
		Message:        launchMessage,
	}, nil
}

func (a *App) GetDashboardData() (*DashboardPayload, error) {
	return a.fetchDashboardData()
}

func (a *App) ReconnectAPI() (*DashboardPayload, error) {
	return a.fetchDashboardData()
}

func (a *App) GetSettings() DashboardSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

func (a *App) SaveSettings(input DashboardSettings) DashboardSettings {
	a.mu.Lock()

	if input.APIBase != "" {
		a.settings.APIBase = input.APIBase
	}
	a.settings.ExplorerBaseURL = strings.TrimSpace(input.ExplorerBaseURL)
	a.settings.EnableIndexer = input.EnableIndexer
	a.settings.DatabaseURL = strings.TrimSpace(input.DatabaseURL)

	saved := a.settings
	a.mu.Unlock()
	a.saveSettingsToDisk(saved)
	return saved
}

func (a *App) CheckIndexerConnection(input DashboardSettings) (*IndexerStatusView, error) {
	a.mu.Lock()
	settings := a.settings
	a.mu.Unlock()
	if input.APIBase != "" || input.ExplorerBaseURL != "" || input.DatabaseURL != "" || input.EnableIndexer {
		settings = mergeSettings(settings, input)
	}

	status := evaluateIndexerStatus(settings)
	return &status, nil
}

func (a *App) SendTransaction(req SendTransactionRequest) (*SendTransactionResult, error) {
	payload := map[string]interface{}{
		"to":     req.To,
		"amount": req.Amount,
		"fee":    req.Fee,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	apiBase := a.settings.APIBase
	a.mu.Unlock()

	resp, err := a.httpClient.Post(apiBase+"/api/transaction", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		var failure map[string]string
		_ = json.Unmarshal(raw, &failure)
		msg := "transaction failed"
		if failure["error"] != "" {
			msg = failure["error"]
		}
		return &SendTransactionResult{Success: false, Message: msg}, nil
	}

	var success struct {
		TxID string `json:"txid"`
	}
	if err := json.Unmarshal(raw, &success); err != nil {
		return nil, err
	}

	return &SendTransactionResult{
		Success: true,
		TxID:    success.TxID,
		Message: "Transaction broadcast successfully.",
	}, nil
}

func (a *App) SetMiningEnabled(enabled bool) (*MiningControlResult, error) {
	body, err := json.Marshal(map[string]bool{
		"enabled": enabled,
	})
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	apiBase := a.settings.APIBase
	a.mu.Unlock()

	resp, err := a.httpClient.Post(apiBase+"/api/miner/control", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		var failure map[string]string
		_ = json.Unmarshal(raw, &failure)
		msg := "unable to update miner state"
		if failure["error"] != "" {
			msg = failure["error"]
		}
		return nil, errors.New(msg)
	}

	var result MiningControlResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (a *App) fetchDashboardData() (*DashboardPayload, error) {
	a.mu.Lock()
	apiBase := a.settings.APIBase
	settings := a.settings
	prev := a.lastSnapshot
	a.mu.Unlock()
	indexerStatus := evaluateIndexerStatus(settings)

	resp, err := a.httpClient.Get(apiBase + "/api/dashboard/status")
	if err != nil {
		return a.disconnectedPayload(settings, indexerStatus, fmt.Sprintf("API unreachable at %s", apiBase)), nil
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		message := fmt.Sprintf("API returned %d", resp.StatusCode)
		var failure map[string]interface{}
		if err := json.Unmarshal(raw, &failure); err == nil {
			if apiError := strings.TrimSpace(fmt.Sprintf("%v", failure["error"])); apiError != "" && apiError != "<nil>" {
				message = fmt.Sprintf("%s: %s", message, apiError)
			}
		}
		return a.disconnectedPayload(settings, indexerStatus, message), nil
	}

	var snapshot dashboardStatusResponse
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}

	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	a.recordEventsLocked(prev, &snapshot, now)
	a.recordHistoryLocked(&snapshot, now)
	a.lastSnapshot = &snapshot

	if snapshot.Indexer != nil {
		indexerStatus = mergeIndexerRuntimeStatus(settings, *snapshot.Indexer)
	}

	payload := buildPayload(snapshot, a.events, a.history, settings, indexerStatus, true, "")
	payload.BackendLog = append([]string(nil), a.backendLogs...)
	return &payload, nil
}

func (a *App) disconnectedPayload(settings DashboardSettings, indexerStatus IndexerStatusView, message string) *DashboardPayload {
	a.mu.Lock()
	defer a.mu.Unlock()

	payload := DashboardPayload{
		Connected: false,
		Timestamp: time.Now().Format(time.RFC3339),
		Node: dashboardNodeStatus{
			Mode:       "offline",
			SyncState:  "offline",
			IsSyncing:  false,
			BestHeight: 0,
		},
		Events:     append([]DashboardEvent(nil), a.events...),
		History:    append([]HeightPoint(nil), a.history...),
		BackendLog: append([]string(nil), a.backendLogs...),
		Settings:   settings,
		Indexer:    indexerStatus,
		Message:    message,
	}

	if payload.Node.Mode == "" {
		payload.Node.Mode = "offline"
	}
	return &payload
}

func buildPayload(snapshot dashboardStatusResponse, events []DashboardEvent, history []HeightPoint, settings DashboardSettings, indexerStatus IndexerStatusView, connected bool, message string) DashboardPayload {
	payload := DashboardPayload{
		Connected: connected,
		Timestamp: snapshot.Timestamp,
		Events:    append([]DashboardEvent(nil), events...),
		History:   append([]HeightPoint(nil), history...),
		Settings:  settings,
		Indexer:   indexerStatus,
		Message:   message,
	}

	if snapshot.Node != nil {
		payload.Node = *snapshot.Node
	}

	if snapshot.Wallet != nil {
		payload.Wallet = dashboardWalletView{
			Address:          snapshot.Wallet.Address,
			Balance:          snapshot.Wallet.Balance,
			SpendableBalance: snapshot.SpendableBalance,
			PendingAmount:    snapshot.PendingAmount,
			PendingTxs:       snapshot.Wallet.PendingTxs,
			Activity:         append([]dashboardWalletActivity(nil), snapshot.Activity...),
		}
	}

	return payload
}

func (a *App) recordHistoryLocked(snapshot *dashboardStatusResponse, now time.Time) {
	if snapshot == nil || snapshot.Node == nil {
		return
	}

	label := now.Format("15:04:05")
	height := snapshot.Node.BestHeight

	if len(a.history) > 0 {
		last := a.history[len(a.history)-1]
		if last.Height == height {
			a.history[len(a.history)-1].Label = label
			return
		}
	}

	a.history = append(a.history, HeightPoint{
		Label:  label,
		Height: height,
	})
	if len(a.history) > 12 {
		a.history = a.history[len(a.history)-12:]
	}
}

func (a *App) recordEventsLocked(prev, curr *dashboardStatusResponse, now time.Time) {
	if curr == nil {
		return
	}
	if prev == nil {
		a.pushEventLocked(now, "node", "Dashboard connected", "Connected to local API.")
		return
	}
	if prev.Node != nil && curr.Node != nil {
		if prev.Node.BestHeight != curr.Node.BestHeight {
			a.pushEventLocked(now, "height", "Height changed", fmt.Sprintf("%d -> %d", prev.Node.BestHeight, curr.Node.BestHeight))
		}
		if prev.Node.SyncState != curr.Node.SyncState || prev.Node.IsSyncing != curr.Node.IsSyncing {
			a.pushEventLocked(now, "sync", "Sync status changed", fmt.Sprintf("%s -> %s", prev.Node.SyncState, curr.Node.SyncState))
		}
		if prev.Node.PeerCount != curr.Node.PeerCount {
			a.pushEventLocked(now, "peers", "Peer count changed", fmt.Sprintf("%d -> %d", prev.Node.PeerCount, curr.Node.PeerCount))
		}
		if prev.Node.MempoolCount != curr.Node.MempoolCount {
			a.pushEventLocked(now, "mempool", "Mempool changed", fmt.Sprintf("%d -> %d", prev.Node.MempoolCount, curr.Node.MempoolCount))
		}
		if prev.Node.OrphanCount != curr.Node.OrphanCount {
			a.pushEventLocked(now, "orphan", "Orphan count changed", fmt.Sprintf("%d -> %d", prev.Node.OrphanCount, curr.Node.OrphanCount))
		}
	}
	if prev.Wallet != nil && curr.Wallet != nil {
		if prev.Wallet.Balance != curr.Wallet.Balance {
			a.pushEventLocked(now, "wallet", "Wallet balance changed", fmt.Sprintf("%.2f -> %.2f", prev.Wallet.Balance, curr.Wallet.Balance))
		}
		if prev.Wallet.PendingTxs != curr.Wallet.PendingTxs {
			a.pushEventLocked(now, "wallet", "Pending tx count changed", fmt.Sprintf("%d -> %d", prev.Wallet.PendingTxs, curr.Wallet.PendingTxs))
		}
	}
}

func (a *App) pushEventLocked(now time.Time, category, title, description string) {
	entry := DashboardEvent{
		Time:        now.Format("15:04:05"),
		Category:    category,
		Title:       title,
		Description: description,
	}
	a.events = append([]DashboardEvent{entry}, a.events...)
	if len(a.events) > 24 {
		a.events = a.events[:24]
	}
}

func (a *App) stopManagedBackend() {
	a.mu.Lock()
	cmd := a.backendCmd
	a.backendCmd = nil
	a.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Kill()
}

func (a *App) resetBackendLogs() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.backendLogs = a.backendLogs[:0]
}

func (a *App) appendBackendLogChunk(chunk string) {
	normalized := strings.ReplaceAll(chunk, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		a.backendLogs = append(a.backendLogs, trimmed)
		if len(a.backendLogs) > 48 {
			a.backendLogs = a.backendLogs[len(a.backendLogs)-48:]
		}
	}
}

func (a *App) backendLogSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.backendLogs...)
}

func normalizeChainMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "archive":
		return "archive"
	case "prune", "pruned":
		return "pruned"
	default:
		return ""
	}
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	for index, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func mergeSettings(base DashboardSettings, input DashboardSettings) DashboardSettings {
	if input.APIBase != "" {
		base.APIBase = input.APIBase
	}
	base.ExplorerBaseURL = strings.TrimSpace(input.ExplorerBaseURL)
	base.EnableIndexer = input.EnableIndexer
	base.DatabaseURL = strings.TrimSpace(input.DatabaseURL)
	return base
}

func (a *App) loadSettingsFromDisk() {
	if a.settingsPath == "" {
		return
	}

	raw, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return
	}

	var saved DashboardSettings
	if err := json.Unmarshal(raw, &saved); err != nil {
		return
	}

	a.settings = mergeSettings(a.settings, saved)
}

func (a *App) saveSettingsToDisk(settings DashboardSettings) {
	if a.settingsPath == "" {
		return
	}

	if err := os.MkdirAll(filepath.Dir(a.settingsPath), 0755); err != nil {
		return
	}

	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}

	_ = os.WriteFile(a.settingsPath, raw, 0644)
}

func evaluateIndexerStatus(settings DashboardSettings) IndexerStatusView {
	if !settings.EnableIndexer {
		return IndexerStatusView{
			Enabled:      false,
			Requested:    false,
			Reachable:    false,
			Running:      false,
			UsingDefault: strings.TrimSpace(settings.DatabaseURL) == "",
			NeedsRestart: false,
			Status:       "Disabled",
			Message:      "Indexer is turned off for the next node launch.",
		}
	}

	databaseURL := strings.TrimSpace(settings.DatabaseURL)
	usingDefault := databaseURL == ""
	host := "127.0.0.1"
	port := "5432"
	if databaseURL == "" {
		address, reachable := probePostgresAddress(host, port)
		message := fmt.Sprintf("Backend default PostgreSQL endpoint %s is not reachable yet.", address)
		status := "DB unavailable"
		if reachable {
			status = "Ready"
			message = fmt.Sprintf("PostgreSQL is reachable at %s. The backend will use its built-in database settings.", address)
		}

		return IndexerStatusView{
			Enabled:      true,
			Requested:    false,
			Reachable:    reachable,
			Running:      false,
			UsingDefault: true,
			NeedsRestart: false,
			Status:       status,
			Message:      message,
		}
	}

	var err error
	host, port, err = extractPostgresHostPort(databaseURL)
	if err != nil {
		return IndexerStatusView{
			Enabled:      true,
			Requested:    false,
			Reachable:    false,
			Running:      false,
			UsingDefault: usingDefault,
			NeedsRestart: false,
			Status:       "DB unavailable",
			Message:      err.Error(),
		}
	}

	address, reachable := probePostgresAddress(host, port)
	if !reachable {
		return IndexerStatusView{
			Enabled:      true,
			Requested:    false,
			Reachable:    false,
			Running:      false,
			UsingDefault: usingDefault,
			NeedsRestart: false,
			Status:       "DB unavailable",
			Message:      fmt.Sprintf("Unable to reach PostgreSQL at %s.", address),
		}
	}

	return IndexerStatusView{
		Enabled:      true,
		Requested:    false,
		Reachable:    true,
		Running:      false,
		UsingDefault: usingDefault,
		NeedsRestart: false,
		Status:       "Ready",
		Message:      fmt.Sprintf("PostgreSQL is reachable at %s.", address),
	}
}

func mergeIndexerRuntimeStatus(settings DashboardSettings, runtime IndexerStatusView) IndexerStatusView {
	runtime.Enabled = settings.EnableIndexer

	if settings.EnableIndexer != runtime.Requested {
		runtime.NeedsRestart = true
		runtime.Status = "Restart required"
		if settings.EnableIndexer {
			runtime.Message = "Indexer is enabled for the next node launch, but the current node session was started without it. Restart the node to apply the saved setting."
		} else if runtime.Running {
			runtime.Message = "Indexer is still running for the current node session. Restart the node to turn it off."
		} else {
			runtime.Message = "Indexer is still configured for the current node session. Restart the node to apply the saved off setting."
		}
		return runtime
	}

	runtime.NeedsRestart = false
	return runtime
}

func probePostgresAddress(host, port string) (string, bool) {
	address := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return address, false
	}
	_ = conn.Close()
	return address, true
}

func extractPostgresHostPort(databaseURL string) (string, string, error) {
	raw := strings.TrimSpace(databaseURL)
	if raw == "" {
		return "", "", errors.New("database URL is empty")
	}

	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", "", errors.New("database URL is not a valid PostgreSQL URL")
		}
		host := parsed.Hostname()
		if host == "" {
			host = "localhost"
		}
		port := parsed.Port()
		if port == "" {
			port = "5432"
		}
		return host, port, nil
	}

	host := "localhost"
	port := "5432"
	for _, part := range strings.Fields(raw) {
		switch {
		case strings.HasPrefix(part, "host="):
			host = strings.Trim(strings.TrimPrefix(part, "host="), `"'`)
		case strings.HasPrefix(part, "port="):
			port = strings.Trim(strings.TrimPrefix(part, "port="), `"'`)
		}
	}

	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	return host, port, nil
}

func (a *App) apiReachable() bool {
	a.mu.Lock()
	apiBase := a.settings.APIBase
	a.mu.Unlock()

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	resp, err := client.Get(apiBase + "/api/dashboard/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < http.StatusBadRequest
}

func (a *App) resolveBackendExecutable() (string, error) {
	a.mu.Lock()
	if a.backendPath != "" {
		path := a.backendPath
		a.mu.Unlock()
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		a.mu.Lock()
		a.backendPath = ""
		a.mu.Unlock()
	} else {
		a.mu.Unlock()
	}

	executableNames := backendExecutableNames()
	for _, candidate := range backendExecutableCandidates(executableNames) {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		if resolved, err := filepath.Abs(candidate); err == nil {
			candidate = resolved
		}

		a.mu.Lock()
		a.backendPath = candidate
		a.mu.Unlock()
		return candidate, nil
	}

	return "", fmt.Errorf(
		"%s not found; expected it on PATH, next to the dashboard binary, or in a nearby mycoin - codex folder",
		strings.Join(executableNames, " or "),
	)
}

func backendExecutableNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"mycoin-node.exe", "mycoin-node"}
	}
	return []string{"mycoin-node", "mycoin-node.exe"}
}

func backendExecutableCandidates(names []string) []string {
	candidates := make([]string, 0, 32)
	seen := make(map[string]struct{}, 32)
	addPath := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}

		cleaned := filepath.Clean(path)
		if _, exists := seen[cleaned]; exists {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}

	if envPath := strings.TrimSpace(os.Getenv("MYCOIN_NODE_PATH")); envPath != "" {
		addPath(envPath)
	}

	for _, name := range names {
		if resolved, err := exec.LookPath(name); err == nil {
			addPath(resolved)
		}
	}

	for _, root := range backendSearchRoots() {
		for _, name := range names {
			addPath(filepath.Join(root, name))
			addPath(filepath.Join(root, "mycoin - codex", name))
			addPath(filepath.Join(root, "mycoin-codex", name))
		}
	}

	return candidates
}

func backendSearchRoots() []string {
	roots := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	addRoot := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}

		cleaned := filepath.Clean(path)
		if _, exists := seen[cleaned]; exists {
			return
		}
		seen[cleaned] = struct{}{}
		roots = append(roots, cleaned)
	}
	addRootAndParents := func(path string, levels int) {
		current := strings.TrimSpace(path)
		for level := 0; level <= levels && current != ""; level++ {
			addRoot(current)
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	if executablePath, err := os.Executable(); err == nil {
		addRootAndParents(filepath.Dir(executablePath), 3)
	}
	if workingDir, err := os.Getwd(); err == nil {
		addRootAndParents(workingDir, 3)
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		addRoot(homeDir)
		addRoot(filepath.Join(homeDir, "Downloads"))
		addRoot(filepath.Join(homeDir, "Documents"))
		addRoot(filepath.Join(homeDir, "Documents", "Playground"))
	}

	return roots
}

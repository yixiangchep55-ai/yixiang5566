//go:build desktopapp
// +build desktopapp

package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	defaultDashboardAPIURL = "http://localhost:8080"
	defaultPollInterval    = 3 * time.Second
	maxSessionLogLines     = 500
)

type Options struct {
	BaseURL        string
	NodeExecutable string
	AutoStartNode  bool
}

type dashboardNodeStatus struct {
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
	MiningAddress string `json:"mining_address"`
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

type dashboardStatusResponse struct {
	Node             *dashboardNodeStatus      `json:"node"`
	Wallet           *dashboardWalletStatus    `json:"wallet"`
	SpendableBalance float64                   `json:"spendable_balance"`
	PendingAmount    float64                   `json:"pending_amount"`
	Activity         []dashboardWalletActivity `json:"activity"`
	Timestamp        string                    `json:"timestamp"`
}

type sessionLog struct {
	At       time.Time
	Category string
	Level    string
	Message  string
}

type UI struct {
	app fyne.App
	win fyne.Window

	opts   Options
	client *http.Client

	mu         sync.Mutex
	status     *dashboardStatusResponse
	lastStatus *dashboardStatusResponse
	lastFetch  time.Time
	connected  bool
	logs       []sessionLog
	logPaused  bool

	nodeCmd      *exec.Cmd
	selectedMode string
	selectedTab  string

	tabs *container.AppTabs

	connectPills []*widget.Label
	connectBoxes []*canvas.Rectangle

	summaryState   *widget.Label
	summaryHeight  *widget.Entry
	summarySync    *widget.Entry
	summaryPeers   *widget.Entry
	summaryMempool *widget.Entry
	summaryOrphans *widget.Entry
	summaryHash    *widget.Entry
	summaryMiner   *widget.Entry
	summaryMode    *widget.Entry

	metricHeight  *widget.Label
	metricPeers   *widget.Label
	metricMempool *widget.Label
	metricBalance *widget.Label

	connEndpoint *widget.Entry
	connWallet   *widget.Entry
	connBadge    *widget.Label
	connUpdated  *widget.Label

	overviewRecent *widget.TextGrid

	walletDefaultAddr *widget.Entry
	walletAvailable   *widget.Entry
	walletPending     *widget.Entry
	walletPendingTx   *widget.Entry

	addressRows     [][]string
	addressTable    *widget.Table
	selectedAddress *widget.Entry

	sendTo       *widget.Entry
	sendAmount   *widget.Entry
	sendFee      *widget.Entry
	sendPassword *widget.Entry
	sendStatus   *widget.Label

	pendingRows  [][]string
	pendingTable *widget.Table

	logEventType   *widget.Select
	logLevel       *widget.Select
	logAutoScroll  *widget.Check
	logOnlyChanges *widget.Check
	logMaxLines    *widget.Entry
	logGrid        *widget.TextGrid
	countHeight    *widget.Entry
	countSync      *widget.Entry
	countPeers     *widget.Entry
	countBalance   *widget.Entry
	countWarnings  *widget.Entry

	settingsAPI      *widget.Entry
	settingsNodeExe  *widget.Entry
	settingsDataDir  *widget.Entry
	settingsAuto     *widget.Check
	settingsLaunch   *widget.RadioGroup
	settingsNetwork  *widget.Select
	settingsWallet   *widget.Select
	settingsMode     *widget.Entry
	settingsHash     *widget.Entry
	settingsWalletID *widget.Entry
}

func NewWithOptions(opts Options) *UI {
	if strings.TrimSpace(opts.BaseURL) == "" {
		opts.BaseURL = defaultDashboardAPIURL
	}
	if strings.TrimSpace(opts.NodeExecutable) == "" {
		opts.NodeExecutable = resolveNodeExecutable()
	}

	app := fyneapp.NewWithID("yicoin.dashboard")
	app.Settings().SetTheme(newDashboardTheme())

	win := app.NewWindow("YiCoin Dashboard")
	win.Resize(fyne.NewSize(1024, 700))
	win.SetFixedSize(true)

	ui := &UI{
		app:          app,
		win:          win,
		opts:         opts,
		client:       &http.Client{Timeout: 4 * time.Second},
		selectedMode: "archive",
		selectedTab:  "Overview",
	}
	ui.build()
	return ui
}

func (ui *UI) Run() {
	go ui.bootstrap()
	ui.win.ShowAndRun()
}

func (ui *UI) build() {
	title := widget.NewLabelWithStyle("Node Dashboard", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	subtitle := widget.NewLabel("Fyne desktop layout")
	header := container.NewBorder(nil, nil, nil, subtitle, title)

	ui.tabs = container.NewAppTabs(
		container.NewTabItem("Overview", ui.buildOverviewTab()),
		container.NewTabItem("Wallet", ui.buildWalletTab()),
		container.NewTabItem("Logs", ui.buildLogsTab()),
		container.NewTabItem("Settings", ui.buildSettingsTab()),
	)
	ui.tabs.SetTabLocation(container.TabLocationTop)
	ui.tabs.OnSelected = func(item *container.TabItem) {
		ui.selectedTab = item.Text
		if item.Text == "Wallet" {
			ui.refreshWalletTables()
		}
		if item.Text == "Logs" {
			ui.renderLogs()
		}
	}

	ui.win.SetContent(container.NewVBox(
		container.NewPadded(header),
		ui.tabs,
	))
}

func (ui *UI) buildOverviewTab() fyne.CanvasObject {
	ui.summaryState = widget.NewLabel("Unknown")
	ui.summaryHeight = readonlyValue("")
	ui.summarySync = readonlyValue("")
	ui.summaryPeers = readonlyValue("")
	ui.summaryMempool = readonlyValue("")
	ui.summaryOrphans = readonlyValue("")
	ui.summaryHash = readonlyValue("")
	ui.summaryMiner = readonlyValue("")
	ui.summaryMode = readonlyValue("")

	nodeSummaryForm := compactForm(
		widget.NewFormItem("Current height", ui.summaryHeight),
		widget.NewFormItem("Sync status", ui.summarySync),
		widget.NewFormItem("Peers", ui.summaryPeers),
		widget.NewFormItem("Mempool", ui.summaryMempool),
		widget.NewFormItem("Orphans", ui.summaryOrphans),
		widget.NewFormItem("Best hash", ui.summaryHash),
		widget.NewFormItem("Mining address", ui.summaryMiner),
		widget.NewFormItem("Wallet mode", ui.summaryMode),
	)
	nodeSummary := widget.NewCard("Node Summary", "Compact status panel for the local node", container.NewVBox(
		statusRow(ui.summaryState),
		nodeSummaryForm,
	))

	ui.metricHeight = valueLabel("0")
	ui.metricPeers = valueLabel("0")
	ui.metricMempool = valueLabel("0")
	ui.metricBalance = valueLabel("0.00")
	metrics := container.NewGridWithColumns(4,
		simpleMetricCard("Height", ui.metricHeight, "stable"),
		simpleMetricCard("Peers", ui.metricPeers, "healthy"),
		simpleMetricCard("Mempool", ui.metricMempool, "stable"),
		simpleMetricCard("Balance", ui.metricBalance, "watch"),
	)

	ui.connEndpoint = readonlyValue("")
	ui.connWallet = readonlyValue("")
	ui.connBadge = widget.NewLabel("offline")
	ui.connUpdated = widget.NewLabel("-")
	connectionForm := compactForm(
		widget.NewFormItem("API endpoint", ui.connEndpoint),
		widget.NewFormItem("Wallet", ui.connWallet),
	)
	connection := widget.NewCard("Connection & Wallet", "Read-only details that fit a simple form layout", container.NewVBox(
		connectionForm,
		container.NewHBox(statusPill(ui.connBadge), widget.NewLabel("Last refresh"), ui.connUpdated),
	))

	ui.overviewRecent = widget.NewTextGrid()
	recent := widget.NewCard("Recent State Changes", "A read-only list that maps well to widget.List or MultiLineEntry", container.NewPadded(ui.overviewRecent))

	right := container.NewBorder(container.NewVBox(metrics, connection), nil, nil, nil, recent)
	left := nodeSummary
	main := container.NewHSplit(left, right)
	main.Offset = 0.25
	return pageScaffold(ui.buildQuickActions(), main)
}

func (ui *UI) buildWalletTab() fyne.CanvasObject {
	ui.walletDefaultAddr = readonlyValue("")
	ui.walletAvailable = readonlyValue("0.00")
	ui.walletPending = readonlyValue("0.00")
	ui.walletPendingTx = readonlyValue("0")

	walletSummaryForm := compactForm(
		widget.NewFormItem("Default addr", ui.walletDefaultAddr),
		widget.NewFormItem("Available", ui.walletAvailable),
		widget.NewFormItem("Pending", ui.walletPending),
		widget.NewFormItem("Pending txs", ui.walletPendingTx),
	)
	walletSummary := widget.NewCard("Wallet Summary", "Keep the left column fixed so values stay readable", container.NewVBox(
		statusPill(widget.NewLabel("Connected")),
		walletSummaryForm,
	))

	addressActions := widget.NewCard("Address Actions", "Simple buttons only; no modal-heavy flow", container.NewVBox(
		widget.NewButton("Copy default address", func() {
			ui.win.Clipboard().SetContent(ui.walletDefaultAddr.Text)
		}),
		widget.NewButton("Generate new address", func() {
			ui.appendLog("wallet", "INFO", "generate address is not implemented in this build")
		}),
		widget.NewButton("Refresh balance", func() { ui.fetchStatus(true) }),
	))

	left := container.NewBorder(walletSummary, nil, nil, nil, addressActions)

	ui.addressRows = [][]string{{"-", "default", "0.00"}}
	ui.addressTable = simpleTable([]string{"Address", "Label", "Balance"}, &ui.addressRows)
	ui.selectedAddress = readonlyValue("")
	btnCopy := widget.NewButton("Copy selected address", func() {
		if ui.selectedAddress.Text != "" {
			ui.win.Clipboard().SetContent(ui.selectedAddress.Text)
		}
	})
	btnCopy.Importance = widget.HighImportance
	addressBottom := container.NewVBox(
		btnCopy,
		compactForm(widget.NewFormItem("Selected", ui.selectedAddress)),
	)
	addresses := widget.NewCard("Addresses", "A compact table with copy/select actions",
		container.NewBorder(nil, addressBottom, nil, nil, ui.addressTable),
	)

	ui.sendTo = widget.NewEntry()
	ui.sendAmount = widget.NewEntry()
	ui.sendFee = widget.NewEntry()
	ui.sendPassword = widget.NewPasswordEntry()
	ui.sendStatus = widget.NewLabel("")
	ui.sendFee.SetText("0.01")
	sendForm := compactForm(
		widget.NewFormItem("To address", ui.sendTo),
		widget.NewFormItem("Amount", ui.sendAmount),
		widget.NewFormItem("Fee", ui.sendFee),
		widget.NewFormItem("Password", ui.sendPassword),
	)
	btnSend := widget.NewButton("Send transaction", func() { ui.sendTransaction() })
	btnSend.Importance = widget.HighImportance
	sendCard := widget.NewCard("Send Transaction", "A straight widget.Form + Buttons layout", container.NewVBox(
		sendForm,
		container.NewHBox(
			btnSend,
			widget.NewButton("Clear", func() {
				ui.sendTo.SetText("")
				ui.sendAmount.SetText("")
				ui.sendPassword.SetText("")
				ui.sendStatus.SetText("")
			}),
		),
		ui.sendStatus,
	))

	topRight := container.NewHSplit(addresses, container.NewVBox(sendCard))
	topRight.Offset = 0.58

	ui.pendingRows = [][]string{{"-", "0.00", "0", "pending"}}
	ui.pendingTable = simpleTable([]string{"Txid", "Amount", "Conf.", "State"}, &ui.pendingRows)
	pendingCard := widget.NewCard("Pending Transactions", "Use widget.Table with four short columns",
		container.NewBorder(nil, nil, nil, nil, ui.pendingTable),
	)

	right := container.NewBorder(topRight, nil, nil, nil, pendingCard)
	main := container.NewHSplit(left, right)
	main.Offset = 0.22
	return pageScaffold(ui.buildQuickActions(), main)
}

func (ui *UI) buildLogsTab() fyne.CanvasObject {
	ui.logEventType = widget.NewSelect([]string{"All", "height", "sync", "peers", "mempool", "wallet", "api"}, func(string) { ui.renderLogs() })
	ui.logEventType.SetSelected("All")
	ui.logLevel = widget.NewSelect([]string{"All", "INFO", "WARN", "ERROR"}, func(string) { ui.renderLogs() })
	ui.logLevel.SetSelected("All")
	ui.logAutoScroll = widget.NewCheck("Auto scroll", func(bool) { ui.renderLogs() })
	ui.logAutoScroll.SetChecked(true)
	ui.logOnlyChanges = widget.NewCheck("Only changes", func(bool) { ui.renderLogs() })
	ui.logMaxLines = widget.NewEntry()
	ui.logMaxLines.SetText("500")
	ui.logMaxLines.OnChanged = func(string) { ui.renderLogs() }

	ui.countHeight = readonlyValue("0")
	ui.countSync = readonlyValue("0")
	ui.countPeers = readonlyValue("0")
	ui.countBalance = readonlyValue("0")
	ui.countWarnings = readonlyValue("0")

	filterForm := compactForm(
		widget.NewFormItem("Event type", ui.logEventType),
		widget.NewFormItem("Max lines", ui.logMaxLines),
		widget.NewFormItem("Level", ui.logLevel),
	)
	filterCard := widget.NewCard("Filters", "All controls map to stock Fyne widgets", container.NewVBox(
		filterForm,
		ui.logAutoScroll,
		ui.logOnlyChanges,
	))

	quickCounts := widget.NewCard("Quick Counts", "A small summary refreshed every poll", compactForm(
		widget.NewFormItem("Height changes", ui.countHeight),
		widget.NewFormItem("Sync changes", ui.countSync),
		widget.NewFormItem("Peer changes", ui.countPeers),
		widget.NewFormItem("Balance changes", ui.countBalance),
		widget.NewFormItem("Warnings", ui.countWarnings),
	))

	ui.logGrid = widget.NewTextGrid()
	logButtons := container.NewHBox(
		widget.NewButton("Clear", func() {
			ui.mu.Lock()
			ui.logs = nil
			ui.mu.Unlock()
			ui.renderLogs()
		}),
		widget.NewButton("Export", func() { ui.exportLogs() }),
		widget.NewButton("Pause", func() { ui.logPaused = !ui.logPaused }),
	)
	logScroll := container.NewScroll(ui.logGrid)
	logScroll.SetMinSize(fyne.NewSize(400, 320))
	sessionLog := widget.NewCard("Session Log", "Best rendered as a read-only MultiLineEntry inside a scroll container",
		container.NewBorder(logButtons, nil, nil, nil, logScroll),
	)

	left := container.NewBorder(filterCard, nil, nil, nil, quickCounts)
	main := container.NewHSplit(left, sessionLog)
	main.Offset = 0.22
	return pageScaffold(ui.buildQuickActions(), main)
}

func (ui *UI) buildSettingsTab() fyne.CanvasObject {
	ui.settingsAPI = widget.NewEntry()
	ui.settingsAPI.SetText(ui.opts.BaseURL)
	ui.settingsNodeExe = widget.NewEntry()
	ui.settingsNodeExe.SetText(ui.opts.NodeExecutable)
	ui.settingsDataDir = widget.NewEntry()
	ui.settingsDataDir.SetText(filepath.Dir(ui.opts.NodeExecutable))
	ui.settingsAuto = widget.NewCheck("Auto-start node when API is offline", nil)
	ui.settingsAuto.SetChecked(ui.opts.AutoStartNode)
	ui.settingsLaunch = widget.NewRadioGroup([]string{"archive", "pruned"}, nil)
	ui.settingsLaunch.Horizontal = true
	ui.settingsLaunch.SetSelected(ui.selectedMode)
	ui.settingsNetwork = widget.NewSelect([]string{"mainnet"}, nil)
	ui.settingsNetwork.SetSelected("mainnet")
	ui.settingsWallet = widget.NewSelect([]string{"default"}, nil)
	ui.settingsWallet.SetSelected("default")

	connectionForm := compactForm(
		widget.NewFormItem("API endpoint", ui.settingsAPI),
		widget.NewFormItem("Node executable", ui.settingsNodeExe),
		widget.NewFormItem("Data directory", ui.settingsDataDir),
		widget.NewFormItem("Network mode", ui.settingsNetwork),
		widget.NewFormItem("Wallet profile", ui.settingsWallet),
	)
	connectionStartup := widget.NewCard("Connection & Startup", "Use a form layout; avoid nested custom panels", container.NewVBox(
		connectionForm,
		ui.settingsAuto,
		container.NewHBox(
			widget.NewButton("Test connection", func() { ui.fetchStatus(true) }),
			widget.NewButton("Browse executable", func() { ui.pickExecutable() }),
			func() fyne.CanvasObject {
				btnSave := widget.NewButton("Save settings", func() { ui.saveSettings() })
				btnSave.Importance = widget.HighImportance
				return btnSave
			}(),
		),
	))

	ui.settingsMode = readonlyValue("")
	ui.settingsHash = readonlyValue("")
	ui.settingsWalletID = readonlyValue("")

	connectedAPI := readonlyValue(parseURLHost(ui.opts.BaseURL))
	currentSummary := widget.NewCard("Current Summary", "Small read-only labels for live configuration state", compactForm(
		widget.NewFormItem("Connected API", connectedAPI),
		widget.NewFormItem("Node mode", ui.settingsMode),
		widget.NewFormItem("Wallet", ui.settingsWalletID),
		widget.NewFormItem("Best hash", ui.settingsHash),
	))
	launchOptions := widget.NewCard("Launch Options", "These controls are still realistic in plain Fyne", container.NewVBox(
		widget.NewLabel("Startup behavior"),
		ui.settingsLaunch,
		widget.NewLabel("Open on launch"),
		widget.NewRadioGroup([]string{"Overview", "Wallet", "Logs", "Settings"}, func(selected string) {
			if selected != "" {
				ui.selectedTab = selected
			}
		}),
	))

	main := container.NewHSplit(
		container.NewVBox(connectionStartup),
		container.NewVBox(currentSummary, launchOptions),
	)
	main.Offset = 0.50
	return pageScaffold(ui.buildQuickActions(), main)
}

func pageScaffold(actionBar fyne.CanvasObject, content fyne.CanvasObject) fyne.CanvasObject {
	topBar := container.NewVBox(actionBar, widget.NewSeparator())
	return container.NewPadded(container.NewBorder(topBar, nil, nil, nil, content))
}

func compactForm(items ...*widget.FormItem) *widget.Form {
	form := widget.NewForm(items...)
	return form
}

func (ui *UI) buildQuickActions() fyne.CanvasObject {
	refreshBtn := widget.NewButton("Refresh", func() { ui.fetchStatus(true) })
	openDirBtn := widget.NewButton("Open data dir", func() { ui.openDataDir() })
	reconnectBtn := widget.NewButton("Reconnect API", func() { ui.fetchStatus(true) })

	label := widget.NewLabel("Disconnected")
	box := canvas.NewRectangle(theme.Color(theme.ColorNameError))
	box.CornerRadius = 14
	box.SetMinSize(fyne.NewSize(160, 28))
	ui.connectPills = append(ui.connectPills, label)
	ui.connectBoxes = append(ui.connectBoxes, box)

	badge := container.NewStack(box, container.NewCenter(label))
	return container.NewBorder(nil, nil, nil, badge, container.NewHBox(
		widget.NewLabel("Quick actions"),
		refreshBtn,
		openDirBtn,
		reconnectBtn,
	))
}

func readonlyValue(text string) *widget.Entry {
	e := widget.NewEntry()
	e.SetText(text)
	e.Disable()
	return e
}

func valueLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func statusPill(label *widget.Label) fyne.CanvasObject {
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameSuccess))
	bg.CornerRadius = 12
	bg.SetMinSize(fyne.NewSize(82, 26))
	return container.NewStack(bg, container.NewCenter(label))
}

func statusRow(state *widget.Label) fyne.CanvasObject {
	return container.NewHBox(widget.NewLabel("Running"), layout.NewSpacer(), statusPill(state))
}

func formRow(label string, value fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, widget.NewLabel(label), nil, value)
}

func simpleMetricCard(title string, value *widget.Label, badge string) fyne.CanvasObject {
	return widget.NewCard(title, badge, value)
}

func simpleTable(headers []string, rows *[][]string) *widget.Table {
	table := widget.NewTable(
		func() (int, int) { return len(*rows) + 1, len(headers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			l := obj.(*widget.Label)
			if id.Row == 0 {
				l.SetText(headers[id.Col])
				l.TextStyle = fyne.TextStyle{Bold: true}
				return
			}
			l.TextStyle = fyne.TextStyle{}
			row := id.Row - 1
			if row < len(*rows) && id.Col < len((*rows)[row]) {
				l.SetText((*rows)[row][id.Col])
			} else {
				l.SetText("")
			}
		},
	)
	table.SetColumnWidth(0, 260)
	table.SetColumnWidth(1, 120)
	table.SetColumnWidth(2, 120)
	table.SetColumnWidth(3, 120)
	return table
}

func (ui *UI) bootstrap() {
	if ui.ensureAPIReady() {
		ui.fetchStatus(true)
		ui.startPolling()
		return
	}
	if !ui.opts.AutoStartNode {
		ui.appendLog("api", "WARN", "dashboard started without a reachable local API")
		return
	}
	mode, ok := ui.chooseLaunchMode()
	if !ok {
		ui.appendLog("api", "WARN", "launch cancelled")
		return
	}
	ui.selectedMode = mode
	if err := ui.launchNode(mode); err != nil {
		ui.appendLog("api", "ERROR", err.Error())
		return
	}
	if ui.ensureAPIReady() {
		ui.fetchStatus(true)
		ui.startPolling()
	}
}

func (ui *UI) startPolling() {
	ticker := time.NewTicker(defaultPollInterval)
	go func() {
		for range ticker.C {
			ui.fetchStatus(false)
		}
	}()
}

func (ui *UI) ensureAPIReady() bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := ui.getStatus(); err == nil {
			return true
		}
		time.Sleep(700 * time.Millisecond)
	}
	return false
}

func (ui *UI) chooseLaunchMode() (string, bool) {
	result := make(chan string, 1)
	cancelled := make(chan struct{}, 1)
	fyne.Do(func() {
		mode := widget.NewRadioGroup([]string{"archive", "pruned"}, nil)
		mode.Horizontal = true
		mode.SetSelected(ui.selectedMode)
		body := container.NewVBox(
			widget.NewLabel("Choose how the dashboard should launch your local node."),
			mode,
		)
		d := dialog.NewCustomConfirm("Start Local Node", "Launch Node", "Cancel", body, func(ok bool) {
			if ok {
				result <- mode.Selected
			} else {
				cancelled <- struct{}{}
			}
		}, ui.win)
		d.Resize(fyne.NewSize(420, 200))
		d.Show()
	})
	select {
	case mode := <-result:
		if mode == "" {
			mode = "archive"
		}
		return mode, true
	case <-cancelled:
		return "", false
	}
}

func (ui *UI) launchNode(mode string) error {
	nodeExe := strings.TrimSpace(ui.opts.NodeExecutable)
	if nodeExe == "" {
		return fmt.Errorf("node executable is not configured")
	}
	cmd := exec.Command(nodeExe, "-mode="+mode)
	prepareNodeCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch local node: %w", err)
	}
	ui.nodeCmd = cmd
	ui.appendLog("api", "INFO", "local node launched in "+mode+" mode")
	return nil
}

func (ui *UI) getStatus() (*dashboardStatusResponse, error) {
	resp, err := ui.client.Get(strings.TrimRight(ui.opts.BaseURL, "/") + "/api/dashboard/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dashboard API returned %s", resp.Status)
	}
	var payload dashboardStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (ui *UI) fetchStatus(force bool) {
	status, err := ui.getStatus()
	if err != nil {
		ui.mu.Lock()
		ui.connected = false
		ui.mu.Unlock()
		fyne.Do(func() { ui.updateConnectionBadge(false) })
		if force {
			ui.appendLog("api", "WARN", "local API is unavailable")
		}
		return
	}

	ui.mu.Lock()
	prev := ui.status
	ui.lastStatus = prev
	ui.status = status
	ui.connected = true
	ui.lastFetch = time.Now()
	ui.mu.Unlock()

	fyne.Do(func() {
		ui.applyStatus(status)
		ui.updateConnectionBadge(true)
	})
	ui.recordStatusChanges(prev, status)
}

func (ui *UI) sendTransaction() {
	to := strings.TrimSpace(ui.sendTo.Text)
	amount := parseFloat(ui.sendAmount.Text)
	fee := parseFloat(ui.sendFee.Text)
	if to == "" || amount <= 0 {
		ui.sendStatus.SetText("Recipient and positive amount are required.")
		return
	}
	payload := map[string]interface{}{"to": to, "amount": amount, "fee": fee}
	body, _ := json.Marshal(payload)
	resp, err := ui.client.Post(strings.TrimRight(ui.opts.BaseURL, "/")+"/api/transaction", "application/json", bytes.NewReader(body))
	if err != nil {
		ui.sendStatus.SetText("Failed to reach local API.")
		return
	}
	defer resp.Body.Close()
	var parsed map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	if resp.StatusCode != http.StatusOK {
		ui.sendStatus.SetText(fmt.Sprintf("%v", parsed["error"]))
		return
	}
	ui.sendStatus.SetText(fmt.Sprintf("Broadcasted tx %v", parsed["txid"]))
	ui.appendLog("wallet", "INFO", fmt.Sprintf("broadcasted transaction %v", parsed["txid"]))
	ui.fetchStatus(true)
}

func (ui *UI) applyStatus(status *dashboardStatusResponse) {
	if status == nil {
		return
	}
	node := status.Node
	if node == nil {
		node = &dashboardNodeStatus{}
	}
	wallet := status.Wallet
	if wallet == nil {
		wallet = &dashboardWalletStatus{}
	}

	nowText := "-"
	if !ui.lastFetch.IsZero() {
		nowText = ui.lastFetch.Format("15:04:05")
	}

	ui.summaryState.SetText(summaryState(node))
	ui.summaryHeight.SetText(fmt.Sprintf("%d", node.BestHeight))
	ui.summarySync.SetText(syncSummary(node))
	ui.summaryPeers.SetText(fmt.Sprintf("%d", node.PeerCount))
	ui.summaryMempool.SetText(fmt.Sprintf("%d", node.MempoolCount))
	ui.summaryOrphans.SetText(fmt.Sprintf("%d", node.OrphanCount))
	ui.summaryHash.SetText(shortHash(node.BestHash))
	ui.summaryMiner.SetText(shortAddress(node.MiningAddress))
	ui.summaryMode.SetText(orDash(node.Mode))

	ui.metricHeight.SetText(fmt.Sprintf("%d", node.BestHeight))
	ui.metricPeers.SetText(fmt.Sprintf("%d", node.PeerCount))
	ui.metricMempool.SetText(fmt.Sprintf("%d", node.MempoolCount))
	ui.metricBalance.SetText(formatCoins(wallet.Balance))

	ui.connEndpoint.SetText(parseURLHost(ui.opts.BaseURL))
	ui.connWallet.SetText(shortAddress(wallet.Address))
	ui.connBadge.SetText(connectionText(ui.connected))
	ui.connUpdated.SetText(nowText)
	ui.overviewRecent.SetText(ui.renderRecentText(8))

	ui.walletDefaultAddr.SetText(wallet.Address)
	ui.walletAvailable.SetText(formatCoins(status.SpendableBalance))
	ui.walletPending.SetText(formatCoins(status.PendingAmount))
	ui.walletPendingTx.SetText(fmt.Sprintf("%d", wallet.PendingTxs))

	rows := [][]string{}
	if wallet.Address != "" {
		rows = append(rows, []string{shortAddress(wallet.Address), "default", formatCoins(wallet.Balance)})
		ui.selectedAddress.SetText(wallet.Address)
	}
	if node.MiningAddress != "" && node.MiningAddress != wallet.Address {
		rows = append(rows, []string{shortAddress(node.MiningAddress), "mining", formatCoins(wallet.Balance)})
	}
	if len(rows) == 0 {
		rows = [][]string{{"-", "default", "0.00"}}
	}
	ui.addressRows = rows
	ui.addressTable.Refresh()

	pending := make([][]string, 0, len(status.Activity))
	for _, item := range status.Activity {
		pending = append(pending, []string{shortHash(item.TxID), formatSignedCoins(item.Amount), fmt.Sprintf("%d", item.Confirmations), strings.ToLower(item.Status)})
	}
	if len(pending) == 0 {
		pending = [][]string{{"-", "0.00", "0", "pending"}}
	}
	ui.pendingRows = pending
	ui.pendingTable.Refresh()

	ui.countHeight.SetText(fmt.Sprintf("%d", ui.countCategory("height")))
	ui.countSync.SetText(fmt.Sprintf("%d", ui.countCategory("sync")))
	ui.countPeers.SetText(fmt.Sprintf("%d", ui.countCategory("peers")))
	ui.countBalance.SetText(fmt.Sprintf("%d", ui.countCategory("wallet")))
	ui.countWarnings.SetText(fmt.Sprintf("%d", ui.countLevel("WARN")))
	ui.renderLogs()

	ui.settingsMode.SetText(orDash(node.Mode))
	ui.settingsHash.SetText(shortHash(node.BestHash))
	ui.settingsWalletID.SetText(orDash(wallet.Address))
}

func (ui *UI) refreshWalletTables() {
	if ui.addressTable != nil {
		ui.addressTable.Refresh()
	}
	if ui.pendingTable != nil {
		ui.pendingTable.Refresh()
	}
}

func (ui *UI) updateConnectionBadge(connected bool) {
	text := "Disconnected"
	fill := theme.Color(theme.ColorNameError)
	if connected {
		text = "Connected to local API"
		fill = theme.Color(theme.ColorNameSuccess)
	}
	for _, l := range ui.connectPills {
		l.SetText(text)
	}
	for _, r := range ui.connectBoxes {
		r.FillColor = fill
		r.Refresh()
	}
	if ui.connBadge != nil {
		ui.connBadge.SetText(connectionText(connected))
	}
}

func (ui *UI) recordStatusChanges(prev, curr *dashboardStatusResponse) {
	if curr == nil || curr.Node == nil {
		return
	}
	if prev == nil || prev.Node == nil {
		ui.appendLog("api", "INFO", fmt.Sprintf("connected at height %d", curr.Node.BestHeight))
		return
	}
	if prev.Node.BestHeight != curr.Node.BestHeight {
		ui.appendLog("height", "INFO", fmt.Sprintf("height: %d -> %d", prev.Node.BestHeight, curr.Node.BestHeight))
	}
	if prev.Node.SyncState != curr.Node.SyncState || prev.Node.IsSyncing != curr.Node.IsSyncing {
		ui.appendLog("sync", "INFO", fmt.Sprintf("sync: %s -> %s", syncSummary(prev.Node), syncSummary(curr.Node)))
	}
	if prev.Node.PeerCount != curr.Node.PeerCount {
		ui.appendLog("peers", "INFO", fmt.Sprintf("peers: %d -> %d", prev.Node.PeerCount, curr.Node.PeerCount))
	}
	if prev.Node.MempoolCount != curr.Node.MempoolCount {
		ui.appendLog("mempool", "INFO", fmt.Sprintf("mempool: %d -> %d", prev.Node.MempoolCount, curr.Node.MempoolCount))
	}
	if prev.Node.OrphanCount != curr.Node.OrphanCount {
		ui.appendLog("mempool", "INFO", fmt.Sprintf("orphan: %d -> %d", prev.Node.OrphanCount, curr.Node.OrphanCount))
	}
	if prev.Wallet != nil && curr.Wallet != nil && prev.Wallet.Balance != curr.Wallet.Balance {
		ui.appendLog("wallet", "INFO", fmt.Sprintf("balance: %.2f -> %.2f", prev.Wallet.Balance, curr.Wallet.Balance))
	}
}

func (ui *UI) appendLog(category, level, message string) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if ui.logPaused {
		return
	}
	ui.logs = append(ui.logs, sessionLog{At: time.Now(), Category: category, Level: level, Message: message})
	if len(ui.logs) > maxSessionLogLines {
		ui.logs = ui.logs[len(ui.logs)-maxSessionLogLines:]
	}
	fyne.Do(func() {
		ui.renderLogs()
		if ui.overviewRecent != nil {
			ui.overviewRecent.SetText(ui.renderRecentText(8))
		}
	})
}

func (ui *UI) renderLogs() {
	if ui.logGrid == nil {
		return
	}
	ui.mu.Lock()
	entries := append([]sessionLog(nil), ui.logs...)
	ui.mu.Unlock()

	eventType := "All"
	level := "All"
	onlyChanges := false
	maxLines := 500
	if ui.logEventType != nil && ui.logEventType.Selected != "" {
		eventType = ui.logEventType.Selected
	}
	if ui.logLevel != nil && ui.logLevel.Selected != "" {
		level = ui.logLevel.Selected
	}
	if ui.logOnlyChanges != nil {
		onlyChanges = ui.logOnlyChanges.Checked
	}
	if ui.logMaxLines != nil {
		if n := int(parseFloat(ui.logMaxLines.Text)); n > 0 {
			maxLines = n
		}
	}

	lines := []string{}
	for _, entry := range entries {
		if eventType != "All" && !strings.EqualFold(entry.Category, eventType) {
			continue
		}
		if level != "All" && !strings.EqualFold(entry.Level, level) {
			continue
		}
		if onlyChanges && !strings.Contains(entry.Message, "->") {
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s  %s  %s", entry.At.Format("15:04:05"), strings.ToUpper(entry.Level), entry.Category, entry.Message))
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	ui.logGrid.SetText(strings.Join(lines, "\n"))
}

func (ui *UI) renderRecentText(limit int) string {
	ui.mu.Lock()
	entries := append([]sessionLog(nil), ui.logs...)
	ui.mu.Unlock()
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	lines := []string{}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("[%s] %s", entry.At.Format("15:04:05"), entry.Message))
	}
	return strings.Join(lines, "\n")
}

func (ui *UI) countCategory(category string) int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	count := 0
	for _, entry := range ui.logs {
		if strings.EqualFold(entry.Category, category) {
			count++
		}
	}
	return count
}

func (ui *UI) countLevel(level string) int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	count := 0
	for _, entry := range ui.logs {
		if strings.EqualFold(entry.Level, level) {
			count++
		}
	}
	return count
}

func (ui *UI) exportLogs() {
	ui.mu.Lock()
	lines := make([]string, 0, len(ui.logs))
	for _, entry := range ui.logs {
		lines = append(lines, fmt.Sprintf("[%s] %s %s %s", entry.At.Format(time.RFC3339), entry.Level, entry.Category, entry.Message))
	}
	ui.mu.Unlock()

	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		defer uc.Close()
		_, _ = uc.Write([]byte(strings.Join(lines, "\n")))
	}, ui.win)
	save.SetFileName("dashboard-log.txt")
	save.Show()
}

func (ui *UI) pickExecutable() {
	open := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil || reader == nil {
			return
		}
		defer reader.Close()
		ui.settingsNodeExe.SetText(reader.URI().Path())
	}, ui.win)
	open.Show()
}

func (ui *UI) saveSettings() {
	ui.opts.BaseURL = strings.TrimSpace(ui.settingsAPI.Text)
	ui.opts.NodeExecutable = strings.TrimSpace(ui.settingsNodeExe.Text)
	ui.opts.AutoStartNode = ui.settingsAuto.Checked
	if ui.settingsLaunch.Selected != "" {
		ui.selectedMode = ui.settingsLaunch.Selected
	}
	ui.appendLog("api", "INFO", "dashboard settings updated")
}

func (ui *UI) openDataDir() {
	target := ui.settingsDataDir.Text
	if target == "" {
		target = filepath.Dir(ui.opts.NodeExecutable)
	}
	if target == "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}

func parseFloat(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var out float64
	fmt.Sscanf(raw, "%f", &out)
	return out
}

func parseURLHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	if u.Host != "" {
		return u.Host
	}
	return raw
}

func resolveNodeExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	name := "mycoin-node"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func summaryState(node *dashboardNodeStatus) string {
	if node == nil {
		return "Unavailable"
	}
	return "Running"
}

func syncSummary(node *dashboardNodeStatus) string {
	if node == nil {
		return "-"
	}
	if node.Synced {
		return "synced"
	}
	if node.SyncState != "" {
		return node.SyncState
	}
	if node.IsSyncing {
		return "syncing"
	}
	return "offline"
}

func connectionText(connected bool) string {
	if connected {
		return "connected"
	}
	return "offline"
}

func formatCoins(v float64) string {
	return fmt.Sprintf("%.2f YIC", v)
}

func formatSignedCoins(v float64) string {
	if v > 0 {
		return fmt.Sprintf("+%.2f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) <= 16 {
		return hash
	}
	return hash[:12] + "..." + hash[len(hash)-4:]
}

func shortAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if len(addr) <= 22 {
		return addr
	}
	return addr[:14] + "..." + addr[len(addr)-6:]
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

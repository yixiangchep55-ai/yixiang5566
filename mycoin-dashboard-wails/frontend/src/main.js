import './style.css';
import { BrowserOpenURL, WindowCenter, WindowSetMinSize, WindowSetSize } from '../wailsjs/runtime/runtime';

const appRoot = document.querySelector('#app');
const SELECTOR_WINDOW = { width: 560, height: 460, minWidth: 540, minHeight: 430 };
const SYNC_WINDOW = { width: 520, height: 380, minWidth: 500, minHeight: 360 };
const DASHBOARD_WINDOW = { width: 1200, height: 800, minWidth: 1200, minHeight: 800 };
const DASHBOARD_REFRESH_MS = 2000;
let syncPollHandle = null;

const state = {
  activeTab: 'overview',
  data: null,
  loading: true,
  toast: '',
  qrOpen: false,
  logFilter: 'all',
  walletExpanded: false,
  sendForm: {
    to: '',
    amount: '',
    fee: '0.01',
    memo: '',
  },
  settingsForm: null,
  chainMode: '',
  selectedChainMode: '',
  selectedIndexerEnabled: false,
  modeSelectionOpen: true,
  syncScreenOpen: false,
  syncLaunchMessage: '',
  syncLaunchError: '',
  syncLogScrollTop: 0,
  syncLogStickToBottom: true,
};

const knownTabs = new Set(['overview', 'wallet', 'logs', 'settings']);

function backend() {
  return window.go?.main?.App;
}

function clone(obj) {
  return JSON.parse(JSON.stringify(obj));
}

function safeArray(value) {
  return Array.isArray(value) ? value : [];
}

function formatNumber(value) {
  const number = Number(value || 0);
  return new Intl.NumberFormat().format(number);
}

function formatAmount(value) {
  return `${Number(value || 0).toFixed(2)} YIC`;
}

function truncate(value, head = 12, tail = 6) {
  if (!value) return '-';
  if (value.length <= head + tail + 3) return value;
  return `${value.slice(0, head)}...${value.slice(-tail)}`;
}

function tooltipText(value) {
  const safe = value == null || value === '' ? '-' : String(value);
  return escapeHtml(safe);
}

function truncateWithTooltip(value, head = 12, tail = 6, className = 'hover-value') {
  const safe = value == null || value === '' ? '-' : String(value);
  return `<span class="${className}" title="${tooltipText(safe)}">${escapeHtml(truncate(safe, head, tail))}</span>`;
}

function formatModeValue(value) {
  return value || 'Unknown';
}

function normalizeTextValue(value) {
  return String(value || '').trim();
}

function resolveExplorerURL(settings) {
  const explicit = String(settings?.explorerBaseURL || '').trim();
  if (explicit) {
    return explicit.replace(/\/+$/, '');
  }

  const apiBase = String(settings?.apiBase || '').trim();
  if (!apiBase) {
    return 'http://127.0.0.1:8080';
  }

  const trimmed = apiBase.replace(/\/+$/, '');
  if (trimmed.endsWith('/api')) {
    return trimmed.slice(0, -4);
  }
  return trimmed;
}

function relativeFromTimestamp(value) {
  if (!value) return 'just now';
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return value;
  const diff = Math.max(0, Date.now() - time);
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function categoryLabel(category) {
  switch (category) {
    case 'height': return 'Height';
    case 'sync': return 'Sync';
    case 'peers': return 'Peers';
    case 'mempool': return 'Mempool';
    case 'wallet': return 'Wallet';
    case 'orphan': return 'Orphan';
    default: return 'Node';
  }
}

function categoryClass(category) {
  return `tag-${category || 'node'}`;
}

function logCount(events, category) {
  return safeArray(events).filter(event => event.category === category).length;
}

function setActiveTab(tab) {
  if (!knownTabs.has(tab)) return;
  state.activeTab = tab;
  render();
}

function setLogFilter(filter) {
  state.logFilter = filter || 'all';
  render();
}

function safeResizeWindow(size) {
  const recenter = () => {
    try {
      WindowCenter();
    } catch (error) {
      console.debug('Window center skipped', error);
    }
  };

  try {
    WindowSetMinSize(size.minWidth, size.minHeight);
    WindowSetSize(size.width, size.height);
    recenter();
    setTimeout(recenter, 40);
    setTimeout(recenter, 140);
  } catch (error) {
    console.debug('Window resize skipped', error);
  }
}

function selectChainMode(mode) {
  if (!['archive', 'prune'].includes(mode)) return;
  state.selectedChainMode = mode;
  render();
}

function setLaunchIndexerEnabled(enabled) {
  const nextEnabled = Boolean(enabled);
  state.selectedIndexerEnabled = nextEnabled;
  state.settingsForm = state.settingsForm || clone(state.data?.settings || buildMockData().settings);
  state.settingsForm.enableIndexer = nextEnabled;
  render();
}

function formatModeLabel(mode) {
  if (mode === 'archive') return 'Archive';
  if (mode === 'prune') return 'Prune';
  return 'Unknown';
}

function normalizeModeLabel(mode, fallback = 'Unknown') {
  const value = String(mode || '').toLowerCase();
  if (value.includes('prun')) return 'Prune';
  if (value.includes('archive')) return 'Archive';
  return fallback;
}

function indexerBadgeMeta(data) {
  const indexer = data?.indexer || {};
  const status = String(indexer.status || '').toLowerCase();

  if (indexer.needs_restart) {
    return { label: 'Indexer Restart', className: 'neutral' };
  }
  if (indexer.running) {
    return { label: 'Indexer On', className: 'indexer-on' };
  }
  if (status.includes('db')) {
    return { label: 'Indexer Error', className: 'offline' };
  }
  return { label: 'Indexer Off', className: 'indexer-off' };
}

function headerPills(data) {
  const node = data?.node || {};
  const modeLabel = normalizeModeLabel(node.mode || state.chainMode, 'Unknown');
  const connectedLabel = data?.connected ? 'Connected' : 'Offline';
  const miningLabel = node.mining_enabled ? 'Miner On' : 'Miner Off';
  const indexerPill = indexerBadgeMeta(data);

  return [
    {
      label: connectedLabel,
      className: data?.connected ? 'connected' : 'offline',
    },
    {
      label: modeLabel,
      className: modeLabel === 'Prune' ? 'prune' : (modeLabel === 'Archive' ? 'archive' : 'neutral'),
    },
    {
      label: miningLabel,
      className: node.mining_enabled ? 'miner-on' : 'miner-off',
    },
    {
      label: indexerPill.label,
      className: indexerPill.className,
    },
  ];
}

function minerRuntimeMeta(data) {
  const node = data?.node || {};
  const miningEnabled = Boolean(node.mining_enabled);

  if (!data?.connected) {
    return {
      label: 'Offline',
      tone: 'tag-node',
      detail: 'Local node API is offline, so the miner cannot run yet.',
    };
  }

  if (!miningEnabled) {
    return {
      label: 'Paused',
      tone: 'tag-node',
      detail: 'Mining is disabled. The node can stay online without producing new blocks.',
    };
  }

  if (node.synced) {
    return {
      label: 'Mining active',
      tone: 'confirmed',
      detail: 'Mining is enabled and the node is fully synced, so block production can run.',
    };
  }

  if (Number(node.peer_count || 0) <= 0) {
    return {
      label: 'Waiting for peers',
      tone: 'tag-peers',
      detail: 'Mining is enabled, but the node still needs network peers before sync can finish.',
    };
  }

  return {
    label: 'Waiting for sync',
    tone: 'tag-sync',
    detail: `Mining is enabled, but the node is still syncing (${String(node.sync_state || 'syncing').toUpperCase()}).`,
  };
}

function parseSyncProgress(data) {
  const node = data?.node || {};
  const label = String(node.sync_state || '').toLowerCase();

  if (data?.connected && node.synced) return 100;
  if (!data?.connected) return 8;
  if (label === 'headers') return 36;
  if (label === 'ibd') return 58;
  if (label === 'bodies') return 82;
  if (label === 'synced') return 100;
  if (label === 'idle') return node.peer_count > 0 ? 18 : 12;
  if (node.is_syncing) return 68;
  if (node.peer_count > 0) return 24;
  return 14;
}

function syncStageText(data) {
  const node = data?.node || {};
  const label = String(node.sync_state || '').toLowerCase();

  if (data?.connected && node.synced) return 'Sync complete';
  if (!data?.connected) return 'Waiting for local node';
  if (label === 'headers') return 'Downloading headers';
  if (label === 'ibd') return 'Running initial block download';
  if (label === 'bodies') return 'Downloading block bodies';
  if (label === 'idle') return 'Waiting for sync to start';
  if (node.is_syncing) return 'Synchronizing node';
  return 'Checking node status';
}

function syncDetailText(data) {
  const node = data?.node || {};

  if (data?.connected && node.synced) {
    return 'Node is fully synced. Opening dashboard...';
  }
  if (!data?.connected) {
    if (state.syncLaunchError) {
      return state.syncLaunchError;
    }
    if (data?.message) {
      return data.message;
    }
    if (state.syncLaunchMessage) {
      return state.syncLaunchMessage;
    }
    return 'Trying to connect to the local dashboard API.';
  }
  if (node.peer_count <= 0) {
    if (node.last_peer_event === 'disconnected' && node.last_peer_error) {
      return `Last peer dropped: ${node.last_peer_error}`;
    }
    if (node.last_peer_event === 'disconnected') {
      return 'Last peer disconnected. Waiting to reconnect before sync can continue.';
    }
    return 'Connected, but still waiting for peers before sync can continue.';
  }
  return `${formatNumber(node.best_height)} blocks loaded with ${formatNumber(node.peer_count)} peers connected.`;
}

function syncPeerDiagnostics(node) {
  const items = [];
  const lastEvent = String(node?.last_peer_event || '').trim();
  const lastAddr = String(node?.last_peer_addr || '').trim();
  const lastError = String(node?.last_peer_error || '').trim();
  const lastSeenAt = String(node?.last_peer_seen_at || '').trim();

  if (lastEvent) {
    items.push({
      label: 'Last peer event',
      value: lastEvent === 'active' ? 'Peer connected' : (lastEvent === 'disconnected' ? 'Peer disconnected' : lastEvent),
      title: lastEvent,
      wide: false,
    });
  }

  if (lastAddr) {
    items.push({
      label: 'Peer address',
      value: truncate(lastAddr, 18, 8),
      title: lastAddr,
      wide: false,
    });
  }

  if (lastSeenAt) {
    items.push({
      label: 'Last update',
      value: relativeFromTimestamp(lastSeenAt),
      title: lastSeenAt,
      wide: false,
    });
  }

  if (lastError) {
    items.push({
      label: 'Last issue',
      value: truncate(lastError, 52, 14),
      title: lastError,
      wide: true,
    });
  }

  return items;
}

function backendLogTail(data) {
  const lines = Array.isArray(data?.backend_log) ? data.backend_log.filter(Boolean) : [];
  if (!lines.length) return [];
  return lines;
}

function captureSyncLogScrollState() {
  const logElement = appRoot.querySelector('.sync-log-tail');
  if (!logElement) return;

  const maxScrollTop = Math.max(0, logElement.scrollHeight - logElement.clientHeight);
  state.syncLogScrollTop = logElement.scrollTop;
  state.syncLogStickToBottom = maxScrollTop-logElement.scrollTop <= 24;
}

function bindSyncLogScrollState() {
  const logElement = appRoot.querySelector('.sync-log-tail');
  if (!logElement) return;

  const maxScrollTop = Math.max(0, logElement.scrollHeight - logElement.clientHeight);
  logElement.scrollTop = state.syncLogStickToBottom
    ? maxScrollTop
    : Math.min(state.syncLogScrollTop, maxScrollTop);

  logElement.onscroll = () => {
    const currentMax = Math.max(0, logElement.scrollHeight - logElement.clientHeight);
    state.syncLogScrollTop = logElement.scrollTop;
    state.syncLogStickToBottom = currentMax-logElement.scrollTop <= 24;
  };
}

function startSyncPolling() {
  stopSyncPolling();
  syncPollHandle = setInterval(() => {
    if (state.syncScreenOpen) {
      refreshSyncGate(false);
    }
  }, 1600);
}

function stopSyncPolling() {
  if (syncPollHandle) {
    clearInterval(syncPollHandle);
    syncPollHandle = null;
  }
}

function openDashboardFromSync() {
  stopSyncPolling();
  state.syncScreenOpen = false;
  state.syncLaunchMessage = '';
  state.syncLaunchError = '';
  safeResizeWindow(DASHBOARD_WINDOW);
  render();
}

async function refreshSyncGate(showLoader = false) {
  await refreshDashboard(false, { silent: !showLoader });
  if (state.data?.connected) {
    state.syncLaunchError = '';
    state.syncLaunchMessage = state.data?.node?.synced
      ? 'Node is fully synced. Opening dashboard...'
      : 'Local node is online. Waiting for synchronization to finish.';
  }
  if (state.data?.connected && state.data?.node?.synced) {
    openDashboardFromSync();
  }
}

async function confirmChainMode() {
  if (!state.selectedChainMode) return;
  const launchSettings = {
    ...(state.settingsForm || clone(state.data?.settings || buildMockData().settings)),
    enableIndexer: state.selectedIndexerEnabled,
  };

  if (backend()?.SaveSettings) {
    const saved = await backend().SaveSettings(launchSettings);
    state.settingsForm = clone(saved);
    state.selectedIndexerEnabled = Boolean(saved.enableIndexer);
    state.data = state.data || buildMockData();
    state.data.settings = saved;
    if (backend()?.CheckIndexerConnection) {
      try {
        const indexer = await backend().CheckIndexerConnection(saved);
        state.data.indexer = indexer;
      } catch (error) {
        console.error('Unable to preflight indexer', error);
      }
    }
  } else {
    state.settingsForm = clone(launchSettings);
    state.data = state.data || buildMockData();
    state.data.settings = clone(launchSettings);
  }

  state.chainMode = state.selectedChainMode;
  state.modeSelectionOpen = false;
  state.syncScreenOpen = true;
  state.syncLaunchError = '';
  state.syncLaunchMessage = `Starting ${formatModeLabel(state.chainMode)} node...`;
  safeResizeWindow(SYNC_WINDOW);
  state.loading = true;
  render();
  try {
    if (backend()?.StartBackend) {
      const launch = await backend().StartBackend(state.chainMode);
      if (launch?.message) {
        state.syncLaunchMessage = launch.message;
      }
    }
  } catch (error) {
    console.error(error);
    state.loading = false;
    state.syncLaunchError = error?.message || 'Unable to start local node.';
    state.data = state.data || buildMockData();
    state.data.connected = false;
    state.data.message = state.syncLaunchError;
    state.data.timestamp = new Date().toISOString();
    render();
    return;
  }
  startSyncPolling();
  await refreshSyncGate(true);
}

function buildMockData() {
  return {
    connected: true,
    timestamp: new Date().toISOString(),
    message: '',
    node: {
      node_id: 1,
      mode: 'full-node + wallet',
      best_height: 843221,
      best_hash: '00000000000000000000000000000a13e6d89bc1',
      synced: false,
      sync_state: 'syncing',
      is_syncing: true,
      peer_count: 14,
      mempool_count: 128,
      orphan_count: 3,
      mining_enabled: true,
      mining_address: 'kaspa:qzn4h9k7v9j5p8l3w8gtr0n2v38f2',
    },
    wallet: {
      address: 'kaspa:qzn4h9k7v9j5p8l3w8gtr0n2v38f2',
      balance: 1261.32,
      spendable_balance: 1248.52,
      pending_amount: 12.80,
      pending_txs: 5,
      activity: [
        { status: 'Pending', type: 'Outgoing', amount: -25.0, confirmations: 0, txid: '8f1a02d92d0d4c77d6', timestamp: Date.now() / 1000 },
        { status: 'Confirmed', type: 'Incoming', amount: 120.0, confirmations: 12, txid: '19ab5a8ef2aa12bb73', timestamp: Date.now() / 1000 },
        { status: 'Pending', type: 'Incoming', amount: 3.2, confirmations: 0, txid: '84be1171a188991122', timestamp: Date.now() / 1000 },
        { status: 'Confirmed', type: 'Outgoing', amount: -8.5, confirmations: 27, txid: '0ad9eeff41122ab882', timestamp: Date.now() / 1000 },
        { status: 'Confirmed', type: 'Mining', amount: 64.0, confirmations: 103, txid: '7c11dde0911fccaabb', timestamp: Date.now() / 1000 },
        { status: 'Pending', type: 'Outgoing', amount: -1.0, confirmations: 0, txid: '1fe22d63339ab8cc11', timestamp: Date.now() / 1000 },
      ],
    },
    events: [
      { time: '10:24:12', category: 'height', title: 'Height changed', description: '843,206 -> 843,221' },
      { time: '10:24:02', category: 'sync', title: 'Sync status changed', description: '99.78% -> 99.82%' },
      { time: '10:23:44', category: 'peers', title: 'Peer count changed', description: '13 -> 14' },
      { time: '10:23:21', category: 'mempool', title: 'Mempool changed', description: '117 -> 128' },
      { time: '10:22:58', category: 'wallet', title: 'Wallet balance changed', description: '1,197.32 -> 1,261.32 YIC' },
      { time: '10:22:31', category: 'orphan', title: 'Orphan count changed', description: '2 -> 3' },
      { time: '10:21:55', category: 'sync', title: 'Node entered syncing', description: 'checking headers' },
    ],
    history: [
      { label: '10:00', height: 843112 },
      { label: '10:05', height: 843121 },
      { label: '10:10', height: 843131 },
      { label: '10:15', height: 843149 },
      { label: '10:20', height: 843176 },
      { label: '10:25', height: 843221 },
    ],
    settings: {
      apiBase: 'http://127.0.0.1:8080',
      explorerBaseURL: '',
      enableIndexer: false,
      databaseURL: '',
    },
    indexer: {
      enabled: false,
      requested: false,
      reachable: false,
      running: false,
      using_default: true,
      needs_restart: false,
      status: 'Disabled',
      message: 'Indexer is turned off for the next node launch.',
    },
  };
}

async function loadDashboard() {
  if (!backend()?.GetDashboardData) {
    return buildMockData();
  }
  return backend().GetDashboardData();
}

async function refreshDashboard(showToast = false, options = {}) {
  const silent = Boolean(options.silent);
  if (!silent) {
    state.loading = true;
    render();
  }
  try {
    const data = await loadDashboard();
    state.data = data;
    if (!state.settingsForm) {
      state.settingsForm = clone(data.settings);
    }
    if (showToast) {
      state.toast = data.connected ? 'Dashboard refreshed' : (data.message || 'API offline');
      setTimeout(() => {
        state.toast = '';
        render();
      }, 2200);
    }
  } catch (error) {
    console.error(error);
    state.data = buildMockData();
    state.data.connected = false;
    state.data.message = 'Unable to load local API';
    if (showToast) {
      state.toast = 'Unable to reach local API';
      setTimeout(() => {
        state.toast = '';
        render();
      }, 2200);
    }
  } finally {
    state.loading = false;
    render();
  }
}

function chartSVG(history) {
  const points = history?.length ? history : buildMockData().history;
  const values = points.map(point => Number(point.height || 0));
  const max = Math.max(...values);
  const min = Math.min(...values);
  const width = 640;
  const height = 250;
  const stepX = width / Math.max(1, points.length - 1);
  const safeRange = Math.max(1, max - min);
  const line = points.map((point, index) => {
    const x = index * stepX;
    const y = height - ((point.height - min) / safeRange) * (height - 18) - 9;
    return `${x},${y}`;
  }).join(' ');

  const dots = points.map((point, index) => {
    const x = index * stepX;
    const y = height - ((point.height - min) / safeRange) * (height - 18) - 9;
    return `<circle cx="${x}" cy="${y}" r="4.5" class="chart-dot"></circle>`;
  }).join('');

  return `
    <svg viewBox="0 0 ${width} ${height}" class="line-chart" preserveAspectRatio="none">
      <g class="chart-grid">
        <line x1="0" y1="40" x2="${width}" y2="40"></line>
        <line x1="0" y1="95" x2="${width}" y2="95"></line>
        <line x1="0" y1="150" x2="${width}" y2="150"></line>
        <line x1="0" y1="205" x2="${width}" y2="205"></line>
      </g>
      <polyline points="${line}" class="chart-line"></polyline>
      ${dots}
    </svg>
  `;
}

function metricCard(title, value, subtitle, badge = '', tone = '') {
  return `
    <section class="metric-card ${tone}">
      <div class="metric-head">
        <span>${title}</span>
        ${badge ? `<span class="metric-badge ${tone}">${badge}</span>` : ''}
      </div>
      <div class="metric-value">${value}</div>
      <div class="metric-subtitle">${subtitle}</div>
    </section>
  `;
}

function metricCardWithTooltip(title, rawValue, subtitle, badge = '', tone = '', head = 12, tail = 6) {
  return metricCard(title, truncateWithTooltip(rawValue, head, tail), subtitle, badge, tone);
}

function topHeader(title, subtitle, data) {
  const pills = headerPills(data);
  return `
    <header class="page-head">
      <div>
        <h1>${title}</h1>
        <p>${subtitle}</p>
      </div>
      <div class="head-pills">
        ${pills.map((pill) => `<span class="head-pill ${pill.className}">${pill.label}</span>`).join('')}
      </div>
    </header>
  `;
}

function navItem(id, label, short) {
  const active = state.activeTab === id ? ' active' : '';
  return `<button type="button" class="nav-item${active}" data-tab="${id}"><span class="nav-icon">${short}</span><span>${label}</span></button>`;
}

function renderSidebar(data) {
  return `
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">D</div>
        <div>
          <div class="brand-title">NodeDash</div>
          <div class="brand-subtitle">Local dashboard</div>
        </div>
      </div>

      <nav class="nav-links">
        ${navItem('overview', 'Overview', 'O')}
        ${navItem('wallet', 'Wallet', 'W')}
        ${navItem('logs', 'Logs', 'L')}
        ${navItem('settings', 'Settings', 'S')}
      </nav>

      <div class="local-node">
        <div class="local-badge">Y</div>
        <div>
          <div class="local-title">Local node</div>
          <div class="local-subtitle">${data.connected ? '127.0.0.1 active' : '127.0.0.1 offline'}</div>
        </div>
      </div>
    </aside>
  `;
}

function renderOverview(data) {
  const node = data.node;
  const wallet = data.wallet;
  const settings = data.settings || buildMockData().settings;
  const history = safeArray(data.history);
  const healthBadge = data.connected ? (node.peer_count > 0 ? 'Healthy' : 'Watch') : 'Offline';
  const statusText = !data.connected ? 'Offline' : (node.is_syncing ? 'Running' : 'Running');
  const syncPrimary = node.synced ? '100%' : (node.sync_state ? node.sync_state.toUpperCase() : 'OFFLINE');

  return `
    <section class="page page-overview">
    ${topHeader('Overview', 'Local node health, chain progress and wallet summary', data)}
    <section class="metrics-grid">
      ${metricCard('Node status', statusText, data.connected ? 'RPC connected' : (data.message || 'Local API offline'), healthBadge, 'healthy')}
      ${metricCard('Current height', formatNumber(node.best_height), `Last change ${relativeFromTimestamp(data.timestamp)}`, '', '')}
      ${metricCard('Sync status', syncPrimary, node.synced ? 'Fully synced' : (node.is_syncing ? 'Synchronization in progress' : 'Waiting for sync'), node.synced ? 'Synced' : 'Syncing', 'sync')}
      ${metricCard('Peers', formatNumber(node.peer_count), node.peer_count > 0 ? `Connected peers available` : 'Awaiting peer discovery', '', '')}
      ${metricCard('Mempool', formatNumber(node.mempool_count), `${Math.min(node.mempool_count, 12)} high-priority tx`, '', '')}
      ${metricCard('Fork / orphan', formatNumber(node.orphan_count), node.orphan_count > 0 ? `Non-main-chain blocks seen ${relativeFromTimestamp(data.timestamp)}` : 'No orphan or fork blocks', '', '')}
      ${metricCardWithTooltip('Best hash', node.best_hash, 'Hover to preview full hash', '', '', 12, 4)}
      ${metricCardWithTooltip('Mining address', node.mining_address, 'Hover to preview full address', '', '', 12, 4)}
    </section>

    <section class="overview-bottom">
      <article class="panel tall-panel">
        <div class="panel-header">
          <div>
            <h2>Height progress</h2>
            <p>Recent samples from this dashboard session</p>
          </div>
          <button type="button" class="ghost-btn explorer-action" data-action="open-explorer">Open explorer</button>
        </div>
        <div class="chart-frame">${chartSVG(history)}</div>
        <div class="mini-stats">
          <div class="mini-stat">
            <span class="mini-dot cyan"></span>
            <div><small>Avg block interval</small><strong>${history.length > 1 ? 'Tracked' : 'Waiting'}</strong></div>
          </div>
          <div class="mini-stat">
            <span class="mini-dot green"></span>
            <div><small>Chain quality</small><strong>${data.connected ? (node.peer_count > 0 ? 'Healthy' : 'Watching') : 'Offline'}</strong></div>
          </div>
          <div class="mini-stat">
            <span class="mini-dot orange"></span>
            <div><small>Lag to network</small><strong>${node.is_syncing ? 'Syncing' : 'Low'}</strong></div>
          </div>
        </div>
      </article>

      <div class="overview-side">
        <article class="panel wallet-summary-card">
          <div class="panel-header summary-header">
            <div>
              <h2>Wallet summary</h2>
              <p>Balance at a glance</p>
            </div>
            <button class="primary-btn summary-action" data-action="switch-wallet">Open wallet</button>
          </div>
          <div class="summary-row"><span>Available</span><strong>${formatAmount(wallet.spendable_balance)}</strong></div>
          <div class="summary-row"><span>Pending</span><strong>${formatAmount(wallet.pending_amount)}</strong></div>
          <div class="summary-row"><span>Total</span><strong>${formatAmount(wallet.balance)}</strong></div>
        </article>

        <article class="panel node-details-card">
          <div class="panel-header">
            <div>
              <h2>Node details</h2>
              <p>Connection and runtime summary</p>
            </div>
          </div>
          <div class="detail-list">
            <div><span>API</span><strong title="${tooltipText(settings.apiBase)}">${escapeHtml(settings.apiBase)}</strong></div>
            <div><span>Mode</span><strong title="${tooltipText(formatModeValue(node.mode || 'Unavailable'))}">${escapeHtml(formatModeValue(node.mode || 'Unavailable'))}</strong></div>
            <div><span>Best hash</span><strong>${truncateWithTooltip(node.best_hash, 14, 6)}</strong></div>
            <div><span>Mining address</span><strong>${truncateWithTooltip(node.mining_address, 14, 6)}</strong></div>
          </div>
        </article>
      </div>
    </section>
    </section>
  `;
}

function renderWallet(data) {
  const wallet = data.wallet;
  const activities = safeArray(wallet.activity).filter((item) => {
    const type = String(item?.type || '').toLowerCase();
    return type !== 'mining' && type !== 'coinbase';
  });

  return `
    <section class="page page-wallet">
    ${topHeader('Wallet', 'Addresses, balances, pending transactions and quick send', data)}
    <section class="wallet-shell">
      <article class="address-card panel">
        <div>
          <p class="eyebrow">Local wallet address</p>
          <h2 class="address-text" title="${tooltipText(wallet.address || 'Unavailable')}">${escapeHtml(truncate(wallet.address || 'Unavailable', 26, 10))}</h2>
        </div>
        <div class="button-row compact">
          <button class="ghost-btn" data-action="copy-address">Copy</button>
          <button class="primary-btn" data-action="toggle-qr">QR</button>
        </div>
      </article>

      <section class="wallet-metrics">
        ${metricCard('Total balance', formatAmount(wallet.balance), `Updated ${relativeFromTimestamp(data.timestamp)}`, '', 'cyan')}
        ${metricCard('Spendable', formatAmount(wallet.spendable_balance), 'Ready to send', '', 'green')}
        ${metricCard('Pending', formatAmount(wallet.pending_amount), 'Awaiting confirmation', '', 'amber')}
        ${metricCard('Pending tx', formatNumber(wallet.pending_txs), wallet.pending_txs ? `${wallet.pending_txs} queued` : 'No pending items', '', 'purple')}
      </section>

      <section class="wallet-lower">
        <article class="panel send-panel">
          <div class="panel-header">
            <div>
              <h2>Send transaction</h2>
            </div>
          </div>
          <form id="send-form" class="send-form">
            <label>
              <span>Recipient address</span>
              <input name="to" placeholder="kaspa:recipient..." value="${escapeHtml(state.sendForm.to)}" />
            </label>
            <div class="two-col">
              <label>
                <span>Amount</span>
                <input name="amount" placeholder="25.00" value="${escapeHtml(state.sendForm.amount)}" />
              </label>
              <label>
                <span>Fee</span>
                <input name="fee" placeholder="0.01" value="${escapeHtml(state.sendForm.fee)}" />
              </label>
            </div>
            <label>
              <span>Memo / note</span>
              <textarea name="memo" rows="3" placeholder="Mining payout for node maintenance">${escapeHtml(state.sendForm.memo)}</textarea>
            </label>
            <div class="button-row">
              <button type="submit" class="primary-btn">Send now</button>
              <button type="button" class="ghost-btn" data-action="clear-send">Clear</button>
            </div>
          </form>
        </article>

        <article class="panel tx-panel">
          <div class="panel-header">
            <div>
              <h2>Recent &amp; pending transactions</h2>
              <p>Latest wallet activity</p>
            </div>
          </div>
          <div class="activity-table-wrap">
            <table class="activity-table">
              <thead>
                <tr>
                  <th>Status</th>
                  <th>Type</th>
                  <th>Amount</th>
                  <th>Confirmations</th>
                  <th>Tx hash</th>
                </tr>
              </thead>
              <tbody>
                ${activities.length ? activities.slice(0, state.walletExpanded ? activities.length : 6).map(item => `
                  <tr>
                    <td><span class="status-pill ${item.status?.toLowerCase() || 'confirmed'}">${item.status || 'Confirmed'}</span></td>
                    <td>${item.type || '-'}</td>
                    <td class="${item.amount >= 0 ? 'amount-positive' : 'amount-negative'}">${item.amount >= 0 ? '+' : ''}${Number(item.amount).toFixed(2)} YIC</td>
                    <td>${item.confirmations}</td>
                    <td title="${tooltipText(item.txid)}">${escapeHtml(truncate(item.txid, 8, 4))}</td>
                  </tr>
                `).join('') : `<tr><td colspan="5" class="empty-cell">No wallet activity yet.</td></tr>`}
              </tbody>
            </table>
          </div>
          <div class="button-row end">
            <button class="ghost-btn" data-action="refresh">Refresh</button>
            <button class="primary-btn" data-action="toggle-wallet-view">${state.walletExpanded ? 'View less' : 'View all'}</button>
          </div>
        </article>
      </section>
    </section>
    </section>
  `;
}

function renderLogs(data) {
  const events = safeArray(data.events);
  const filtered = events.filter(event => {
    if (state.logFilter === 'all') {
      return true;
    }
    if (state.logFilter === 'mempool') {
      return event.category === 'mempool' || event.category === 'orphan';
    }
    return event.category === state.logFilter;
  });
  const categories = [
    { value: 'all', label: 'All events' },
    { value: 'height', label: 'Height' },
    { value: 'sync', label: 'Sync' },
    { value: 'peers', label: 'Peers' },
    { value: 'mempool', label: 'Mempool/Orphan' },
    { value: 'wallet', label: 'Wallet' },
  ];

  return `
    <section class="page page-logs">
    ${topHeader('Logs', 'Session status changes for node, sync, peers, mempool, orphan and wallet', data)}
    <div class="logs-filters">
      <span class="filter-label">Filter</span>
      ${categories.map(category => {
        const active = state.logFilter === category.value ? ' active' : '';
        return `<button class="filter-pill${active} ${categoryClass(category.value)}" data-action="set-filter" data-filter="${category.value}">${category.label}</button>`;
      }).join('')}
    </div>
    <section class="logs-layout">
      <article class="panel timeline-panel">
        <div class="panel-header">
          <div>
            <h2>Dashboard session timeline</h2>
            <p>Every state transition is logged with previous and current values</p>
          </div>
        </div>
        <div class="timeline-list">
          ${filtered.length ? filtered.map(event => `
            <div class="timeline-item">
              <div class="timeline-time">${event.time}</div>
              <div class="timeline-dot ${categoryClass(event.category)}"></div>
              <div class="timeline-content">
                <div class="timeline-top">
                  <strong>${event.title}</strong>
                  <span class="status-pill ${categoryClass(event.category)}">${categoryLabel(event.category)}</span>
                </div>
                <div class="timeline-detail">${event.description}</div>
              </div>
            </div>
          `).join('') : `<div class="empty-state">No session events match the current filter.</div>`}
        </div>
      </article>

      <div class="logs-side">
        <article class="panel snapshot-panel">
          <div class="panel-header">
            <div>
              <h2>Current session snapshot</h2>
            </div>
          </div>
          <div class="summary-row"><span>Height</span><strong>${formatNumber(data.node.best_height)}</strong></div>
          <div class="summary-row"><span>Sync</span><strong>${data.node.synced ? '100%' : (data.node.sync_state || 'offline')}</strong></div>
          <div class="summary-row"><span>Peers</span><strong>${formatNumber(data.node.peer_count)}</strong></div>
          <div class="summary-row"><span>Mempool</span><strong>${formatNumber(data.node.mempool_count)}</strong></div>
          <div class="summary-row"><span>Fork / orphan</span><strong>${formatNumber(data.node.orphan_count)}</strong></div>
        </article>

        <article class="panel counter-panel">
          <div class="panel-header">
            <div><h2>Event counters</h2></div>
          </div>
          <div class="counter-grid">
            <div class="counter-box"><small>Height changes</small><strong>${logCount(events, 'height')}</strong></div>
            <div class="counter-box"><small>Sync changes</small><strong>${logCount(events, 'sync')}</strong></div>
            <div class="counter-box"><small>Peer changes</small><strong>${logCount(events, 'peers')}</strong></div>
            <div class="counter-box"><small>Wallet changes</small><strong>${logCount(events, 'wallet')}</strong></div>
          </div>
        </article>

        <article class="panel live-tail-panel">
          <div class="panel-header">
            <div><h2>Live tail</h2></div>
          </div>
          <pre class="live-tail">${events.slice(0, 6).map(event => `[${event.time}] ${event.category} ${event.description}`).join('\n')}</pre>
        </article>
      </div>
    </section>
    </section>
  `;
}

function renderSettings(data) {
  const settings = state.settingsForm || data.settings;
  const savedSettings = data.settings || buildMockData().settings;
  const miningEnabled = Boolean(data.node?.mining_enabled);
  const runtime = minerRuntimeMeta(data);
  const minerButtonLabel = miningEnabled ? 'Stop mining' : 'Start mining';
  const minerButtonTarget = miningEnabled ? 'false' : 'true';
  const minerConfigured = miningEnabled ? 'Enabled' : 'Disabled';
  const indexer = data.indexer || buildMockData().indexer;
  const indexerPendingSave =
    normalizeTextValue(settings.databaseURL) !== normalizeTextValue(savedSettings.databaseURL);
  const indexerStatusLabel = indexerPendingSave ? 'Pending save' : indexer.status;
  const indexerMessage = indexerPendingSave
    ? 'Save settings to apply the updated indexer database URL on the next node launch.'
    : indexer.message;
  return `
    <section class="page page-settings">
    ${topHeader('Settings', 'Local dashboard connection, central explorer link and runtime summary', data)}
    <section class="settings-layout">
      <article class="panel settings-main">
        <div class="panel-header">
          <div>
            <h2>Connection &amp; startup</h2>
            <p>Configure the local node API and the explorer link shown in this dashboard</p>
          </div>
        </div>
        <form id="settings-form" class="settings-form">
          <label>
            <span>API endpoint</span>
            <input name="apiBase" value="${escapeHtml(settings.apiBase)}" />
          </label>
          <label>
            <span>Explorer URL</span>
            <input
              name="explorerBaseURL"
              placeholder="https://explorer.mycoin.com"
              value="${escapeHtml(settings.explorerBaseURL || '')}"
            />
          </label>
          <label>
            <span>Database URL (optional)</span>
            <input
              name="databaseURL"
              placeholder="postgres://user:pass@localhost:5432/mycoin_explorer?sslmode=disable"
              value="${escapeHtml(settings.databaseURL || '')}"
            />
          </label>
          <p class="settings-help">${indexerMessage}</p>
          <div class="button-row">
            <button type="button" class="ghost-btn" data-action="refresh">Test connection</button>
            <button type="button" class="ghost-btn" data-action="check-indexer">Test indexer DB</button>
            <button type="submit" class="primary-btn">Save</button>
          </div>
        </form>
      </article>

      <div class="settings-side">
        <article class="panel runtime-panel">
          <div class="panel-header">
            <div><h2>Runtime summary</h2></div>
          </div>
          <div class="summary-row"><span>Mode</span><strong title="${tooltipText(formatModeValue(data.node.mode || 'Unavailable'))}">${escapeHtml(formatModeValue(data.node.mode || 'Unavailable'))}</strong></div>
          <div class="summary-row"><span>Wallet</span><strong>${truncateWithTooltip(data.wallet.address || 'Unavailable', 18, 8)}</strong></div>
          <div class="summary-row"><span>Best hash</span><strong>${truncateWithTooltip(data.node.best_hash, 18, 8)}</strong></div>
          <div class="summary-row"><span>Explorer</span><strong>${truncateWithTooltip(resolveExplorerURL(settings), 18, 8)}</strong></div>
          <div class="summary-row"><span>Indexer</span><strong>${escapeHtml(indexerStatusLabel)}</strong></div>
          <div class="summary-row"><span>API status</span><strong>${data.connected ? 'Connected' : 'Offline'}</strong></div>
        </article>

        <article class="panel miner-panel">
          <div class="panel-header">
            <div>
              <h2>Control miner</h2>
            </div>
          </div>
          <div class="summary-row">
            <span>Configured</span>
            <strong>${minerConfigured}</strong>
          </div>
          <div class="summary-row">
            <span>Runtime</span>
            <strong><span class="status-pill ${runtime.tone} miner-runtime-pill">${runtime.label}</span></strong>
          </div>
          <div class="summary-row">
            <span>Payout address</span>
            <strong>${truncateWithTooltip(data.node.mining_address || 'Unavailable', 18, 8)}</strong>
          </div>
          <div class="button-row">
            <button
              type="button"
              class="${miningEnabled ? 'danger-btn' : 'primary-btn'}"
              data-action="toggle-miner"
              data-enabled="${minerButtonTarget}"
              ${data.connected ? '' : 'disabled'}
            >${minerButtonLabel}</button>
          </div>
        </article>
      </div>
    </section>
    </section>
  `;
}

function escapeHtml(value = '') {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;');
}

function renderContent(data) {
  try {
    switch (state.activeTab) {
      case 'wallet':
        return renderWallet(data);
      case 'logs':
        return renderLogs(data);
      case 'settings':
        return renderSettings(data);
      default:
        return renderOverview(data);
    }
  } catch (error) {
    console.error('Unable to render active tab', state.activeTab, error);
    state.activeTab = 'overview';
    return renderOverview(buildMockData());
  }
}

function renderModeSelector() {
  const selected = state.selectedChainMode;
  const indexerEnabled = state.selectedIndexerEnabled;
  return `
    <main class="launch-shell">
      <section class="launch-card">
        <div class="launch-top">
          <div class="launch-mark">D</div>
          <div>
            <p class="launch-kicker">MyCoin Dashboard</p>
            <h1>Select Node Mode</h1>
          </div>
        </div>
        <p class="launch-copy">Choose the mode first, then confirm to open the dashboard.</p>
        <div class="mode-grid">
          <button type="button" class="mode-option archive${selected === 'archive' ? ' selected' : ''}" data-action="select-chain-mode" data-mode="archive">
            <span class="mode-tag">Archive</span>
            <strong>Full chain mode</strong>
            <small>Best when you want the complete chain and full history available.</small>
          </button>
          <button type="button" class="mode-option prune${selected === 'prune' ? ' selected' : ''}" data-action="select-chain-mode" data-mode="prune">
            <span class="mode-tag">Prune</span>
            <strong>Lightweight mode</strong>
            <small>Use less disk and start leaner when full archive data is not needed.</small>
          </button>
        </div>
        <div class="launch-toggle">
          <div class="launch-toggle-copy">
            <span class="launch-toggle-label">Start indexer</span>
            <small>Turn this on if you want explorer indexing included in the next node launch.</small>
          </div>
          <button
            type="button"
            class="launch-switch${indexerEnabled ? ' enabled' : ''}"
            data-action="toggle-launch-indexer"
            aria-pressed="${indexerEnabled ? 'true' : 'false'}"
          >
            <span class="launch-switch-track">
              <span class="launch-switch-thumb"></span>
            </span>
            <span class="launch-switch-text">${indexerEnabled ? 'On' : 'Off'}</span>
          </button>
        </div>
        <div class="launch-footer">
          <span class="launch-choice">${selected ? `Selected: ${selected} | Indexer: ${indexerEnabled ? 'on' : 'off'}` : 'Select one mode to continue'}</span>
          <button type="button" class="primary-btn launch-confirm" data-action="confirm-chain-mode" ${selected ? '' : 'disabled'}>Confirm</button>
        </div>
      </section>
    </main>
  `;
}

function renderSyncScreen() {
  const data = state.data || buildMockData();
  const progress = parseSyncProgress(data);
  const node = data.node || {};
  const stage = syncStageText(data);
  const detail = syncDetailText(data);
  const peerDiagnostics = syncPeerDiagnostics(node);
  const logTail = backendLogTail(data);
  const peerPanel = peerDiagnostics.length ? `
        <div class="sync-peer-panel">
          <div class="sync-log-title">Peer diagnostics</div>
          <div class="sync-peer-grid">
            ${peerDiagnostics.map((item) => `
              <div class="sync-peer-item${item.wide ? ' sync-peer-item-wide' : ''}">
                <span>${escapeHtml(item.label)}</span>
                <strong title="${tooltipText(item.title || item.value)}">${escapeHtml(item.value)}</strong>
              </div>
            `).join('')}
          </div>
        </div>
  ` : '';
  const logPanel = logTail.length ? `
        <div class="sync-log-panel">
          <div class="sync-log-title">Recent node output</div>
          <pre class="sync-log-tail">${escapeHtml(logTail.join('\n'))}</pre>
        </div>
  ` : '';

  return `
    <main class="launch-shell sync-shell">
      <section class="launch-card sync-card">
        <div class="launch-top">
          <div class="launch-mark">D</div>
          <div>
            <p class="launch-kicker">MyCoin Dashboard</p>
            <h1>Syncing ${formatModeLabel(state.chainMode)}</h1>
          </div>
        </div>
        <div class="sync-pills">
          <span class="mode-tag">${formatModeLabel(state.chainMode)}</span>
          <span class="sync-stage-pill">${stage}</span>
        </div>
        <p class="launch-copy">${detail}</p>
        <div class="sync-meter">
          <div class="sync-fill" style="width:${progress}%"></div>
        </div>
        <div class="sync-progress-row">
          <strong>${progress}%</strong>
          <span>${state.loading ? 'Checking latest status...' : relativeFromTimestamp(data.timestamp)}</span>
        </div>
        <div class="sync-grid">
          <div class="sync-stat">
            <span>Height</span>
            <strong>${formatNumber(node.best_height)}</strong>
          </div>
          <div class="sync-stat">
            <span>Peers</span>
            <strong>${formatNumber(node.peer_count)}</strong>
          </div>
          <div class="sync-stat">
            <span>Status</span>
            <strong>${String(node.sync_state || (data.connected ? 'connected' : 'offline')).toUpperCase()}</strong>
          </div>
        </div>
        ${peerPanel}
        ${logPanel}
      </section>
    </main>
  `;
}

function bindViewInteractions() {
  appRoot.querySelectorAll('[data-tab]').forEach((element) => {
    element.onclick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      setActiveTab(element.dataset.tab);
    };
  });

  appRoot.querySelectorAll('[data-action="switch-wallet"]').forEach((element) => {
    element.onclick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      setActiveTab('wallet');
    };
  });

  appRoot.querySelectorAll('[data-action="set-filter"]').forEach((element) => {
    element.onclick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      setLogFilter(element.dataset.filter);
    };
  });

  appRoot.querySelectorAll('[data-action="select-chain-mode"]').forEach((element) => {
    element.onclick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      selectChainMode(element.dataset.mode);
    };
  });

  appRoot.querySelectorAll('[data-action="confirm-chain-mode"]').forEach((element) => {
    element.onclick = async (event) => {
      event.preventDefault();
      event.stopPropagation();
      await confirmChainMode();
    };
  });

}

function render() {
  if (state.syncScreenOpen) {
    captureSyncLogScrollState();
  }

  if (state.modeSelectionOpen) {
    appRoot.innerHTML = renderModeSelector();
    bindViewInteractions();
    return;
  }

  if (state.syncScreenOpen) {
    appRoot.innerHTML = renderSyncScreen();
    bindViewInteractions();
    bindSyncLogScrollState();
    return;
  }

  const data = state.data || buildMockData();
  const toast = state.toast ? `<div class="toast">${state.toast}</div>` : '';
  const qrModal = state.qrOpen ? `
    <div class="modal-backdrop" data-action="close-qr">
      <div class="modal-card" onclick="event.stopPropagation()">
        <h3>Wallet address</h3>
        <p>${data.wallet.address || 'Unavailable'}</p>
        <div class="qr-placeholder">QR preview</div>
        <button class="primary-btn" data-action="close-qr">Close</button>
      </div>
    </div>
  ` : '';

  appRoot.innerHTML = `
    <div class="dashboard-shell">
      ${renderSidebar(data)}
      <main class="content-shell">
        ${renderContent(data)}
      </main>
      ${toast}
      ${qrModal}
    </div>
  `;

  bindViewInteractions();
}

appRoot.addEventListener('click', async (event) => {
  const source = event.target instanceof Element ? event.target : event.target?.parentElement;
  if (!source) return;

  const target = source.closest('[data-tab],[data-action],[data-filter]');
  if (!target) return;

  if (target.dataset.tab) {
    setActiveTab(target.dataset.tab);
    return;
  }

  const action = target.dataset.action;
  if (action === 'select-chain-mode') {
    selectChainMode(target.dataset.mode);
    return;
  }
  if (action === 'toggle-launch-indexer') {
    setLaunchIndexerEnabled(!state.selectedIndexerEnabled);
    return;
  }
  if (action === 'confirm-chain-mode') {
    await confirmChainMode();
    return;
  }
  if (action === 'refresh') {
    await refreshDashboard(true);
    return;
  }
  if (action === 'check-indexer') {
    try {
      const candidate = state.settingsForm || clone(state.data?.settings || buildMockData().settings);
      if (backend()?.CheckIndexerConnection) {
        const result = await backend().CheckIndexerConnection(candidate);
        state.data = state.data || buildMockData();
        state.data.indexer = result;
        state.toast = result?.message || 'Indexer status checked';
      } else {
        state.toast = 'Indexer check unavailable';
      }
    } catch (error) {
      console.error(error);
      state.toast = error?.message || 'Unable to check indexer database';
    }
    render();
    setTimeout(() => {
      state.toast = '';
      render();
    }, 2400);
    return;
  }
  if (action === 'switch-wallet') {
    setActiveTab('wallet');
    return;
  }
  if (action === 'open-explorer') {
    try {
      const url = resolveExplorerURL(state.data?.settings || buildMockData().settings);
      BrowserOpenURL(url);
      state.toast = 'Explorer opened';
    } catch (error) {
      console.error(error);
      state.toast = 'Unable to open explorer';
    }
    render();
    setTimeout(() => {
      state.toast = '';
      render();
    }, 1800);
    return;
  }
  if (action === 'toggle-wallet-view') {
    state.walletExpanded = !state.walletExpanded;
    render();
    return;
  }
  if (action === 'copy-address') {
    await navigator.clipboard.writeText(state.data?.wallet?.address || '');
    state.toast = 'Wallet address copied';
    render();
    setTimeout(() => {
      state.toast = '';
      render();
    }, 1800);
    return;
  }
  if (action === 'toggle-qr') {
    state.qrOpen = true;
    render();
    return;
  }
  if (action === 'close-qr') {
    state.qrOpen = false;
    render();
    return;
  }
  if (action === 'clear-send') {
    state.sendForm = { to: '', amount: '', fee: state.sendForm.fee || '0.01', memo: '' };
    render();
    return;
  }
  if (action === 'set-filter') {
    setLogFilter(target.dataset.filter);
    return;
  }
  if (action === 'reconnect') {
    if (backend()?.ReconnectAPI) {
      state.data = await backend().ReconnectAPI();
      state.toast = 'API reconnected';
      render();
      setTimeout(() => {
        state.toast = '';
        render();
      }, 1800);
    } else {
      await refreshDashboard(true);
    }
    return;
  }
  if (action === 'toggle-miner') {
    const nextEnabled = target.dataset.enabled === 'true';
    try {
      if (backend()?.SetMiningEnabled) {
        const result = await backend().SetMiningEnabled(nextEnabled);
        state.toast = result?.message || (nextEnabled ? 'Mining enabled' : 'Mining paused');
      } else {
        state.toast = nextEnabled ? 'Mining enabled' : 'Mining paused';
      }
      await refreshDashboard(false);
    } catch (error) {
      console.error(error);
      state.toast = error?.message || 'Unable to update miner state';
    }
    render();
    setTimeout(() => {
      state.toast = '';
      render();
    }, 2200);
  }
});

appRoot.addEventListener('input', (event) => {
  const target = event.target;
  if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement)) {
    return;
  }
  if (target.form?.id === 'send-form') {
    state.sendForm[target.name] = target.value;
  }
  if (target.form?.id === 'settings-form') {
    state.settingsForm = state.settingsForm || clone(state.data?.settings || buildMockData().settings);
    if (target.type === 'checkbox') {
      state.settingsForm[target.name] = target.checked;
    } else {
      state.settingsForm[target.name] = target.value;
    }
  }
});

appRoot.addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  if (!(form instanceof HTMLFormElement)) return;

  if (form.id === 'send-form') {
    try {
      let result;
      if (backend()?.SendTransaction) {
        result = await backend().SendTransaction({
          to: state.sendForm.to,
          amount: Number(state.sendForm.amount || 0),
          fee: Number(state.sendForm.fee || 0),
        });
      } else {
        result = { success: true, message: 'Mock transaction sent' };
      }
      state.toast = result.success ? (result.message || 'Transaction sent') : (result.message || 'Transaction failed');
      if (result.success) {
        state.sendForm.to = '';
        state.sendForm.amount = '';
        state.sendForm.memo = '';
      }
      await refreshDashboard(false);
    } catch (error) {
      console.error(error);
      state.toast = 'Transaction failed';
    }
    render();
    setTimeout(() => {
      state.toast = '';
      render();
    }, 2400);
  }

  if (form.id === 'settings-form') {
    try {
      if (backend()?.SaveSettings) {
        const saved = await backend().SaveSettings(state.settingsForm);
        state.settingsForm = clone(saved);
        state.selectedIndexerEnabled = Boolean(saved.enableIndexer);
        if (state.data) {
          state.data.settings = saved;
        }
        if (backend()?.CheckIndexerConnection) {
          const indexer = await backend().CheckIndexerConnection(saved);
          if (state.data) {
            state.data.indexer = indexer;
          }
        }
      }
      state.toast = 'Settings saved';
      render();
      setTimeout(() => {
        state.toast = '';
        render();
      }, 1800);
    } catch (error) {
      console.error(error);
      state.toast = 'Unable to save settings';
      render();
    }
  }
});

async function boot() {
  try {
    if (backend()?.GetSettings) {
      const settings = await backend().GetSettings();
      state.settingsForm = clone(settings);
      state.selectedIndexerEnabled = Boolean(settings.enableIndexer);
      state.data = state.data || buildMockData();
      state.data.settings = settings;
      if (backend()?.CheckIndexerConnection) {
        try {
          const indexer = await backend().CheckIndexerConnection(settings);
          state.data.indexer = indexer;
        } catch (error) {
          console.error('Unable to load indexer state', error);
        }
      }
    }
  } catch (error) {
    console.error('Unable to load saved settings', error);
  }
  safeResizeWindow(SELECTOR_WINDOW);
  render();
  setInterval(() => {
    if (!state.modeSelectionOpen && !state.syncScreenOpen) {
      refreshDashboard(false);
    }
  }, DASHBOARD_REFRESH_MS);
}

boot();

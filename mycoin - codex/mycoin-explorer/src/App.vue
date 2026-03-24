<script setup>
import { ref, onMounted, computed } from "vue";

// --- 1. 狀態定義 ---
const mainBlocks = ref([]);
const mempoolTxs = ref([]);
const orphanBlocks = ref([]);
const dormantAddresses = ref([]);
const dormantMessage = ref("");
const currentPage = ref("explorer");
const searchInput = ref("");
const walletData = ref(null);
const showWallet = ref(false);
const successTxId = ref("");
const blockData = ref(null);
const txData = ref(null);
const showBlockTransactions = ref(false);
const closeTxModal = () => {
  txData.value = null;
};
const closeBlockModal = () => {
  blockData.value = null;
  showBlockTransactions.value = false;
};

const txForm = ref({
  to: "",
  amount: "",
  fee: 0.01,
});
const txMessage = ref("");
const txStatus = ref("");

// --- 2. 工具函數 ---

// ⌚ 全能時光機：支援 Mempool (字串) 與區塊 (數字)
const formatTime = (timestamp) => {
  if (!timestamp || timestamp === 0) return "Just now";
  const now = Math.floor(Date.now() / 1000);
  let diff;

  if (typeof timestamp === "string") {
    diff = now - Math.floor(new Date(timestamp).getTime() / 1000);
  } else {
    diff = now - timestamp;
  }

  if (diff < 0) return "Just now";
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return new Date(
    typeof timestamp === "number" ? timestamp * 1000 : timestamp,
  ).toLocaleDateString();
};

const toggleWallet = () => {
  showWallet.value = !showWallet.value;
};

const setPage = (page) => {
  currentPage.value = page;
};

const formatLastSeen = (lastSeen) => {
  if (!lastSeen || lastSeen === "Never") return "Never";
  return formatTime(lastSeen);
};

const formatDormantFor = (days) => {
  const numericDays = Number(days || 0);
  if (numericDays <= 0) return "Today";
  if (numericDays < 30) return `${numericDays} days`;
  if (numericDays < 365) return `${(numericDays / 30).toFixed(1)} months`;
  return `${(numericDays / 365).toFixed(1)} years`;
};

const dormantDistribution = computed(() => {
  const buckets = [
    { label: "0-30d", min: 0, max: 30, count: 0, totalBalance: 0 },
    { label: "31-90d", min: 31, max: 90, count: 0, totalBalance: 0 },
    { label: "91-180d", min: 91, max: 180, count: 0, totalBalance: 0 },
    { label: "181-365d", min: 181, max: 365, count: 0, totalBalance: 0 },
    { label: "1y+", min: 366, max: Number.POSITIVE_INFINITY, count: 0, totalBalance: 0 },
  ];

  for (const entry of dormantAddresses.value) {
    const days = Number(entry?.dormant_days || 0);
    const balance = Number(entry?.balance || 0);
    const bucket = buckets.find((item) => days >= item.min && days <= item.max);
    if (!bucket) continue;
    bucket.count += 1;
    bucket.totalBalance += balance;
  }

  return buckets;
});

const dormantMaxBucketCount = computed(() =>
  Math.max(...dormantDistribution.value.map((bucket) => bucket.count), 1),
);

const dormantBarStyle = (count) => ({
  width: `${Math.max((count / dormantMaxBucketCount.value) * 100, count > 0 ? 10 : 0)}%`,
});

const topDormantWhales = computed(() =>
  [...dormantAddresses.value]
    .sort((a, b) => Number(b?.balance || 0) - Number(a?.balance || 0))
    .slice(0, 5),
);

const isDormantWhale = (balance) => Number(balance || 0) >= 100;

const dormantBalanceStyle = (balance) => {
  const numericBalance = Number(balance || 0);
  if (numericBalance >= 250) {
    return { color: "#ffd166", fontWeight: 700 };
  }
  if (numericBalance >= 100) {
    return { color: "#ffb703", fontWeight: 700 };
  }
  return { color: "#f5f5f5", fontWeight: 600 };
};

const getBlockHash = (block) => block?.hash || block?.Hash || "Unknown";

const getBlockPrevHash = (block) =>
  block?.prevhash || block?.PrevHash || "Unknown";

const getBlockTxs = (block) =>
  block?.transactions || block?.Transactions || [];

const getBlockMiner = (block) => {
  const miner = block?.miner || block?.Miner;
  if (miner) return miner;

  const txs = getBlockTxs(block);
  if (!txs.length) return "Unknown";

  const outputs = txs[0].vout || txs[0].Outputs || [];
  return outputs.length ? outputs[0].to || outputs[0].To || "Unknown" : "Unknown";
};

const getBlockTxCount = (block) => getBlockTxs(block).length;

const getBlockReward = (block) => block?.reward ?? block?.Reward ?? 0;
const getBlockTarget = (block) => block?.target || block?.Target || "";

const getTxId = (tx) => tx?.txid || tx?.TxID || "Unknown";

const getTxInputs = (tx) => tx?.vin || tx?.Inputs || [];

const getTxOutputs = (tx) => tx?.vout || tx?.Outputs || [];

const getTxOutputAmount = (tx) =>
  getTxOutputs(tx).reduce(
    (sum, output) => sum + Number(output?.amount ?? output?.Amount ?? 0),
    0,
  );

const shortenHash = (value, size = 16) => {
  if (!value) return "Unknown";
  return value.length > size ? `${value.substring(0, size)}...` : value;
};

const getTxInputLabel = (input) => {
  const from = input?.from || input?.From;
  if (from) return from;

  const txid = input?.txid || input?.TxID || "";
  const index = input?.index ?? input?.Index;
  if (!txid) return "coinbase";

  return `${shortenHash(txid, 12)}:${index}`;
};

const getTxOutputTo = (output) => output?.to || output?.To || "Unknown";

const getTxOutputAmountValue = (output) =>
  Number(output?.amount ?? output?.Amount ?? 0);

const TARGET_BLOCK_TIME_SECONDS = 30;
const RETARGET_INTERVAL = 10;
const GENESIS_TARGET_HEX =
  "00000fffffffffffffffffffffffffffffffffffffffffffffffffffffffffff";

const recentBlocks = computed(() =>
  [...mainBlocks.value]
    .filter((block) => block && Number(block.Height ?? block.height ?? 0) >= 0)
    .sort(
      (a, b) => Number(b.Height ?? b.height ?? 0) - Number(a.Height ?? a.height ?? 0),
    ),
);

const averageBlockTimeSeconds = computed(() => {
  if (recentBlocks.value.length < 2) return null;

  const intervals = [];
  for (let i = 0; i < recentBlocks.value.length - 1; i++) {
    const newer = Number(recentBlocks.value[i].Timestamp ?? recentBlocks.value[i].timestamp ?? 0);
    const older = Number(
      recentBlocks.value[i + 1].Timestamp ?? recentBlocks.value[i + 1].timestamp ?? 0,
    );
    const diff = newer - older;
    if (diff > 0) {
      intervals.push(diff);
    }
  }

  if (!intervals.length) return null;
  return intervals.reduce((sum, value) => sum + value, 0) / intervals.length;
});

const parseHexBigInt = (hex) => {
  if (!hex) return null;
  const normalized = String(hex).trim().replace(/^0x/i, "");
  if (!normalized) return null;
  try {
    return BigInt(`0x${normalized}`);
  } catch {
    return null;
  }
};

const calculateWorkFromTarget = (targetHex) => {
  const target = parseHexBigInt(targetHex);
  if (target === null || target <= 0n) return null;
  return (1n << 256n) / (target + 1n);
};

const estimatedHashrate = computed(() => {
  const targetHex = getBlockTarget(recentBlocks.value[0]);
  const averageTime = averageBlockTimeSeconds.value;
  if (!targetHex || !averageTime || averageTime <= 0) return null;

  const work = calculateWorkFromTarget(targetHex);
  if (work === null) return null;

  const hashRate = Number(work) / averageTime;
  return Number.isFinite(hashRate) ? hashRate : null;
});

const currentDifficulty = computed(() => {
  const currentTarget = parseHexBigInt(getBlockTarget(recentBlocks.value[0]));
  const baseTarget = parseHexBigInt(GENESIS_TARGET_HEX);
  if (currentTarget === null || baseTarget === null || currentTarget <= 0n) return null;
  return Number(baseTarget) / Number(currentTarget);
});

const nextDifficultyEstimate = computed(() => {
  const averageTime = averageBlockTimeSeconds.value;
  if (!averageTime || averageTime <= 0) return null;

  const projectedRatio = TARGET_BLOCK_TIME_SECONDS / averageTime;
  const percentDelta = (projectedRatio - 1) * 100;
  const direction =
    Math.abs(percentDelta) < 0.5 ? "Stable" : percentDelta > 0 ? "Up" : "Down";

  return {
    direction,
    percentDelta,
  };
});

const minerDistribution = computed(() => {
  const counts = new Map();
  for (const block of recentBlocks.value) {
    const miner = getBlockMiner(block);
    const label = miner && miner !== "Unknown" ? miner : "Unknown";
    counts.set(label, (counts.get(label) || 0) + 1);
  }

  const total = recentBlocks.value.length || 1;
  return [...counts.entries()]
    .map(([miner, count]) => ({
      miner,
      count,
      share: (count / total) * 100,
    }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 6);
});

const activeMinerCount = computed(() => minerDistribution.value.length);

const formatHashrate = (value) => {
  if (!value || value <= 0) return "Unavailable";
  const units = ["H/s", "KH/s", "MH/s", "GH/s", "TH/s", "PH/s", "EH/s"];
  let scaled = value;
  let unitIndex = 0;
  while (scaled >= 1000 && unitIndex < units.length - 1) {
    scaled /= 1000;
    unitIndex++;
  }
  return `${scaled.toFixed(scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2)} ${units[unitIndex]}`;
};

const formatSeconds = (value) => {
  if (!value || value <= 0) return "Unavailable";
  if (value < 60) return `${value.toFixed(1)} sec`;
  return `${(value / 60).toFixed(1)} min`;
};

const formatDifficultyDelta = (estimate) => {
  if (!estimate) return "Unavailable";
  if (estimate.direction === "Stable") return "Stable";
  return `${estimate.direction} ${Math.abs(estimate.percentDelta).toFixed(1)}%`;
};

const minerShareStyle = (share) => ({
  width: `${Math.max(share, 6)}%`,
});

const writeTextToClipboard = async (text) => {
  if (!text) return false;

  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (err) {
      console.warn("Clipboard API failed, falling back:", err);
    }
  }

  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    textarea.style.pointerEvents = "none";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch (err) {
    console.error("Clipboard fallback failed:", err);
    return false;
  }
};

const copyToClipboard = async (text) => {
  try {
    const copied = await writeTextToClipboard(text);
    if (!copied) {
      return;
    }
    // 這裡可以用 alert，或是之後我們做個更漂亮的 Toast 提示
    alert("Copied to clipboard: " + text.substring(0, 8) + "...");
  } catch (err) {
    console.error("複製失敗:", err);
  }
};

// --- 3. API 請求函數 ---

const copyTransactionId = async (txid) => {
  await copyToClipboard(txid);
};

const apiUrl = (path) => new URL(path, window.location.origin).toString();

const fetchBlocks = async () => {
  try {
    const res = await fetch(apiUrl("/api/blocks"));
    if (res.ok) mainBlocks.value = await res.json();
  } catch (error) {
    console.error("API 連線失敗！", error);
  }
};

const fetchDormantAddresses = async () => {
  try {
    const res = await fetch(apiUrl("/api/dormant-addresses"));
    if (res.ok) {
      const data = await res.json();
      dormantAddresses.value = Array.isArray(data) ? data : [];
      dormantMessage.value = dormantAddresses.value.length
        ? ""
        : "No dormant addresses with remaining balance found yet.";
      return;
    }

    const data = await res.json().catch(() => ({}));
    dormantAddresses.value = [];
    dormantMessage.value = data.error || "Dormant addresses are unavailable.";
  } catch (error) {
    console.error("Dormant addresses API failed:", error);
    dormantAddresses.value = [];
    dormantMessage.value = "Dormant addresses are unavailable.";
  }
};

const fetchOrphans = async () => {
  try {
    const res = await fetch(apiUrl("/api/orphans"));
    if (res.ok) orphanBlocks.value = (await res.json()) || [];
  } catch (error) {
    console.error("孤塊連線失敗！", error);
  }
};

const fetchMempool = async () => {
  try {
    const res = await fetch(apiUrl("/api/mempool"));
    if (res.ok) {
      const data = await res.json();
      if (Array.isArray(data)) {
        data.sort((a, b) => (b.time || 0) - (a.time || 0));
        mempoolTxs.value = data;
      } else {
        // 🧹 探長防線：如果 Go 傳回 null (代表沒交易了)，強制清空畫面！
        mempoolTxs.value = [];
      }
    }
  } catch (error) {
    console.error("Mempool 連線失敗！", error);
  }
};

const fetchRecommendedFee = async () => {
  try {
    const res = await fetch(apiUrl("/api/estimatefee"));
    const data = await res.json();
    if (data.fee !== undefined) txForm.value.fee = data.fee;
  } catch (error) {
    console.error("無法取得預估手續費", error);
  }
};

const openTransactionDetails = async (txid) => {
  if (!txid || txid === "Unknown") return;

  walletData.value = null;
  txData.value = null;

  try {
    const res = await fetch(apiUrl(`/api/tx/${txid}`));
    if (res.ok) {
      txData.value = await res.json();
    } else {
      alert("Transaction not found.");
    }
  } catch (error) {
    console.error("Transaction lookup failed", error);
  }
};

// --- 4. 交互操作 ---

// 🌟 2. 雙引擎雷達 (REST API 版)
const handleSearch = async () => {
  const query = searchInput.value.trim();
  if (!query) return;

  showBlockTransactions.value = false;
  blockData.value = null;
  walletData.value = null;
  txData.value = null;

  try {
    if (/^\d+$/.test(query)) {
      const res = await fetch(apiUrl(`/api/block/${query}`));
      if (res.ok) {
        blockData.value = await res.json();
      } else {
        alert("Block not found.");
      }
    } else if (/^[0-9a-fA-F]{64}$/.test(query)) {
      const txRes = await fetch(apiUrl(`/api/tx/${query}`));
      if (txRes.ok) {
        txData.value = await txRes.json();
      } else {
        const blockRes = await fetch(apiUrl(`/api/block/${query}`));
        if (blockRes.ok) {
          blockData.value = await blockRes.json();
        } else {
          alert("Transaction or block not found.");
        }
      }
    } else {
      const res = await fetch(apiUrl(`/api/address/${query}`));
      if (res.ok) {
        walletData.value = await res.json();
      } else {
        alert("Address not found.");
      }
    }
  } catch (error) {
    console.error("Search failed", error);
  }
  searchInput.value = "";
  return;

  try {
    if (/^\d+$/.test(query)) {
      // 🕵️ 呼叫我們剛剛在 Go 裡寫好的 /api/tx/ 端點！
      const res = await fetch(apiUrl(`/api/tx/${query}`));
      if (res.ok) {
        txData.value = await res.json();
      } else {
        alert("找不到該筆交易！它可能不存在，或是節點尚未同步。");
      }
    } else {
      // 查地址維持不變
      const res = await fetch(apiUrl(`/api/address/${query}`));
      if (res.ok) {
        walletData.value = await res.json();
      } else {
        alert("無法查詢該地址！");
      }
    }
  } catch (error) {
    console.error("搜尋失敗", error);
  }
  searchInput.value = "";
};

// ==========================================
// 🚀 裝備二：發送交易的魔法函數 (升級自動清除訊息)
// ==========================================
const handleSendTx = async () => {
  if (!txForm.value.to || !txForm.value.amount || txForm.value.fee === "") {
    txMessage.value = "⚠️ Please fill in all fields!";
    txStatus.value = "error";

    // 🕵️ 探長魔法：3 秒後清除警告
    setTimeout(() => {
      txMessage.value = "";
    }, 3000);
    return;
  }

  try {
    const res = await fetch(apiUrl("/api/transaction"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        to: txForm.value.to,
        amount: parseFloat(txForm.value.amount),
        fee: parseFloat(txForm.value.fee),
      }),
    });

    const data = await res.json();
    if (res.ok && !data.error) {
      txMessage.value = "✅ Success! Tx sent to Mempool.";
      txStatus.value = "ok";
      txForm.value.to = "";
      txForm.value.amount = "";
      successTxId.value = data.txid;

      setTimeout(() => {
        fetchMempool();
        fetchRecommendedFee();
      }, 1000);

      // 🕵️ 探長魔法：成功後，5 秒自動把訊息變不見！
      setTimeout(() => {
        txMessage.value = "";
        txStatus.value = "";
        successTxId.value = "";
      }, 8000);
    } else {
      txMessage.value = `❌ Failed: ${data.error || "Unknown error"}`;
      txStatus.value = "error";
      setTimeout(() => {
        txMessage.value = "";
      }, 5000); // 錯誤訊息也定時清除
    }
  } catch (error) {
    txMessage.value = "❌ Network error!";
    txStatus.value = "error";
    setTimeout(() => {
      txMessage.value = "";
    }, 5000);
  }
};

// --- 5. 🚀 引擎啟動：生命週期鉤子 ---
onMounted(() => {
  fetchDormantAddresses();
  fetchBlocks();
  fetchOrphans();
  fetchMempool();
  fetchRecommendedFee();

  // 每 10 秒自動刷新一次數據，讓瀏覽器動起來！
  setInterval(() => {
    fetchDormantAddresses();
    fetchBlocks();
    fetchOrphans();
    fetchMempool();
  }, 10000);
});
</script>

<template>
  <div class="btc-explorer">
    <header class="header">
      <div class="logo-area">
        <span class="yicoin-logo">Ⓨ</span>
        <h1>YiCoin Explorer</h1>
      </div>

      <div class="search-bar">
        <input
          v-model="searchInput"
          type="text"
          placeholder="Search Hash, Height or Address..."
          @keyup.enter="handleSearch"
        />
        <button @click="handleSearch">Search</button>
        <button
          @click="toggleWallet"
          class="wallet-toggle-btn"
          :class="{ 'btn-active': showWallet }"
        >
          {{ showWallet ? "✖ Close" : "🚀 Send" }}
        </button>
      </div>

      <div class="network-status desktop-only">
        <span class="status-dot"></span> Mainnet
      </div>
    </header>

    <main class="container">
      <div class="content-wrapper">
        <div class="page-tabs">
          <button
            class="page-tab-btn"
            :class="{ 'page-tab-active': currentPage === 'explorer' }"
            @click="setPage('explorer')"
          >
            Explorer
          </button>
          <button
            class="page-tab-btn"
            :class="{ 'page-tab-active': currentPage === 'dormant' }"
            @click="setPage('dormant')"
          >
            Dormant
          </button>
        </div>
        <div
          v-if="currentPage === 'dormant'"
          class="table-card"
          style="margin-bottom: 20px; padding: 20px"
        >
          <div class="card-header">
            <h2>Dormant Addresses</h2>
          </div>
          <div
            style="
              color: #8f8f8f;
              font-size: 0.92rem;
              margin-bottom: 16px;
            "
          >
            Addresses with remaining balance, ranked by oldest recent activity.
          </div>
          <div
            v-if="dormantAddresses.length"
            style="
              margin-bottom: 20px;
              padding: 18px;
              border: 1px solid #303030;
              border-radius: 14px;
              background: rgba(255, 255, 255, 0.02);
            "
          >
            <div
              style="
                display: flex;
                justify-content: space-between;
                align-items: center;
                gap: 12px;
                margin-bottom: 14px;
                flex-wrap: wrap;
              "
            >
              <div>
                <div
                  style="
                    color: #f5f5f5;
                    font-size: 1rem;
                    font-weight: 700;
                    margin-bottom: 4px;
                  "
                >
                  Dormancy Distribution
                </div>
                <div style="color: #8f8f8f; font-size: 0.86rem">
                  Address count grouped by time since last activity.
                </div>
              </div>
              <div style="color: #8f8f8f; font-size: 0.86rem">
                {{ dormantAddresses.length }} tracked dormant addresses
              </div>
            </div>

            <div style="display: flex; flex-direction: column; gap: 12px">
              <div
                v-for="bucket in dormantDistribution"
                :key="bucket.label"
                style="
                  display: grid;
                  grid-template-columns: 88px minmax(0, 1fr) 90px;
                  gap: 12px;
                  align-items: center;
                "
              >
                <div style="color: #cfcfcf; font-size: 0.9rem; font-weight: 600">
                  {{ bucket.label }}
                </div>
                <div
                  style="
                    height: 10px;
                    background: #1b1b1b;
                    border-radius: 999px;
                    overflow: hidden;
                  "
                >
                  <div
                    :style="dormantBarStyle(bucket.count)"
                    style="
                      height: 100%;
                      background: linear-gradient(90deg, #2ecc71, #8be9a8);
                      border-radius: 999px;
                    "
                  ></div>
                </div>
                <div
                  style="
                    color: #7fe7a0;
                    font-size: 0.9rem;
                    font-weight: 600;
                    text-align: right;
                  "
                >
                  {{ bucket.count }}
                </div>
              </div>
            </div>
          </div>
          <div
            v-if="topDormantWhales.length"
            style="
              margin-bottom: 20px;
              padding: 18px;
              border: 1px solid #303030;
              border-radius: 14px;
              background: rgba(255, 255, 255, 0.02);
            "
          >
            <div
              style="
                display: flex;
                justify-content: space-between;
                align-items: center;
                gap: 12px;
                margin-bottom: 14px;
                flex-wrap: wrap;
              "
            >
              <div>
                <div
                  style="
                    color: #f5f5f5;
                    font-size: 1rem;
                    font-weight: 700;
                    margin-bottom: 4px;
                  "
                >
                  Top Dormant Whales
                </div>
                <div style="color: #8f8f8f; font-size: 0.86rem">
                  Largest dormant balances with no recent movement.
                </div>
              </div>
              <div style="color: #ffb703; font-size: 0.86rem; font-weight: 700">
                Top {{ topDormantWhales.length }}
              </div>
            </div>

            <div style="display: flex; flex-direction: column; gap: 12px">
              <div
                v-for="entry in topDormantWhales"
                :key="`whale-${entry.address}`"
                style="
                  display: grid;
                  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
                  gap: 12px;
                  align-items: center;
                  padding: 12px 14px;
                  border-radius: 12px;
                  background: rgba(255, 255, 255, 0.03);
                  border: 1px solid #303030;
                "
              >
                <div style="display: flex; align-items: center; gap: 8px; min-width: 0">
                  <span
                    style="
                      background: rgba(255, 183, 3, 0.14);
                      color: #ffb703;
                      border: 1px solid rgba(255, 183, 3, 0.4);
                      border-radius: 999px;
                      padding: 2px 8px;
                      font-size: 0.72rem;
                      font-weight: 700;
                      flex-shrink: 0;
                    "
                  >
                    Whale
                  </span>
                  <span
                    :title="entry.address"
                    style="
                      color: #f5f5f5;
                      font-weight: 600;
                      overflow: hidden;
                      text-overflow: ellipsis;
                      white-space: nowrap;
                    "
                  >
                    {{ shortenHash(entry.address, 20) }}
                  </span>
                </div>

                <div
                  style="
                    color: #ffd166;
                    font-weight: 700;
                    text-align: right;
                  "
                >
                  {{ Number(entry.balance || 0).toFixed(2) }} YIC
                </div>

                <div
                  style="
                    color: #f39c12;
                    font-weight: 600;
                    text-align: right;
                  "
                >
                  {{ formatDormantFor(entry.dormant_days) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="dormantAddresses.length" class="table-responsive">
            <table class="btc-table">
              <thead>
                <tr>
                  <th>Address</th>
                  <th class="right-align">Balance</th>
                  <th>Last Active</th>
                  <th>Dormant For</th>
                  <th class="right-align desktop-only">TXs</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="entry in dormantAddresses" :key="entry.address">
                  <td class="hash" :title="entry.address">
                    <div style="display: flex; align-items: center; gap: 8px">
                      <span style="cursor: help">
                        {{ shortenHash(entry.address, 18) }}
                      </span>
                      <span
                        v-if="isDormantWhale(entry.balance)"
                        style="
                          background: rgba(255, 183, 3, 0.14);
                          color: #ffb703;
                          border: 1px solid rgba(255, 183, 3, 0.4);
                          border-radius: 999px;
                          padding: 2px 8px;
                          font-size: 0.72rem;
                          font-weight: 700;
                        "
                      >
                        Whale
                      </span>
                      <button
                        @click="copyToClipboard(entry.address)"
                        class="copy-btn"
                        title="Copy Address"
                      >
                        📋
                      </button>
                    </div>
                  </td>
                  <td
                    class="right-align"
                    :style="dormantBalanceStyle(entry.balance)"
                  >
                    {{ Number(entry.balance || 0).toFixed(2) }} YIC
                  </td>
                  <td style="color: #b0b0b0">
                    {{ formatTime(entry.last_active) }}
                  </td>
                  <td style="color: #f39c12; font-weight: 600">
                    {{ formatDormantFor(entry.dormant_days) }}
                  </td>
                  <td class="right-align desktop-only">
                    {{ entry.tx_count }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else style="color: #999">{{ dormantMessage }}</div>
        </div>

        <template v-else>
        <div
          v-if="blockData"
          class="transfer-card"
          style="margin-bottom: 20px; position: relative"
        >
          <div
            style="
              display: flex;
              align-items: center;
              justify-content: space-between;
              gap: 10px;
            "
          >
            <h3 style="color: #f7931a; margin: 0">Block Details</h3>
            <div style="display: flex; gap: 8px; align-items: center">
              <button
                @click="showBlockTransactions = !showBlockTransactions"
                class="copy-btn"
                :title="
                  showBlockTransactions
                    ? 'Hide block transactions'
                    : 'Show block transactions'
                "
              >
                {{ showBlockTransactions ? "📕" : "📖" }}
              </button>
              <button
                @click="showBlockTransactions = !showBlockTransactions"
                style="
                  width: 34px;
                  height: 34px;
                  background: rgba(255, 215, 0, 0.12);
                  border: 1px solid rgba(255, 215, 0, 0.35);
                  color: #ffd700;
                  border-radius: 999px;
                  cursor: pointer;
                  font-size: 0.75rem;
                  font-weight: 800;
                "
                :title="
                  showBlockTransactions
                    ? 'Hide block transactions'
                    : 'Show block transactions'
                "
              >
                TX
              </button>
              <button
                @click="closeBlockModal"
                style="
                  background: rgba(220, 38, 38, 0.12);
                  border: 1px solid rgba(248, 113, 113, 0.45);
                  color: #f87171;
                  cursor: pointer;
                  font-size: 1rem;
                  font-weight: 700;
                  width: 34px;
                  height: 34px;
                  border-radius: 999px;
                  display: inline-flex;
                  align-items: center;
                  justify-content: center;
                "
              >
                ✕
              </button>
            </div>
          </div>

          <div
            style="
              background: rgba(0, 0, 0, 0.2);
              padding: 15px;
              border-radius: 8px;
              margin-top: 15px;
            "
          >
            <p style="margin: 0 0 10px 0">
              <strong>Height:</strong> {{ blockData.height ?? blockData.Height }}
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Hash:</strong>
              <span class="hash">{{ getBlockHash(blockData) }}</span>
              <button
                @click="copyToClipboard(getBlockHash(blockData))"
                class="copy-btn"
                style="margin-left: 8px"
                title="Copy Block Hash"
              >
                📋
              </button>
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Prev Hash:</strong>
              <span class="hash">{{ getBlockPrevHash(blockData) }}</span>
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Timestamp:</strong>
              {{
                formatTime(
                  blockData.timestamp || blockData.Timestamp || blockData.time,
                )
              }}
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Nonce:</strong>
              {{ blockData.nonce ?? blockData.Nonce ?? "Unknown" }}
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Miner:</strong> {{ getBlockMiner(blockData) }}
            </p>
            <p style="margin: 0 0 10px 0">
              <strong>Reward:</strong> {{ Number(getBlockReward(blockData)).toFixed(2) }} YIC
            </p>
            <p style="margin: 0">
              <strong>Transactions:</strong>
              {{ getBlockTxCount(blockData) }}
            </p>

          </div>
        </div>

        <div
          v-if="showBlockTransactions && blockData && getBlockTxCount(blockData)"
          style="
            position: fixed;
            inset: 0;
            background: rgba(0, 0, 0, 0.6);
            display: flex;
            align-items: center;
            justify-content: center;
            z-index: 50;
            padding: 24px;
          "
        >
          <div
            style="
              width: min(920px, 100%);
              max-height: 80vh;
              overflow-y: auto;
              background: #171717;
              border: 1px solid rgba(255, 215, 0, 0.2);
              border-radius: 14px;
              box-shadow: 0 16px 40px rgba(0, 0, 0, 0.45);
              padding: 18px;
            "
          >
            <div
              style="
                display: flex;
                align-items: center;
                justify-content: space-between;
                gap: 12px;
                margin-bottom: 14px;
              "
            >
              <h4 style="margin: 0; color: #ffd700">
                Transactions in Block {{ blockData.height ?? blockData.Height }}
              </h4>
              <button
                @click="showBlockTransactions = false"
                style="
                  background: transparent;
                  border: none;
                  color: #d4d4d4;
                  cursor: pointer;
                  font-size: 1.2rem;
                "
                title="Close transactions"
              >
                ✕
              </button>
            </div>

            <details
              v-for="tx in getBlockTxs(blockData)"
              :key="getTxId(tx)"
              style="
                background: rgba(0, 0, 0, 0.22);
                border: 1px solid rgba(255, 255, 255, 0.06);
                border-radius: 8px;
                padding: 12px;
                margin-bottom: 10px;
              "
            >
              <summary
                style="
                  display: flex;
                  justify-content: space-between;
                  align-items: center;
                  gap: 12px;
                  cursor: pointer;
                  list-style: none;
                "
              >
                <div
                  style="
                    display: flex;
                    align-items: center;
                    gap: 8px;
                    min-width: 0;
                    flex-shrink: 0;
                  "
                >
                  <span class="hash" :title="getTxId(tx)">
                    {{ shortenHash(getTxId(tx), 16) }}
                  </span>
                  <button
                    @click.stop="copyTransactionId(getTxId(tx))"
                    class="copy-btn"
                    title="Copy Transaction ID"
                  >
                    📋
                  </button>
                </div>
                <div
                  style="
                    display: grid;
                    grid-template-columns: repeat(3, minmax(0, 1fr));
                    gap: 10px;
                    color: #aaa;
                    font-size: 0.9em;
                    flex: 1;
                    max-width: 360px;
                  "
                >
                  <div>Inputs: {{ getTxInputs(tx).length }}</div>
                  <div>Outputs: {{ getTxOutputs(tx).length }}</div>
                  <div>Amount: {{ getTxOutputAmount(tx).toFixed(2) }} YIC</div>
                </div>
              </summary>

              <div style="margin-top: 12px">
                <div style="display: flex; gap: 8px; margin-bottom: 12px">
                  <button
                    @click="copyTransactionId(getTxId(tx))"
                    class="copy-btn"
                    title="Copy Transaction ID"
                  >
                    📋
                  </button>
                  <button
                    @click="openTransactionDetails(getTxId(tx))"
                    class="copy-btn"
                    title="Open Transaction Details"
                  >
                    🔎
                  </button>
                </div>

                <div
                  style="
                    display: grid;
                    grid-template-columns: repeat(2, minmax(0, 1fr));
                    gap: 16px;
                  "
                >
                  <div>
                    <h5 style="margin: 0 0 8px 0; color: #ffd700">Inputs</h5>
                    <div
                      v-for="(input, index) in getTxInputs(tx)"
                      :key="`${getTxId(tx)}-in-${index}`"
                      style="
                        background: rgba(255, 255, 255, 0.03);
                        border-radius: 6px;
                        padding: 8px 10px;
                        margin-bottom: 8px;
                        color: #bbb;
                        font-size: 0.9em;
                      "
                    >
                      {{ getTxInputLabel(input) }}
                    </div>
                  </div>

                  <div>
                    <h5 style="margin: 0 0 8px 0; color: #ffd700">Outputs</h5>
                    <div
                      v-for="(output, index) in getTxOutputs(tx)"
                      :key="`${getTxId(tx)}-out-${index}`"
                      style="
                        background: rgba(255, 255, 255, 0.03);
                        border-radius: 6px;
                        padding: 8px 10px;
                        margin-bottom: 8px;
                        color: #bbb;
                        font-size: 0.9em;
                      "
                    >
                      <div>{{ getTxOutputTo(output) }}</div>
                      <div style="margin-top: 4px; color: #4ade80">
                        {{ getTxOutputAmountValue(output).toFixed(2) }} YIC
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </details>
          </div>
        </div>

        <div v-if="walletData" class="wallet-card">
          <div class="wallet-header">
            <h2>💼 Wallet Details</h2>
            <button class="close-btn" @click="walletData = null">
              ✖ Close
            </button>
          </div>
          <div class="wallet-info">
            <p>
              <strong>Address:</strong>
              <span class="hash">{{ walletData.Address }}</span>
            </p>
            <p>
              <strong>Balance:</strong>
              <span class="balance"
                >{{ Number(walletData.Balance).toFixed(2) }} YiCoin</span
              >
            </p>
            <p v-if="walletData.Message" class="empty-msg">
              {{ walletData.Message }}
            </p>
          </div>
        </div>

        <div
          v-if="txData"
          class="transfer-card"
          style="margin-bottom: 20px; position: relative"
        >
          <button
            @click="closeTxModal"
            style="
              position: absolute;
              top: 15px;
              right: 15px;
              background: transparent;
              border: none;
              cursor: pointer;
              font-size: 1.2rem;
              opacity: 0.7;
            "
          >
            ✖️
          </button>

          <h3 style="color: #f7931a; margin-top: 0">🧾 Transaction Receipt</h3>

          <div
            style="
              background: rgba(0, 0, 0, 0.2);
              padding: 15px;
              border-radius: 8px;
              margin-top: 15px;
            "
          >
            <p
              style="
                margin: 0 0 10px 0;
                display: flex;
                align-items: center;
                gap: 8px;
              "
            >
              <strong>TxID:</strong>
              <span
                style="font-family: monospace; color: #aaa; cursor: help"
                :title="txData.txid"
              >
                {{ txData.txid ? txData.txid.substring(0, 16) : "Unknown" }}...
              </span>
              <button
                @click="copyTransactionId(txData.txid)"
                class="copy-btn"
                title="Copy Transaction ID"
              >
                📋
              </button>
            </p>

            <p style="margin: 0 0 15px 0">
              <strong>Status:</strong>
              <span
                v-if="txData.amount_sent !== undefined"
                style="color: #4ade80"
                >✅ Confirmed / Tracked</span
              >
              <span v-else style="color: #f7931a">⏳ Processing...</span>
            </p>

            <hr
              style="
                border: 0;
                border-top: 1px dashed rgba(255, 255, 255, 0.1);
                margin: 15px 0;
              "
            />

            <p style="margin: 0 0 8px 0; font-size: 0.9em">
              <strong style="display: inline-block; width: 60px">From:</strong>
              <span style="color: #aaa; font-family: monospace">{{
                txData.sender || "Unknown"
              }}</span>
            </p>
            <p style="margin: 0 0 15px 0; font-size: 0.9em">
              <strong style="display: inline-block; width: 60px">To:</strong>
              <span style="color: #aaa; font-family: monospace">{{
                txData.receiver || "Multiple / Unknown"
              }}</span>
            </p>

            <div
              style="
                background: rgba(0, 0, 0, 0.3);
                padding: 12px;
                border-radius: 6px;
                border-left: 4px solid #4ade80;
              "
            >
              <p
                style="
                  margin: 0 0 8px 0;
                  display: flex;
                  justify-content: space-between;
                  align-items: center;
                "
              >
                <strong>{{
                  txData.is_coinbase ? "⛏️ Block Reward" : "💸 Amount Sent"
                }}</strong>
                <span
                  style="color: #4ade80; font-weight: bold; font-size: 1.2em"
                >
                  {{
                    txData.amount_sent ? txData.amount_sent.toFixed(2) : "0.00"
                  }}
                  YIC
                </span>
              </p>

              <p
                v-if="!txData.is_coinbase && txData.network_fee !== undefined"
                style="
                  margin: 0 0 8px 0;
                  display: flex;
                  justify-content: space-between;
                  color: #aaa;
                  font-size: 0.9em;
                "
              >
                <span>Network Fee</span>
                <span>{{ txData.network_fee.toFixed(2) }} YIC</span>
              </p>

              <p
                v-if="txData.change > 0"
                style="
                  margin: 0;
                  display: flex;
                  justify-content: space-between;
                  color: #888;
                  font-size: 0.9em;
                "
              >
                <span>Change Returned</span>
                <span>{{ txData.change.toFixed(2) }} YIC</span>
              </p>
            </div>
          </div>
        </div>

        <transition name="wallet-slide">
          <div v-if="showWallet" class="transfer-card">
            <div class="card-header">
              <h2>💸 Send YiCoin (From Node Wallet)</h2>
            </div>
            <div class="transfer-form">
              <div class="input-row">
                <div class="input-group">
                  <label>Recipient Address (To)</label>
                  <input
                    v-model="txForm.to"
                    type="text"
                    placeholder="e.g. 19QoLXuub8kGUy..."
                  />
                </div>
                <div class="input-group amount-group">
                  <label>Amount</label>
                  <input
                    v-model="txForm.amount"
                    type="number"
                    step="0.1"
                    placeholder="0.0"
                  />
                </div>
                <div class="input-group amount-group">
                  <label>Fee (Auto)</label>
                  <input v-model="txForm.fee" type="number" step="1" />
                </div>
                <button class="send-btn" @click="handleSendTx">🚀 Send</button>
              </div>
              <div
                v-if="txMessage"
                :class="{
                  'success-msg': txStatus === 'ok',
                  'error-msg': txStatus === 'error',
                }"
                style="
                  display: flex;
                  align-items: center;
                  justify-content: space-between;
                  padding: 10px;
                  margin-top: 15px;
                  border-radius: 4px;
                "
              >
                <span>{{ txMessage }}</span>

                <div
                  v-if="successTxId"
                  style="display: flex; align-items: center; gap: 6px"
                >
                  <span
                    style="font-family: monospace; cursor: help; opacity: 0.9"
                    :title="successTxId"
                  >
                    TxID: {{ successTxId.substring(0, 8) }}...
                  </span>
                  <button
                    @click="copyToClipboard(successTxId)"
                    class="copy-btn"
                    title="Copy TxID"
                    style="opacity: 0.8; visibility: visible"
                  >
                    📋
                  </button>
                </div>
              </div>
            </div>
          </div>
        </transition>
        <div class="dashboard-layout">
          <div class="dashboard-row">
            <div
              class="stats-column"
              style="flex: 1; display: flex; flex-direction: column; gap: 30px"
            >
              <div
                class="table-card stats-column"
                style="
                  flex: 1;
                  padding: 25px;
                  display: flex;
                  flex-direction: column;
                "
              >
                <div style="margin-bottom: 25px">
                  <h2 style="color: #f39c12; margin-top: 0; font-size: 1.4rem">
                    Network Stats
                  </h2>
                  <div style="display: flex; flex-direction: column; gap: 14px">
                    <div>
                      <div
                        style="
                          font-size: 2rem;
                          font-weight: bold;
                          color: #fff;
                          line-height: 1.1;
                        "
                      >
                        {{ formatHashrate(estimatedHashrate) }}
                      </div>
                      <div style="color: #8f8f8f; font-size: 0.95rem">
                        Estimated Network Hashrate
                      </div>
                    </div>

                    <div
                      style="
                        display: grid;
                        grid-template-columns: repeat(2, minmax(0, 1fr));
                        gap: 12px;
                      "
                    >
                      <div
                        style="
                          background: rgba(255, 255, 255, 0.03);
                          border: 1px solid #303030;
                          border-radius: 12px;
                          padding: 12px 14px;
                        "
                      >
                        <div style="color: #8f8f8f; font-size: 0.85rem">
                          Current Difficulty
                        </div>
                        <div
                          style="
                            color: #f5f5f5;
                            font-size: 1.05rem;
                            font-weight: 600;
                            margin-top: 6px;
                          "
                        >
                          {{
                            currentDifficulty !== null
                              ? `${currentDifficulty.toFixed(2)}x`
                              : "Unavailable"
                          }}
                        </div>
                      </div>

                      <div
                        style="
                          background: rgba(255, 255, 255, 0.03);
                          border: 1px solid #303030;
                          border-radius: 12px;
                          padding: 12px 14px;
                        "
                      >
                        <div style="color: #8f8f8f; font-size: 0.85rem">
                          Average Block Time
                        </div>
                        <div
                          style="
                            color: #f5f5f5;
                            font-size: 1.05rem;
                            font-weight: 600;
                            margin-top: 6px;
                          "
                        >
                          {{ formatSeconds(averageBlockTimeSeconds) }}
                        </div>
                      </div>
                    </div>

                    <div
                      style="
                        background: rgba(255, 255, 255, 0.03);
                        border: 1px solid #303030;
                        border-radius: 12px;
                        padding: 12px 14px;
                      "
                    >
                      <div style="color: #8f8f8f; font-size: 0.85rem">
                        Next Difficulty Estimated
                      </div>
                      <div
                        style="
                          color: #7fd4ff;
                          font-size: 1.05rem;
                          font-weight: 600;
                          margin-top: 6px;
                        "
                      >
                        {{ formatDifficultyDelta(nextDifficultyEstimate) }}
                      </div>
                      <div
                        style="
                          color: #7a7a7a;
                          font-size: 0.82rem;
                          margin-top: 4px;
                        "
                      >
                        Based on the most recent {{ recentBlocks.length }} blocks
                      </div>
                    </div>
                  </div>
                </div>

                <hr
                  style="
                    border: 0;
                    border-top: 1px solid #333;
                    margin: 0 0 20px 0;
                    width: 100%;
                  "
                />

                <div style="flex: 1">
                  <h2
                    style="
                      color: #3498db;
                      margin-top: 0;
                      text-align: left;
                      font-size: 1.4rem;
                    "
                  >
                    🥧 Miner Pool Distribution
                  </h2>
                  <div
                    v-if="minerDistribution.length"
                    style="display: flex; flex-direction: column; gap: 14px"
                  >
                    <div
                      style="
                        color: #7a7a7a;
                        font-size: 0.88rem;
                        text-align: left;
                        margin-bottom: 2px;
                      "
                    >
                      Based on {{ recentBlocks.length }} recent blocks from
                      {{ activeMinerCount }} active miners.
                    </div>

                    <div
                      v-for="miner in minerDistribution"
                      :key="miner.miner"
                      style="
                        background: rgba(255, 255, 255, 0.03);
                        border: 1px solid #303030;
                        border-radius: 12px;
                        padding: 12px 14px;
                      "
                    >
                      <div
                        style="
                          display: flex;
                          justify-content: space-between;
                          align-items: center;
                          gap: 10px;
                          margin-bottom: 10px;
                        "
                      >
                        <div
                          style="
                            color: #f5f5f5;
                            font-weight: 600;
                            text-align: left;
                            word-break: break-word;
                          "
                        >
                          {{ miner.miner }}
                        </div>
                        <div
                          style="
                            color: #7fd4ff;
                            font-size: 0.9rem;
                            white-space: nowrap;
                          "
                        >
                          {{ miner.count }} blocks
                        </div>
                      </div>

                      <div
                        style="
                          height: 8px;
                          background: #1b1b1b;
                          border-radius: 999px;
                          overflow: hidden;
                        "
                      >
                        <div
                          :style="minerShareStyle(miner.share)"
                          style="
                            height: 100%;
                            background: linear-gradient(90deg, #3498db, #6dd5fa);
                            border-radius: 999px;
                          "
                        ></div>
                      </div>

                      <div
                        style="
                          margin-top: 8px;
                          color: #9a9a9a;
                          font-size: 0.85rem;
                          text-align: left;
                        "
                      >
                        {{ miner.share.toFixed(1) }}% share
                      </div>
                    </div>
                  </div>
                  <div
                    v-else
                    style="
                      padding: 40px 0;
                      color: #7a7a7a;
                      font-size: 1rem;
                      line-height: 1.6;
                      text-align: center;
                    "
                  >
                    Not enough recent blocks yet to estimate miner distribution.
                  </div>
                </div>
              </div>
            </div>
            <div class="table-card blocks-card" style="flex: 2">
              <div class="table-header">
                <h2>📦 Latest Blocks</h2>
              </div>
              <div class="table-responsive">
                <table class="btc-table">
                  <thead>
                    <tr>
                      <th>Height</th>
                      <th>Hash</th>
                      <th>Miner</th>
                      <th>Time</th>
                      <th class="right-align desktop-only">TXs</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="block in mainBlocks" :key="block.Hash">
                      <td class="height">
                        <a href="#">{{ block.Height }}</a>
                      </td>
                      <td class="hash" :title="block.Hash">
                        <div
                          style="display: flex; align-items: center; gap: 8px"
                        >
                          <span style="cursor: help">
                            {{
                              block.Hash
                                ? block.Hash.substring(0, 8)
                                : "Unknown"
                            }}...
                          </span>

                          <button
                            @click="copyToClipboard(block.Hash)"
                            class="copy-btn"
                            title="Copy Full Hash"
                          >
                            📋
                          </button>
                        </div>
                      </td>
                      <td class="miner">
                        <div
                          style="display: flex; align-items: center; gap: 8px"
                        >
                          <span
                            class="miner-tag"
                            :title="block.Miner"
                            style="cursor: help"
                          >
                            {{
                              block.Miner
                                ? block.Miner.substring(0, 8)
                                : "Unknown"
                            }}...
                          </span>
                          <button
                            @click="copyToClipboard(block.Miner)"
                            class="copy-btn"
                            title="Copy Miner Address"
                          >
                            📋
                          </button>
                        </div>
                      </td>
                      <td style="color: #888; font-size: 0.9em">
                        {{ formatTime(block.Timestamp) }}
                      </td>
                      <td class="tx right-align desktop-only">
                        {{ block.TxCount }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
          <div class="dashboard-row">
            <div class="table-card mempool-card">
              <div class="table-header">
                <h2>⏳ Mempool (Unconfirmed Txs)</h2>
              </div>
              <div class="table-responsive">
                <table class="btc-table">
                  <thead>
                    <tr>
                      <th class="left-align">TxID</th>
                      <th class="right-align">Amount</th>
                      <th class="right-align">Time</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="tx in mempoolTxs" :key="tx.txid">
                      <td class="hash" :title="tx.txid">
                        <div
                          style="display: flex; align-items: center; gap: 8px"
                        >
                          <span
                            style="cursor: pointer"
                            @click="copyTransactionId(tx.txid)"
                          >
                            {{
                              tx.txid ? tx.txid.substring(0, 12) : "Unknown"
                            }}...
                          </span>

                          <button
                            @click="copyTransactionId(tx.txid)"
                            class="copy-btn"
                            title="Copy Full TxID"
                          >
                            📋
                          </button>
                        </div>
                      </td>
                      <td class="right-align" style="color: #ffd700">
                        {{ tx.amount ? tx.amount.toFixed(2) : "0.00" }} YiCoin
                      </td>
                      <td
                        class="right-align"
                        style="color: #888; font-size: 0.85em"
                      >
                        {{ tx.time ? formatTime(tx.time) : "Just now" }}
                      </td>
                    </tr>
                    <tr v-if="mempoolTxs.length === 0">
                      <td colspan="3" class="empty-state">
                        No unconfirmed txs.
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <div class="table-card orphan-card">
              <div class="table-header">
                <h2>👻 Orphan Blocks</h2>
              </div>
              <div class="table-responsive">
                <table class="btc-table">
                  <thead>
                    <tr>
                      <th>Height</th>
                      <th>Hash</th>
                      <th>Miner</th>
                      <th>Time</th>
                      <th class="right-align desktop-only">TXs</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="block in orphanBlocks" :key="block.Hash">
                      <td class="height">
                        <span style="color: #ff6b6b; font-weight: bold">{{
                          block.Height
                        }}</span>
                      </td>
                      <td class="hash" :title="block.Hash">
                        <div
                          style="display: flex; align-items: center; gap: 8px"
                        >
                          <span style="cursor: help">
                            {{
                              block.Hash
                                ? block.Hash.substring(0, 8)
                                : "Unknown"
                            }}...
                          </span>

                          <button
                            @click="copyToClipboard(block.Hash)"
                            class="copy-btn"
                            title="Copy Full Block Hash"
                          >
                            📋
                          </button>
                        </div>
                      </td>
                      <td class="miner">
                        <div
                          v-if="block.Miner"
                          style="display: flex; align-items: center; gap: 8px"
                        >
                          <span
                            class="miner-tag"
                            :title="block.Miner"
                            style="
                              cursor: help;
                              background-color: rgba(255, 107, 107, 0.1);
                              border: 1px solid #ff6b6b;
                              color: #ff6b6b;
                            "
                          >
                            {{ block.Miner.substring(0, 8) }}...
                          </span>

                          <button
                            @click="copyToClipboard(block.Miner)"
                            class="copy-btn"
                            title="Copy Miner Address"
                          >
                            📋
                          </button>
                        </div>
                      </td>
                      <td
                        style="color: #ff6b6b; font-size: 0.9em; opacity: 0.8"
                      >
                        {{ formatTime(block.Timestamp) }}
                      </td>
                      <td class="tx right-align desktop-only">
                        {{
                          block.TxCount ||
                          (block.Transactions ? block.Transactions.length : 0)
                        }}
                      </td>
                    </tr>
                    <tr v-if="orphanBlocks.length === 0">
                      <td colspan="5" class="empty-state">
                        No orphan blocks in memory.
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
        </template>
      </div>
    </main>
  </div>
</template>

<style scoped>
/* =========================================
   YiCoin 專屬風格 (致敬 Bitcoin)
   ========================================= */
.btc-explorer {
  font-family:
    "Inter",
    -apple-system,
    sans-serif;
  background-color: #111111;
  color: #ffffff;
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.header {
  background-color: #1a1a1a;
  border-bottom: 2px solid #222222;
  padding: 15px 40px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 15px;
  flex-shrink: 0;
}
.logo-area {
  display: flex;
  align-items: center;
  gap: 12px;
}
.yicoin-logo {
  font-size: 2.5rem;
  color: #ffd700;
  font-weight: bold;
  text-shadow: 0 0 15px rgba(255, 215, 0, 0.4);
}
.logo-area h1 {
  font-size: 1.6rem;
  margin: 0;
  letter-spacing: 1px;
}

.search-bar {
  display: flex;
  flex: 1;
  max-width: 600px;
  min-width: 280px;
}
.search-bar input {
  flex: 1;
  padding: 10px 15px;
  background-color: #222;
  border: 1px solid #444;
  color: white;
  border-radius: 6px 0 0 6px;
  outline: none;
  width: 100%;
}
.search-bar input:focus {
  border-color: #ffd700;
}
.search-bar button {
  padding: 10px 20px;
  background-color: #ffd700;
  color: #111;
  border: none;
  font-weight: bold;
  cursor: pointer;
  border-radius: 0 6px 6px 0;
  transition: 0.2s;
}
.search-bar button:hover {
  background-color: #e6c200;
}

.network-status {
  color: #888;
  display: flex;
  align-items: center;
  gap: 8px;
}
.status-dot {
  width: 10px;
  height: 10px;
  background-color: #2ecc71;
  border-radius: 50%;
  box-shadow: 0 0 8px #2ecc71;
}

.container {
  max-width: 1400px;
  margin: 20px auto;
  padding: 0 20px;
  flex: 1;
  display: flex;
  width: 100%;
  box-sizing: border-box;
}
.content-wrapper {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: auto;
}
.page-tabs {
  display: flex;
  gap: 10px;
  margin-bottom: 20px;
}
.page-tab-btn {
  background-color: #1f1f1f;
  border: 1px solid #3a3a3a;
  color: #d5d5d5;
  border-radius: 999px;
  padding: 10px 18px;
  font-weight: 700;
  cursor: pointer;
  transition:
    background-color 0.2s ease,
    border-color 0.2s ease,
    color 0.2s ease;
}
.page-tab-btn:hover {
  border-color: #ffd700;
  color: #ffffff;
}
.page-tab-active {
  background-color: #ffd700;
  border-color: #ffd700;
  color: #111111;
}

/* 🌟 錢包卡片專屬樣式 */
.wallet-card {
  background: linear-gradient(135deg, #1e1e1e 0%, #2a2a2a 100%);
  border: 1px solid #ffd700;
  border-radius: 10px;
  padding: 20px;
  margin-bottom: 20px;
  box-shadow: 0 0 20px rgba(255, 215, 0, 0.15);
  animation: slideDown 0.3s ease-out;
  flex-shrink: 0;
}
.wallet-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  border-bottom: 1px solid #444;
  padding-bottom: 10px;
  margin-bottom: 15px;
}
.wallet-header h2 {
  margin: 0;
  color: #ffd700;
  font-size: 1.4rem;
}
.close-btn {
  background: none;
  border: none;
  color: #888;
  font-size: 1rem;
  cursor: pointer;
}
.close-btn:hover {
  color: #e74c3c;
}
.wallet-info p {
  font-size: 1.1rem;
  margin: 10px 0;
}
.balance {
  color: #2ecc71;
  font-weight: bold;
  font-size: 1.3rem;
  margin-left: 10px;
}

/* =========================================
   📊 探長修改：全新左右雙拼排版 (取代舊的 dashboard-grid)
   ========================================= */
.dashboard-layout {
  display: flex;
  flex-direction: column;
  gap: 30px;
  width: 100%;
}

.dashboard-layout .dashboard-row:first-child {
  margin-top: 20px !important; /* 👈 補上結尾的斜線 */
}

.dashboard-row {
  display: flex;
  gap: 30px;
  width: 100%;
  height: 530px; /* 👈 直接把高度釘死在外框！ */
  align-items: stretch; /* 強迫裡面的卡片上下填滿 */
  margin-bottom: 30px;
}

.table-card {
  height: 100% !important; /* 🔒 鎖死：絕對要等於外層的高度 (480px)，不准偷長高！ */
  margin: 0 !important; /* 🔪 拔除任何會推擠的外邊距 */
  flex: 1;
  min-width: 0;
  background-color: #1e1e1e;
  border-radius: 10px;
  border: 1px solid #333;
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  overflow: hidden; /* 確保內容不會溢出邊界 */
}
.table-header {
  padding: 15px 20px;
  border-bottom: 1px solid #333;
  background-color: #181818;
  flex-shrink: 0;
  border-radius: 10px 10px 0 0; /* 🕵️ 探長微調：上面邊角加一點圓潤 */
}
.table-header h2 {
  margin: 0;
  font-size: 1.2rem;
}
.table-responsive {
  flex: 1;
  overflow-y: auto;
}

/* =========================================
   📦 表格內容樣式 (完全保留你的原版)
   ========================================= */
.btc-table {
  width: 100%;
  border-collapse: collapse;
  text-align: left;
}
.btc-table th {
  padding: 12px 15px;
  color: #888;
  border-bottom: 2px solid #333;
  font-size: 0.9rem;
  background-color: #181818;
  position: sticky;
  top: 0;
  z-index: 10;
}
.btc-table td {
  padding: 12px 15px;
  border-bottom: 1px solid #2a2a2a;
  font-size: 0.9rem;
}
.btc-table tr:hover {
  background-color: #252525;
}

.height a {
  color: #ffd700;
  text-decoration: none;
  font-weight: bold;
}
.hash {
  font-family: monospace;
  color: #a0a0a0;
}
.miner-tag {
  background: #2c3e50;
  padding: 4px 6px;
  border-radius: 4px;
  font-family: monospace;
  font-size: 0.8rem;
}
.right-align {
  text-align: right;
}
.empty-state {
  text-align: center;
  padding: 30px !important;
  color: #666;
  font-style: italic;
}

::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}
::-webkit-scrollbar-track {
  background: transparent;
}
::-webkit-scrollbar-thumb {
  background: #444;
  border-radius: 4px;
}
::-webkit-scrollbar-thumb:hover {
  background: #ffd700;
}

@keyframes slideDown {
  from {
    opacity: 0;
    transform: translateY(-10px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* =========================================
   💸 轉帳卡片專屬樣式 (完全保留你的原版)
   ========================================= */
.wallet-slide-enter-active,
.wallet-slide-leave-active {
  transition: all 0.4s cubic-bezier(0.4, 0, 0.2, 1); /* 使用貝茲曲線讓動作更流暢 */
  max-height: 500px; /* 設定一個足夠撐開的高度值 */
  opacity: 1;
}

.wallet-slide-enter-from,
.wallet-slide-leave-to {
  max-height: 0;
  opacity: 0;
  transform: translateY(-20px); /* 消失時往上縮回 */
  margin-bottom: 0;
  overflow: hidden; /* 防止內容在縮放時溢出 */
}
.wallet-toggle-btn {
  margin-left: 12px;
  background-color: #2ecc71 !important;
  border: none;
  font-weight: bold;
  transition: all 0.3s ease;
}

.wallet-toggle-btn:hover {
  background-color: #27ae60 !important;
  transform: scale(1.05);
}

.btn-active {
  background-color: #e74c3c !important; /* 開啟時變紅色，方便關閉 */
}
.transfer-card {
  background-color: #1e1e1e;
  border: 1px solid #333;
  border-radius: 10px;
  padding: 20px;
  margin-bottom: 25px;
  box-shadow: 0 4px 10px rgba(0, 0, 0, 0.5);
  flex-shrink: 0;
}
.card-header h2 {
  margin: 0 0 15px 0;
  font-size: 1.3rem;
  color: #2ecc71;
}

.transfer-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.input-row {
  display: flex;
  gap: 15px;
  align-items: flex-end;
  flex-wrap: wrap;
}
.input-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex: 1;
  min-width: 200px;
}
.amount-group {
  flex: 0.3;
  min-width: 100px;
}

.input-group label {
  color: #aaa;
  font-size: 0.9rem;
  font-weight: bold;
}
.input-group input {
  padding: 12px;
  background: #111;
  border: 1px solid #444;
  color: white;
  border-radius: 6px;
  outline: none;
  font-size: 1rem;
  transition: border 0.2s;
}
.input-group input:focus {
  border-color: #2ecc71;
}

.send-btn {
  padding: 12px 25px;
  background: #2ecc71;
  color: #111;
  font-weight: bold;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  transition:
    background 0.2s,
    transform 0.1s;
  font-size: 1.1rem;
  height: 45px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.send-btn:hover {
  background: #27ae60;
  transform: translateY(-2px);
}
.send-btn:active {
  transform: translateY(0);
}

.success-msg {
  color: #2ecc71;
  font-weight: bold;
  margin-top: 10px;
  font-size: 0.95rem;
}
.error-msg {
  color: #e74c3c;
  font-weight: bold;
  margin-top: 10px;
  font-size: 0.95rem;
}

/* 📋 複製按鈕樣式 */
.copy-btn {
  background: transparent !important;
  border: none !important;
  padding: 2px 5px !important;
  cursor: pointer;
  font-size: 0.8rem;

  /* 🕵️ 探長秘訣：預設透明度為 0 */
  opacity: 0;
  transition:
    opacity 0.2s ease-in-out,
    transform 0.2s; /* 讓出現的過程變平滑 */

  display: flex;
  align-items: center;
}

/* 🔥 當滑鼠移入表格儲存格 (td) 時，讓按鈕現身 */
td:hover .copy-btn {
  opacity: 0.8; /* 隱約現身 */
  visibility: visible;
}

/* 🌟 當滑鼠直接指著按鈕時，全亮並放大 */
.copy-btn:hover {
  opacity: 1 !important;
  transform: scale(1.2);
}

.copy-btn:active {
  transform: scale(0.9);
}

/* =========================================
   📱 響應式設計整合 (將你原有的兩塊 media 完美合併)
   ========================================= */
@media (max-width: 1000px) {
  /* 🕵️ 探長微調：放寬到 1000px 讓新排版提早適應小螢幕 */
  .btc-explorer {
    height: auto;
    overflow: visible;
  }
  .header {
    flex-direction: column;
    align-items: flex-start;
    padding: 15px 20px;
  }
  .search-bar {
    width: 100%;
    max-width: 100%;
  }
  .container {
    display: block;
    height: auto;
    margin: 15px auto;
  }
  .content-wrapper {
    height: auto;
  }

  /* 🕵️ 探長修改：將 dashboard-grid 替換為我們新的 dashboard-row */
  .dashboard-row {
    flex-direction: column;
    gap: 20px;
    height: auto;
  }

  .table-card {
    height: 50vh;
  }
  .desktop-only {
    display: none;
  }

  /* 原本轉帳卡片的折疊設定 */
  .input-row {
    flex-direction: column;
    align-items: stretch;
  }
  .amount-group {
    flex: 1;
  }
}
</style>

<script setup>
import { ref, onMounted } from "vue";

// --- 1. 狀態定義 ---
const mainBlocks = ref([]);
const mempoolTxs = ref([]);
const orphanBlocks = ref([]);
const searchInput = ref("");
const walletData = ref(null);
const showWallet = ref(false);
const successTxId = ref("");
const txData = ref(null);
const closeTxModal = () => {
  txData.value = null;
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

const copyToClipboard = async (text) => {
  try {
    await navigator.clipboard.writeText(text);
    // 這裡可以用 alert，或是之後我們做個更漂亮的 Toast 提示
    alert("Copied to clipboard: " + text.substring(0, 8) + "...");
  } catch (err) {
    console.error("複製失敗:", err);
  }
};

// --- 3. API 請求函數 ---

const fetchBlocks = async () => {
  try {
    const res = await fetch("http://localhost:8080/api/blocks");
    if (res.ok) mainBlocks.value = await res.json();
  } catch (error) {
    console.error("API 連線失敗！", error);
  }
};

const fetchOrphans = async () => {
  try {
    const res = await fetch("http://localhost:8080/api/orphans");
    if (res.ok) orphanBlocks.value = (await res.json()) || [];
  } catch (error) {
    console.error("孤塊連線失敗！", error);
  }
};

const fetchMempool = async () => {
  try {
    const res = await fetch("http://localhost:8080/api/mempool");
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
    const res = await fetch("http://localhost:8080/api/estimatefee");
    const data = await res.json();
    if (data.fee !== undefined) txForm.value.fee = data.fee;
  } catch (error) {
    console.error("無法取得預估手續費", error);
  }
};

// --- 4. 交互操作 ---

// 🌟 2. 雙引擎雷達 (REST API 版)
const handleSearch = async () => {
  const query = searchInput.value.trim();
  if (!query) return;

  walletData.value = null;
  txData.value = null;

  try {
    if (query.length === 64) {
      // 🕵️ 呼叫我們剛剛在 Go 裡寫好的 /api/tx/ 端點！
      const res = await fetch(`http://localhost:8080/api/tx/${query}`);
      if (res.ok) {
        txData.value = await res.json();
      } else {
        alert("找不到該筆交易！它可能不存在，或是節點尚未同步。");
      }
    } else {
      // 查地址維持不變
      const res = await fetch(`http://localhost:8080/api/address/${query}`);
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
    const res = await fetch("http://localhost:8080/api/transaction", {
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
  fetchBlocks();
  fetchOrphans();
  fetchMempool();
  fetchRecommendedFee();

  // 每 10 秒自動刷新一次數據，讓瀏覽器動起來！
  setInterval(() => {
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

          <h3 style="color: #f7931a; margin-top: 0">Transaction Details</h3>

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
                {{ txData.txid.substring(0, 16) }}...
              </span>
              <button @click="copyToClipboard(txData.txid)" class="copy-btn">
                📋
              </button>
            </p>

            <p style="margin: 0 0 10px 0">
              <strong>Status:</strong>
              <span v-if="txData.blockHash" style="color: #4ade80"
                >✅ Confirmed</span
              >
              <span v-else style="color: #f7931a">⏳ In Mempool</span>
            </p>

            <div
              v-if="txData.blockHash"
              style="display: flex; gap: 30px; margin-bottom: 10px"
            >
              <p style="margin: 0">
                <strong>Block Height:</strong> {{ txData.blockHeight }}
              </p>
              <p style="margin: 0">
                <strong>Block Hash:</strong>
                <span
                  style="font-family: monospace; font-size: 0.9em; cursor: help"
                  :title="txData.blockHash"
                >
                  {{ txData.blockHash.substring(0, 10) }}...
                </span>
              </p>
            </div>

            <p
              v-if="txData.amount !== undefined"
              style="
                margin: 0;
                padding-top: 10px;
                border-top: 1px solid rgba(255, 255, 255, 0.1);
              "
            >
              <strong>Total Value:</strong>
              <span style="color: #4ade80; font-weight: bold; font-size: 1.1em">
                {{ txData.amount }} YIC
              </span>
            </p>
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
                    📈 Network Stats
                  </h2>
                  <div style="color: #aaa; font-size: 1rem; line-height: 1.8">
                    <span
                      style="font-size: 2rem; font-weight: bold; color: #fff"
                      >1155.67 EH/s</span
                    ><br />
                    Next Difficulty Estimated<br />
                    Average Block Time
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

                <div style="flex: 1; text-align: center">
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
                    style="
                      padding: 40px 0;
                      color: #3498db;
                      font-size: 1rem;
                      line-height: 1.6;
                    "
                  >
                    🚧 <b>圖表施工中預留位</b><br /><br />
                    Foundry USA / AntPool<br />
                    將在這裡完美呈現！
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
                          <span style="cursor: help">
                            {{
                              tx.txid ? tx.txid.substring(0, 12) : "Unknown"
                            }}...
                          </span>

                          <button
                            @click="copyToClipboard(tx.txid)"
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
                      <td colspan="4" class="empty-state">
                        No orphan blocks in memory.
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
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

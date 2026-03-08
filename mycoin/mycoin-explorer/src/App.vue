<script setup>
import { ref, onMounted } from "vue";

const mainBlocks = ref([]);
const mempoolTxs = ref([]);
const searchInput = ref("");
const walletData = ref(null);
const orphanBlocks = ref([]);

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
    if (res.ok) {
      const data = await res.json();
      orphanBlocks.value = data || [];
    }
  } catch (error) {
    console.error("孤塊連線失敗！", error);
  }
};

// 🌟 新增：真正去後端「查水表」的魔法
const handleSearch = async () => {
  const query = searchInput.value.trim();
  if (!query) return;

  try {
    const res = await fetch(`http://localhost:8080/api/address/${query}`);
    if (res.ok) {
      walletData.value = await res.json(); // 把查到的結果存起來顯示
    } else {
      alert("無法查詢該地址！");
    }
  } catch (error) {
    console.error("搜尋失敗", error);
  }
  searchInput.value = ""; // 清空搜尋框
};

// 💸 轉帳專用變數
// 💸 轉帳專用變數
const txForm = ref({
  to: "",
  amount: "",
  fee: 0.01, // 🚀 修正：預設為 0.01 YiCoin (底層的 1 YiCent)，這才合理！
});
const txMessage = ref("");
const txStatus = ref("");

// ==========================================
// 📡 裝備一：手續費雷達 (獨立出來)
// ==========================================
const fetchRecommendedFee = async () => {
  try {
    const res = await fetch("http://localhost:8080/api/estimatefee");
    const data = await res.json();
    if (data.fee !== undefined) {
      txForm.value.fee = data.fee; // 自動把報價填入輸入框！
    }
  } catch (error) {
    console.error("無法取得預估手續費", error);
  }
};

// 🚀 引擎啟動：一打開網頁就自動掃描一次手續費！
fetchRecommendedFee();

// 🌟 新增：去後端撈取 Mempool 的函數
// 🌟 現代化 REST 版本：清爽、簡單、直接！
const fetchMempool = async () => {
  try {
    const res = await fetch("http://localhost:8080/api/mempool");

    if (res.ok) {
      const data = await res.json();

      // 🕵️ 探長的時光排序魔法 (升級版：時間 + TxID 雙重防線)
      if (data && Array.isArray(data)) {
        data.sort((a, b) => {
          // 第一關：先比時間 (時間越大的越接近現在，排在越前面)
          const timeA = a.time || 0;
          const timeB = b.time || 0;

          if (timeB !== timeA) {
            return timeB - timeA; // 時間不一樣，誰晚來誰就在上面！
          }

          // 第二關：如果時間一模一樣 (例如重啟時同時載入的舊交易)
          // 就用 TxID 的字母順序來排，保證畫面永遠不會亂跳！
          if (a.txid && b.txid) {
            return a.txid.localeCompare(b.txid);
          }

          return 0;
        });
      }

      mempoolTxs.value = data || [];
    }
  } catch (error) {
    console.error("Mempool 連線失敗！", error);
  }
};

// ==========================================
// 🚀 裝備二：發送交易的魔法函數
// ==========================================
const handleSendTx = async () => {
  // 檢查有沒有填寫完整 (加入對 fee 的檢查)
  if (!txForm.value.to || !txForm.value.amount || txForm.value.fee === "") {
    txMessage.value = "⚠️ Please fill in all fields (including Fee)!";
    txStatus.value = "error";
    return;
  }

  try {
    // 呼叫我們在 Go 寫的轉帳 API
    const res = await fetch("http://localhost:8080/api/transaction", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        to: txForm.value.to,
        amount: parseFloat(txForm.value.amount),
        fee: parseFloat(txForm.value.fee), // 👈 大偵探修正：千萬別忘了把小費傳給後端！
      }),
    });

    const data = await res.json();
    if (res.ok && !data.error) {
      txMessage.value = `✅ Success! Tx sent to Mempool. TxID: ${data.txid.substring(0, 8)}...`;
      txStatus.value = "ok";

      // 清空輸入框，但保留最新的手續費報價！
      txForm.value.to = "";
      txForm.value.amount = "";

      // 發送成功後，重新抓取最新的區塊與手續費
      if (typeof fetchBlocks === "function") fetchBlocks();
      fetchRecommendedFee();
    } else {
      txMessage.value = `❌ Failed: ${data.error || "Unknown error"}`;
      txStatus.value = "error";
    }
  } catch (error) {
    console.error("轉帳失敗", error);
    txMessage.value = "❌ Network error! Is the Go server running?";
    txStatus.value = "error";
  }
};

// ==========================================
// 🕒 裝備三：時間格式化函數 (維持你原本的)
// ==========================================
const formatTime = (timestamp) => {
  const date = new Date(timestamp * 1000);
  return date.toLocaleString("zh-TW", { hour12: false });
};

onMounted(() => {
  fetchBlocks();
  fetchMempool(); // 👈 剛打開網頁時抓一次

  setInterval(() => {
    fetchBlocks();
    fetchMempool(); // 👈 每 3 秒抓一次
    fetchOrphans();
  }, 3000);

  fetchRecommendedFee();
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

        <div class="transfer-card">
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
            <p
              v-if="txMessage"
              :class="{
                'success-msg': txStatus === 'ok',
                'error-msg': txStatus === 'error',
              }"
            >
              {{ txMessage }}
            </p>
          </div>
        </div>
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
                      <th class="right-align desktop-only">TXs</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="block in mainBlocks" :key="block.Hash">
                      <td class="height">
                        <a href="#">{{ block.Height }}</a>
                      </td>
                      <td class="hash" :title="block.Hash" style="cursor: help">
                        {{
                          block.Hash ? block.Hash.substring(0, 8) : "Unknown"
                        }}...
                      </td>
                      <td class="miner">
                        <span
                          class="miner-tag"
                          v-if="block.Miner"
                          :title="block.Miner"
                          style="cursor: help"
                        >
                          {{ block.Miner.substring(0, 8) }}...
                        </span>
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
                      <td class="hash" :title="tx.txid" style="cursor: help">
                        {{ tx.txid ? tx.txid.substring(0, 12) : "Unknown" }}...
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
                      <td class="hash" :title="block.Hash" style="cursor: help">
                        {{
                          block.Hash ? block.Hash.substring(0, 8) : "Unknown"
                        }}...
                      </td>
                      <td class="miner">
                        <span
                          class="miner-tag"
                          v-if="block.Miner"
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

.dashboard-row {
  display: flex;
  gap: 30px;
  width: 100%;
}

.table-card {
  flex: 1;
  min-width: 0; /* 🕵️ 探長微調：防止內容過長撐爆卡片寬度 */
  background-color: #1e1e1e;
  border-radius: 10px;
  border: 1px solid #333;
  display: flex;
  flex-direction: column;
  min-height: 350px; /* 🕵️ 探長微調：給卡片一個基本高度 */
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

package mempool

import (
	"fmt"
	"log"
	"mycoin/blockchain"
	"mycoin/database"
	"strconv"
	"sync"
	"time"
)

type Mempool struct {
	Txs      map[string][]byte
	mu       sync.Mutex
	Spent    map[string]string
	Parents  map[string][]string // child → parents
	Children map[string][]string // parent → children
	Sources  map[string]uint64
	MaxTx    int
	DB       *database.BoltDB
	Times    map[string]int64 // 👈 探長的打卡鐘：TxID -> Unix 時間戳

}

func (m *Mempool) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Txs = make(map[string][]byte)

	// 🛡️ 探長加碼：清空的時候也要重新給一個新櫃子
	m.Times = make(map[string]int64)
	m.Sources = make(map[string]uint64)

	m.Spent = make(map[string]string)
	m.Parents = make(map[string][]string)
	m.Children = make(map[string][]string)
}

func NewMempool(maxTx int, db *database.BoltDB) *Mempool {
	return &Mempool{
		Times: make(map[string]int64),
		Txs:   make(map[string][]byte),
		// ==========================================
		// 🌟 探長提醒：這個專屬身分證的櫃子終於買進來了！
		// ==========================================
		Sources:  make(map[string]uint64),
		Spent:    make(map[string]string),
		Parents:  make(map[string][]string),
		Children: make(map[string][]string),
		MaxTx:    maxTx,
		DB:       db,
	}
}
func utxoKey(txid string, index int) string {
	return fmt.Sprintf("%s_%d", txid, index)
}

func (m *Mempool) Has(txid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.Txs[txid]
	return ok
}

func (m *Mempool) AddTxRBF(txid string, txBytes []byte, utxo *blockchain.UTXOSet, fromNodeID uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	newTx, err := blockchain.DeserializeTransaction(txBytes)
	if err != nil {
		return false
	}

	newFee := newTx.Fee(utxo, m.Txs)
	// 🕵️ 大偵探建議：定義一個最小增量 (0.01 YiCoin)
	const MinIncrementalFee = 1

	// 3️⃣ RBF：查找衝突
	conflicts := m.findConflicts(newTx)
	if len(conflicts) > 0 {
		for oldTxid := range conflicts {
			oldBytes := m.Txs[oldTxid]
			oldTx, _ := blockchain.DeserializeTransaction(oldBytes)
			oldFee := oldTx.Fee(utxo, m.Txs)

			// 🚀 修改點：新小費必須比舊的小費多出至少一個門檻
			if newFee < oldFee+MinIncrementalFee {
				fmt.Printf("🚫 [RBF] 拒絕替換：新小費 %.2f 不足以覆蓋舊小費 %.2f (需多於 %.2f)\n",
					float64(newFee)/100.0, float64(oldFee)/100.0, float64(MinIncrementalFee)/100.0)
				return false
			}
		}

		for oldTxid := range conflicts {
			m.removeTxUnsafe(oldTxid)
		}
	}

	// 🔥 Mempool Eviction (汰弱留強)
	if len(m.Txs) >= m.MaxTx {
		lowestTxid, lowestFee := m.findLowestFeeTx(utxo)
		if lowestTxid == "" || newFee <= lowestFee {
			return false
		}

		m.removeTxUnsafe(lowestTxid)

		// 🚀 修改點：讓日誌印出人類看得懂的小數點
		log.Printf("🧹 [Mempool Eviction] 踢掉低手續費交易: %s (Fee: %.2f) -> 換入新交易: %s (Fee: %.2f)\n",
			lowestTxid[:8], float64(lowestFee)/100.0, txid[:8], float64(newFee)/100.0)
	}

	m.addTxUnsafe(txid, newTx, txBytes, fromNodeID)
	return true
}

func (m *Mempool) Get(txid string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, ok := m.Txs[txid]
	return tx, ok
}

func (m *Mempool) RemoveTx(txid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.removeTxUnsafe(txid)
}

func (m *Mempool) GetAll() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string][]byte, len(m.Txs))
	for k, v := range m.Txs {
		out[k] = v
	}
	return out
}

func (m *Mempool) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Txs = make(map[string][]byte)
	m.Spent = make(map[string]string)
	m.Times = make(map[string]int64)    // 👈 探長提醒：加上這行
	m.Sources = make(map[string]uint64) // 👈 探長提醒：加上這行
}

func (m *Mempool) HasDoubleSpend(tx *blockchain.Transaction) bool {
	// 🛡️ 必須加上這把鎖，保護 m.Spent 不被併發修改
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, in := range tx.Inputs {
		key := utxoKey(in.TxID, in.Index)
		if _, used := m.Spent[key]; used {
			return true
		}
	}
	return false
}

func (m *Mempool) findConflicts(tx *blockchain.Transaction) map[string]bool {
	conflicts := make(map[string]bool)

	for _, in := range tx.Inputs {
		key := utxoKey(in.TxID, in.Index)
		if txid, ok := m.Spent[key]; ok {
			conflicts[txid] = true
		}
	}

	return conflicts
}

func (m *Mempool) addTxUnsafe(
	txid string,
	tx *blockchain.Transaction,
	txBytes []byte,
	fromNodeID uint64, // 👈 已經加上了
) {
	m.Txs[txid] = txBytes

	if m.DB != nil {
		m.DB.Put("mempool", txid, txBytes)
	}

	// ==========================================
	// 🌟 探長的「永恆記憶與身分溯源」持久化打卡鐘
	// ==========================================
	var enterTime int64
	var finalFromID uint64 = fromNodeID // 預設使用這次傳入的 NodeID
	isNewTransaction := true

	// 1. 嘗試從資料庫找舊時間與舊身分 (防重啟歸零)
	if m.DB != nil {
		// 找時間
		timeBytes := m.DB.Get("mempool_times", txid)
		if len(timeBytes) > 0 {
			parsedTime, parseErr := strconv.ParseInt(string(timeBytes), 10, 64)
			if parseErr == nil {
				enterTime = parsedTime
				isNewTransaction = false // 這是一筆重啟後載入的舊交易！
			}
		}

		// 🕵️ 探長新增：如果是舊交易，我們得把當時是誰給的 NodeID 讀回來
		if !isNewTransaction {
			sourceBytes := m.DB.Get("mempool_sources", txid)
			if len(sourceBytes) > 0 {
				parsedID, parseErr := strconv.ParseUint(string(sourceBytes), 10, 64)
				if parseErr == nil {
					finalFromID = parsedID
				}
			}
		}
	}

	// 2. 如果是「全新交易」，打上現在的時間、登記來源，並寫入資料庫保存！
	if isNewTransaction {
		enterTime = time.Now().Unix()
		if m.DB != nil {
			// 存時間
			timeStr := strconv.FormatInt(enterTime, 10)
			m.DB.Put("mempool_times", txid, []byte(timeStr))

			// 🕵️ 探長新增：存來源身分證
			idStr := strconv.FormatUint(finalFromID, 10)
			m.DB.Put("mempool_sources", txid, []byte(idStr))
		}
	}

	// 3. 把最終確定的時間與來源寫入記憶體 map
	m.Times[txid] = enterTime
	m.Sources[txid] = finalFromID // 🕵️ 探長新增：寫入 Sources 給廣播系統用
	// ==========================================

	// 👇 以下完美保留你原本的 UTXO 與關聯邏輯
	for _, in := range tx.Inputs {
		key := utxoKey(in.TxID, in.Index)
		m.Spent[key] = txid

		// 🚀 關鍵修復：直接檢查底層 Map，絕對不要呼叫 m.Has()！
		if _, exists := m.Txs[in.TxID]; exists {
			m.Parents[txid] = append(m.Parents[txid], in.TxID)
			m.Children[in.TxID] = append(m.Children[in.TxID], txid)
		}
	}
}

func (m *Mempool) removeTxUnsafe(txid string) {
	// 🛡️ 防彈衣：先檢查這筆交易到底在不在 Mempool 裡？
	txBytes, exists := m.Txs[txid]
	if !exists || len(txBytes) == 0 {
		return // 不在池子裡 (例如 Coinbase 交易)，直接結束，不用刪除！
	}

	tx, err := blockchain.DeserializeTransaction(txBytes)
	// 🛡️ 雙重保險：確保 tx 不是 nil
	if err == nil && tx != nil {
		for _, in := range tx.Inputs {
			key := utxoKey(in.TxID, in.Index)
			delete(m.Spent, key)
		}
	}
	delete(m.Txs, txid)
	delete(m.Times, txid)
	delete(m.Sources, txid)

	if m.DB != nil {
		m.DB.Delete("mempool", txid)
	}
}

func (m *Mempool) findLowestFeeTx(utxo *blockchain.UTXOSet) (string, int) {
	lowestFee := int(^uint(0) >> 1) // MaxInt
	lowestTxid := ""

	for txid, txBytes := range m.Txs {
		tx, err := blockchain.DeserializeTransaction(txBytes)
		if err != nil {
			continue
		}

		// 🚀 修改點：計算手續費時，讓它參考整個 Mempool (m.Txs)
		// 這樣富兒子的手續費就不會是 0，而是真實的 35 元
		fee := tx.Fee(utxo, m.Txs)

		if fee < lowestFee {
			lowestFee = fee
			lowestTxid = txid
		}
	}

	return lowestTxid, lowestFee
}

func (m *Mempool) Remove(txid string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.removeTxUnsafe(txid)
}

func (m *Mempool) GetSource(txid string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id, exists := m.Sources[txid]; exists {
		return id
	}
	return 0 // 如果找不到或是本地發起的，回傳 0
}

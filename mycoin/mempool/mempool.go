package mempool

import (
	"fmt"
	"log"
	"mycoin/blockchain"
	"sync"

	"mycoin/database"
)

type Mempool struct {
	Txs      map[string][]byte
	mu       sync.Mutex
	Spent    map[string]string
	Parents  map[string][]string // child → parents
	Children map[string][]string // parent → children
	MaxTx    int
	DB       *database.BoltDB
}

func (m *Mempool) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Txs = make(map[string][]byte)
	m.Spent = make(map[string]string)

	m.Parents = make(map[string][]string)
	m.Children = make(map[string][]string)
}

func NewMempool(maxTx int, db *database.BoltDB) *Mempool {
	return &Mempool{
		Txs:      make(map[string][]byte),
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

func (m *Mempool) AddTxRBF(txid string, txBytes []byte, utxo *blockchain.UTXOSet) bool {
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

	m.addTxUnsafe(txid, newTx, txBytes)
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
) {
	m.Txs[txid] = txBytes

	if m.DB != nil {
		m.DB.Put("mempool", txid, txBytes)
	}

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

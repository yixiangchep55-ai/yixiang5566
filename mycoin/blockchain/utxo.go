package blockchain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mycoin/database"
)

// UTXO 结构
type UTXO struct {
	TxID   string
	Index  int
	Amount int
	To     string // 收款公钥 hex
}

// UTXOSet 管理整个节点的 UTXO 集合
type UTXOSet struct {
	Set       map[string]UTXO     // key = TxID_Index
	AddrIndex map[string][]string // 按地址索引，加速查询
	DB        *database.BoltDB
}

// 创建新的 UTXOSet
func NewUTXOSet(db *database.BoltDB) *UTXOSet {
	return &UTXOSet{
		Set:       make(map[string]UTXO),
		AddrIndex: make(map[string][]string),
		DB:        db,
	}
}

func (u *UTXOSet) Clear() error {
	// 清空内存中的 UTXO
	u.Set = make(map[string]UTXO)
	u.AddrIndex = make(map[string][]string)

	// 清空 DB bucket （可选但推荐）
	if u.DB != nil {
		err := u.DB.ClearBucket("utxo")
		if err != nil {
			return err
		}
	}

	return nil
}

// 添加UTXO（交易输出）
// 添加UTXO（交易输出）
func (u *UTXOSet) Add(tx Transaction) {
	for i, out := range tx.Outputs {

		key := fmt.Sprintf("%s_%d", tx.ID, i)

		// 构造 UTXO 对象
		utxo := UTXO{
			TxID:   tx.ID,
			Index:  i,
			Amount: out.Amount,
			To:     out.To,
		}

		// 1️⃣ 写入内存 Set (Map 会自动覆盖旧值，所以很安全)
		u.Set[key] = utxo

		// 2️⃣ 🚀 写入地址索引前，先检查是否已经存在（防止影分身！）
		exists := false
		for _, existingKey := range u.AddrIndex[out.To] {
			if existingKey == key {
				exists = true
				break
			}
		}

		// 只有當這個 key 不存在時，我們才把它加進陣列裡
		if !exists {
			u.AddrIndex[out.To] = append(u.AddrIndex[out.To], key)
		}

		// 3️⃣ ⭐ 持久化到数据库（可选，但推荐）
		if u.DB != nil {
			b, _ := json.Marshal(utxo)
			err := u.DB.Put("utxo", key, b)
			if err != nil {
				fmt.Println("❌ failed to persist utxo:", err)
			}
		}
	}
}
func (u *UTXOSet) Clone() *UTXOSet {
	nu := NewUTXOSet(u.DB)
	for k, v := range u.Set {
		nu.Set[k] = v
	}
	for addr, keys := range u.AddrIndex {
		nu.AddrIndex[addr] = append([]string{}, keys...)
	}
	return nu
}

// 消耗UTXO（交易输入），返回错误
func (u *UTXOSet) Spend(tx Transaction) error {
	if tx.IsCoinbase {
		return nil
	}
	for _, in := range tx.Inputs {
		key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
		utxo, ok := u.Set[key]
		if !ok {
			return fmt.Errorf("UTXO not found: %s", key)
		}

		// 🚀 關鍵修復 1：將 Hex 公鑰還原成 Base58 錢包地址
		pubBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			return fmt.Errorf("invalid pubkey hex: %v", err)
		}

		// ⚠️ 注意：如果你的 PubKeyToAddress 是在 blockchain 包裡，這裡就是 blockchain.PubKeyToAddress
		// 如果這個 Spend 函數本身就在 blockchain 包裡，直接呼叫 PubKeyToAddress 即可
		addr := PubKeyToAddress(pubBytes)

		// 🚀 關鍵修復 2：用算出來的「地址 (addr)」來跟 UTXO 上的「地址 (utxo.To)」比對
		if utxo.To != addr {
			return fmt.Errorf("UTXO owner mismatch: %s", key)
		}

		// 删除UTXO
		delete(u.Set, key)

		if u.DB != nil {
			u.DB.Delete("utxo", key)
		}

		// 🚀 關鍵修復 3：同步地址索引時，也必須使用「地址 (addr)」來尋找，而不是公鑰！
		keys := u.AddrIndex[addr]
		for i, k := range keys {
			if k == key {
				u.AddrIndex[addr] = append(keys[:i], keys[i+1:]...)
				break
			}
		}
	}
	return nil
}

// 查询某个地址所有可用UTXO
func (u *UTXOSet) GetUTXOs(pub string) []UTXO {
	keys := u.AddrIndex[pub]
	utxos := make([]UTXO, 0, len(keys))
	for _, k := range keys {
		if utxo, ok := u.Set[k]; ok {
			utxos = append(utxos, utxo)
		}
	}
	return utxos
}

// 检查UTXO是否存在
func (u *UTXOSet) Exists(txID string, idx int, pub string) bool {
	key := fmt.Sprintf("%s_%d", txID, idx)
	v, ok := u.Set[key]
	return ok && v.To == pub
}

func (u *UTXOSet) Get(txid string, index int) (*TxOutput, bool) {
	// 正确的 key
	key := fmt.Sprintf("%s_%d", txid, index)

	utxo, ok := u.Set[key]
	if !ok {
		return nil, false
	}

	// 返回 TxOutput，而不是 utxo.Outs[index]
	return &TxOutput{
		Amount: utxo.Amount,
		To:     utxo.To,
	}, true
}

func (u *UTXOSet) FindSpendableOutputs(pubKey string, amount int) (int, map[string][]int) {
	unspentOutputs := make(map[string][]int)
	accumulated := 0

	// 利用你寫好的 AddrIndex 快速找出這個人的所有 UTXO
	keys := u.AddrIndex[pubKey]

	for _, k := range keys {
		if utxo, ok := u.Set[k]; ok {
			accumulated += utxo.Amount
			unspentOutputs[utxo.TxID] = append(unspentOutputs[utxo.TxID], utxo.Index)

			// 錢湊夠了就停止，不需要把所有的 UTXO 都找出來
			if accumulated >= amount {
				break
			}
		}
	}

	return accumulated, unspentOutputs
}

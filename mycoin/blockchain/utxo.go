package blockchain

import (
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
	Outs   []TxOutput
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

		// 1️⃣ 写入内存 Set
		u.Set[key] = utxo

		// 2️⃣ 写入地址索引（内存）
		u.AddrIndex[out.To] = append(u.AddrIndex[out.To], key)

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
	for _, in := range tx.Inputs {
		key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
		utxo, ok := u.Set[key]
		if !ok {
			return fmt.Errorf("UTXO not found: %s", key)
		}
		if utxo.To != in.PubKey {
			return fmt.Errorf("UTXO owner mismatch: %s", key)
		}

		// 删除UTXO
		delete(u.Set, key)

		if u.DB != nil {
			u.DB.Delete("utxo", key)
		}

		// 同步地址索引
		keys := u.AddrIndex[in.PubKey]
		for i, k := range keys {
			if k == key {
				u.AddrIndex[in.PubKey] = append(keys[:i], keys[i+1:]...)
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

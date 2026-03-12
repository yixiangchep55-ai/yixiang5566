package wallet

import (
	"fmt"
	"mycoin/blockchain"
)

// 从 UTXO 里选钱
// 💡 注意：我們新增了 mempoolTxs 參數來進行比對
func SelectUTXO(utxo *blockchain.UTXOSet, addr string, amount int, mempoolTxs []blockchain.Transaction) ([]blockchain.UTXO, int) {
	var selected []blockchain.UTXO
	total := 0

	// 1️⃣ 先找出所有在 Mempool 裡「已經被預訂」的鈔票 (Inputs)
	spentInMempool := make(map[string]bool)
	for _, tx := range mempoolTxs {
		for _, input := range tx.Inputs {
			// 標記：這張 TxID + Index 的組合已經被用掉了
			key := input.TxID + "_" + fmt.Sprint(input.Index)
			spentInMempool[key] = true
		}
	}

	keys := utxo.AddrIndex[addr]
	for _, key := range keys {
		// 🕵️ 關鍵過濾：如果這張錢在 Mempool 預訂名單裡，就跳過它！
		if spentInMempool[key] {
			fmt.Printf("⚠️ [SelectUTXO] 發現鈔票 %s 正在 Mempool 排隊，跳過不使用。\n", key[:8])
			continue
		}

		u, ok := utxo.Set[key]
		if !ok {
			continue
		}

		selected = append(selected, u)
		total += u.Amount

		if total >= amount {
			break
		}
	}

	if total < amount {
		return nil, total
	}

	return selected, total
}

func BuildTransaction(
	fromAddr string,
	toAddr string,
	amount int,
	fee int,
	utxoSet *blockchain.UTXOSet,
	// 🚀 【新增參數】傳入目前 Mempool 中的交易列表，防止重覆選錢
	mempoolTxs []blockchain.Transaction,
) (*blockchain.Transaction, error) {

	// 計算總共需要的錢 (匯給對方的錢 + 手續費)
	targetAmount := amount + fee

	// 1️⃣ 选 UTXO
	// 🚀 【修改點】將 mempoolTxs 傳入 SelectUTXO
	utxos, total := SelectUTXO(utxoSet, fromAddr, targetAmount, mempoolTxs)

	if utxos == nil {
		// 在 wallet/wallet.go 裡
		return nil, fmt.Errorf("餘額不足！你目前只有 %.2f YiCoin，不足以支付本次轉帳與手續費", float64(total)/100.0)
	}

	// 2️⃣ 构造 inputs
	var inputs []blockchain.TxInput
	for _, u := range utxos {
		inputs = append(inputs, blockchain.TxInput{
			TxID:  u.TxID,
			Index: u.Index,
		})
	}

	// 3️⃣ 构造 outputs
	var outputs []blockchain.TxOutput
	outputs = append(outputs, blockchain.TxOutput{
		Amount: amount,
		To:     toAddr,
	})

	// 4️⃣ 找零
	if change := total - amount - fee; change > 0 {
		outputs = append(outputs, blockchain.TxOutput{
			Amount: change,
			To:     fromAddr,
		})
	}

	// 5️⃣ 创建交易
	tx := blockchain.NewTransaction(inputs, outputs)
	return tx, nil
}

package wallet

import (
	"fmt"
	"mycoin/blockchain"
)

// 从 UTXO 里选钱
func SelectUTXO(utxo *blockchain.UTXOSet, addr string, amount int) ([]blockchain.UTXO, int) {
	var selected []blockchain.UTXO
	total := 0
	missCount := 0 // 👈 关键在这里：必须先声明这个幽灵计数器！

	keys := utxo.AddrIndex[addr]
	fmt.Printf("【Debug UTXO缓存】地址: %s, 找到的可用 UTXO 数量: %d\n", addr, len(keys))

	used := make(map[string]bool)

	for _, key := range keys {
		if used[key] {
			continue
		}

		u, ok := utxo.Set[key]
		if !ok {
			missCount++ // 抓到一只幽灵钞票
			continue
		}

		// 看看拿出来的钞票面额到底是几块钱
		fmt.Printf("【Debug 验钞】拿到一笔面额为: %d 的 UTXO\n", u.Amount)

		selected = append(selected, u)
		total += u.Amount
		used[key] = true

		if total >= amount {
			break
		}
	}

	// 循环结束后的最终战况汇总
	fmt.Printf("【Debug 结算】最终凑集总额: %d, 发现幽灵钞票: %d 张\n", total, missCount)

	if total < amount {
		return nil, 0
	}

	return selected, total
}
func BuildTransaction(
	fromAddr string,
	toAddr string,
	amount int,
	fee int, // 🚀 1. 新增：手續費參數
	utxoSet *blockchain.UTXOSet,
) (*blockchain.Transaction, error) {

	// 🚀 2. 新增：計算總共需要的錢 (匯給對方的錢 + 手續費)
	targetAmount := amount + fee

	// 1️⃣ 选 UTXO（fromAddr 只用于选钱）
	// 注意這裡要傳入 targetAmount 去找錢包拿錢！
	utxos, total := SelectUTXO(utxoSet, fromAddr, targetAmount)
	if utxos == nil {
		return nil, fmt.Errorf("insufficient funds. [Debug] From: %s, 尝试找金额 (含手續費): %d, 但找不到足够的UTXO", fromAddr, targetAmount)
	}

	// 2️⃣ 构造 inputs（⚠️ 不再写 From）
	var inputs []blockchain.TxInput
	for _, u := range utxos {
		inputs = append(inputs, blockchain.TxInput{
			TxID:  u.TxID,
			Index: u.Index,
			// Signature / PubKey 之后签名再填
		})
	}

	// 3️⃣ 构造 outputs
	var outputs []blockchain.TxOutput
	outputs = append(outputs, blockchain.TxOutput{
		Amount: amount, // 給對方原本的金額 (不含手續費)
		To:     toAddr,
	})

	// 4️⃣ 找零
	// 🚀 3. 修改：找零給自己 = 總共拿出來的錢 - 給對方的錢 - 手續費！
	if change := total - amount - fee; change > 0 {
		outputs = append(outputs, blockchain.TxOutput{
			Amount: change,
			To:     fromAddr,
		})
	}

	// 5️⃣ 创建交易（此时是“未签名交易”）
	tx := blockchain.NewTransaction(inputs, outputs)
	return tx, nil
}

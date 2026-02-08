package wallet

import (
	"fmt"
	"mycoin/blockchain"
)

// 从 UTXO 里选钱
func SelectUTXO(utxo *blockchain.UTXOSet, addr string, amount int) ([]blockchain.UTXO, int) {
	var selected []blockchain.UTXO
	total := 0

	keys := utxo.AddrIndex[addr]
	for _, key := range keys {
		u := utxo.Set[key]
		selected = append(selected, u)
		total += u.Amount
		if total >= amount {
			break
		}
	}

	if total < amount {
		return nil, 0
	}

	return selected, total
}

func BuildTransaction(
	fromAddr string,
	toAddr string,
	amount int,
	utxoSet *blockchain.UTXOSet,
) (*blockchain.Transaction, error) {

	// 1️⃣ 选 UTXO（fromAddr 只用于选钱）
	utxos, total := SelectUTXO(utxoSet, fromAddr, amount)
	if utxos == nil {
		return nil, fmt.Errorf("insufficient funds")
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
		Amount: amount,
		To:     toAddr,
	})

	// 4️⃣ 找零
	if change := total - amount; change > 0 {
		outputs = append(outputs, blockchain.TxOutput{
			Amount: change,
			To:     fromAddr,
		})
	}

	// 5️⃣ 创建交易（此时是“未签名交易”）
	tx := blockchain.NewTransaction(inputs, outputs)
	return tx, nil
}

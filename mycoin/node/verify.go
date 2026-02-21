package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"mycoin/blockchain"
)

// VerifyBlockWithUTXO 驗證整個區塊的合法性
func VerifyBlockWithUTXO(
	block *blockchain.Block,
	parent *blockchain.Block,
	utxo *blockchain.UTXOSet,
) error {

	// 1️⃣ header / PoW / tx signature
	if err := block.Verify(parent); err != nil {
		return err
	}

	// 2️⃣ 临时 UTXO (隔離沙盒)
	tmp := utxo.Clone()

	// 3️⃣ coinbase 必须第一个
	if len(block.Transactions) == 0 || !block.Transactions[0].IsCoinbase {
		return fmt.Errorf("coinbase must be first")
	}

	// 4️⃣ 执行交易
	for i, tx := range block.Transactions {
		if i == 0 {
			tmp.Add(tx)
			continue
		}

		// 🔥 關鍵新增：在 Spend 之前，利用 tmp 進行嚴格的簽名與金額檢查
		if err := VerifyTx(tx, tmp); err != nil {
			return fmt.Errorf("tx %s invalid: %v", tx.ID, err)
		}

		// 如果上面檢查通過，這裡執行花費 (同時防禦同一區塊內的雙花)
		if err := tmp.Spend(tx); err != nil {
			return fmt.Errorf("double spend or missing utxo: %v", err)
		}

		// 產生新的 UTXO 供後續交易使用
		tmp.Add(tx)
	}

	return nil
}

func VerifyTx(tx blockchain.Transaction, utxoSet *blockchain.UTXOSet) error {
	// 1️⃣ coinbase 永远合法
	if tx.IsCoinbase {
		return nil
	}

	// 🔥 2️⃣ 直接呼叫 Transaction 內建的驗證，確保密碼學簽名絕對合法！
	if !tx.Verify() {
		return errors.New("signature verification failed (tx.Verify returned false)")
	}

	totalIn := 0
	for _, in := range tx.Inputs {

		// 3️⃣ 检查 UTXO 是否存在
		key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
		utxo, ok := utxoSet.Set[key]
		if !ok {
			return fmt.Errorf("missing input utxo: %s", key)
		}
		totalIn += utxo.Amount

		// 4️⃣ 验证公钥是否匹配该 UTXO 的 owner (防小偷拿自己的私鑰花你的錢)
		pubBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			return errors.New("invalid pubkey hex")
		}

		addr := blockchain.PubKeyToAddress(pubBytes)
		if addr != utxo.To {
			return fmt.Errorf("pubkey does not match utxo owner")
		}
	}

	// 5️⃣ 检查出账金额 (防憑空印鈔)
	totalOut := 0
	for _, out := range tx.Outputs {
		totalOut += out.Amount
	}

	if totalIn < totalOut {
		return errors.New("inputs < outputs")
	}

	return nil
}

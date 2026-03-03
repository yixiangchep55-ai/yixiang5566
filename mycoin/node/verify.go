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
		if err := VerifyTx(tx, tmp, nil); err != nil {
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

// 🕵️ 大偵探修改：第三個參數變成 map[string][]byte
func VerifyTx(tx blockchain.Transaction, utxoSet *blockchain.UTXOSet, mempoolTxs map[string][]byte) error {
	// 1️⃣ coinbase 永远合法
	if tx.IsCoinbase {
		return nil
	}

	// 2️⃣ 直接呼叫 Transaction 內建的驗證，確保密碼學簽名絕對合法！
	if !tx.Verify() {
		return errors.New("signature verification failed (tx.Verify returned false)")
	}

	totalIn := 0
	for _, in := range tx.Inputs {
		// 3️⃣ 检查 UTXO 是否存在
		key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
		utxo, ok := utxoSet.Set[key]

		// ==========================================
		// 🕵️ 大偵探的終極 CPFP 邏輯 (反序列化版)！
		// ==========================================
		if !ok && mempoolTxs != nil {
			// 從 Mempool 拿出這團 []byte
			if parentTxBytes, inMempool := mempoolTxs[in.TxID]; inMempool {

				// 🛠️ 關鍵動作：把它解壓縮成真正的 Transaction！
				// (請確認你的反序列化函數是不是叫 DeserializeTransaction)
				parentTx, err := blockchain.DeserializeTransaction(parentTxBytes)

				if err == nil { // 解壓縮成功
					if in.Index >= 0 && in.Index < len(parentTx.Outputs) {
						out := parentTx.Outputs[in.Index]

						// 構造臨時 UTXO (如果編譯報錯說不能用 &，請把 & 刪掉)
						utxo = blockchain.UTXO{
							TxID:   in.TxID,
							Index:  in.Index,
							Amount: out.Amount,
							To:     out.To,
						}
						ok = true // 成功在 Mempool 找到了！
						fmt.Printf("💡 [CPFP] 偵測到未確認的父交易輸入: %s\n", key)
					}
				}
			}
		}
		// ==========================================

		if !ok {
			return fmt.Errorf("missing input utxo: %s (已確認帳本和 Mempool 都找不到)", key)
		}
		totalIn += utxo.Amount

		// 4️⃣ 验证公钥是否匹配该 UTXO 的 owner
		pubBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			return errors.New("invalid pubkey hex")
		}

		addr := blockchain.PubKeyToAddress(pubBytes)
		if addr != utxo.To {
			return fmt.Errorf("pubkey does not match utxo owner")
		}
	}

	// 5️⃣ 检查出账金额
	totalOut := 0
	for _, out := range tx.Outputs {
		totalOut += out.Amount
	}

	if totalIn < totalOut {
		return errors.New("inputs < outputs")
	}

	return nil
}

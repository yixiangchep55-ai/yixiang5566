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
	// 1️⃣ 基礎驗證 (PoW, PrevHash)
	if err := block.Verify(parent); err != nil {
		return err
	}

	tmp := utxo.Clone()
	var totalFees int = 0

	// 4️⃣ 執行交易 (跳過 Coinbase)
	for i, tx := range block.Transactions {
		if i == 0 {
			continue
		}

		// 🚀 區塊內驗證不需要 Mempool，因為依賴項必須在區塊內的前面幾筆或已入帳
		if err := VerifyTx(tx, tmp, nil); err != nil {
			return fmt.Errorf("交易 %s 驗證失敗: %v", tx.ID[:8], err)
		}

		totalFees += tx.Fee(tmp, nil)

		if err := tmp.Spend(tx); err != nil {
			return fmt.Errorf("區塊內偵測到雙花或資金來源異常: %v", err)
		}
		tmp.Add(tx)
	}

	// 5️⃣ 🕵️ 偵探嚴審：Coinbase 金額校驗
	coinbaseTx := block.Transactions[0]

	// 🚀 修正點：使用 500 (5.00 YiCoin) 而非 100
	// 💡 最佳實踐：如果 block 裡面有 Reward 欄位，直接用 block.Reward
	expectedReward := 500 + totalFees

	actualReward := 0
	for _, out := range coinbaseTx.Outputs {
		actualReward += out.Amount
	}

	if actualReward > expectedReward {
		return fmt.Errorf("礦工太貪心了！溢領獎勵：預期 %.2f, 實際領取 %.2f",
			float64(expectedReward)/100.0, float64(actualReward)/100.0)
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
						fmt.Printf("💡 [CPFP] 偵測到連鎖交易！父交易輸入: %s\n", key)
						fmt.Printf("💰 [CPFP] 該筆未確認金額為: %.2f YiCoin\n", float64(utxo.Amount)/100.0)
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

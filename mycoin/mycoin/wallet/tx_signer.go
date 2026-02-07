package wallet

import (
	"encoding/hex"

	"mycoin/blockchain"

	"crypto/sha256"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

func SignTransaction(tx *blockchain.Transaction, w *Wallet) error {
	for i := range tx.Inputs {

		// 1️⃣ 计算该输入的签名哈希
		data := tx.IDForSig(i)
		hash := sha256.Sum256(data)

		// 2️⃣ 用 secp256k1 私钥签名（btcec/v2）
		sig := ecdsa.Sign(w.PrivateKey, hash[:])

		// 3️⃣ 写入 DER 签名（hex）
		tx.Inputs[i].Sig = hex.EncodeToString(sig.Serialize())

		// 4️⃣ 写入压缩公钥（hex）
		tx.Inputs[i].PubKey = hex.EncodeToString(w.PublicKey)
	}

	return nil
}

package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mycoin/blockchain"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

func VerifyBlockWithUTXO(
	block *blockchain.Block,
	parent *blockchain.Block,
	utxo *blockchain.UTXOSet,
) error {

	// 1️⃣ header / PoW / tx signature
	if err := block.Verify(parent); err != nil {
		return err
	}

	// 2️⃣ 临时 UTXO
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

		if err := tmp.Spend(tx); err != nil {
			return fmt.Errorf("double spend or missing utxo: %v", err)
		}
		tmp.Add(tx)
	}

	return nil
}

func (n *Node) VerifyTx(tx blockchain.Transaction) error {

	// 1️⃣ coinbase 永远合法
	if tx.IsCoinbase {
		return nil
	}

	totalIn := 0
	for i, in := range tx.Inputs {

		// 2️⃣ 检查 UTXO 是否存在
		key := fmt.Sprintf("%s_%d", in.TxID, in.Index)
		utxo, ok := n.UTXO.Set[key]
		if !ok {
			return fmt.Errorf("missing input utxo: %s", key)
		}
		totalIn += utxo.Amount

		// 3️⃣ 验证公钥是否匹配该 UTXO 的 owner
		pubBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			return errors.New("invalid pubkey hex")
		}

		addr := blockchain.PubKeyToAddress(pubBytes)
		if addr != utxo.To {
			return fmt.Errorf("pubkey does not match utxo owner")
		}

		// 4️⃣ 验证签名
		sigBytes, err := hex.DecodeString(in.Sig)
		if err != nil {
			return errors.New("invalid signature hex")
		}

		sig, err := ecdsa.ParseDERSignature(sigBytes)
		if err != nil {
			return errors.New("invalid DER signature")
		}

		pubKey, err := btcec.ParsePubKey(pubBytes)
		if err != nil {
			return errors.New("invalid public key")
		}

		// 5️⃣ 重算签名哈希
		hash := sha256.Sum256(tx.IDForSig(i))

		if !sig.Verify(hash[:], pubKey) {
			return fmt.Errorf("signature verification failed for input %d", i)
		}
	}

	// 6️⃣ 检查出账金额
	totalOut := 0
	for _, out := range tx.Outputs {
		totalOut += out.Amount
	}

	if totalIn < totalOut {
		return errors.New("inputs < outputs")
	}

	return nil
}

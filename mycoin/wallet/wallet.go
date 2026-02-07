package wallet

import (
	"crypto/sha256"
	"mycoin/blockchain"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

type Wallet struct {
	PrivateKey *btcec.PrivateKey
	PublicKey  []byte
	Address    string
}

func NewWallet() (*Wallet, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}

	pub := priv.PubKey().SerializeCompressed()
	addr := blockchain.PubKeyToAddress(pub)

	return &Wallet{
		PrivateKey: priv,
		PublicKey:  pub,
		Address:    addr,
	}, nil
}

// --- INDUSTRIAL SIGNATURE ---
func (w *Wallet) Sign(data []byte) ([]byte, error) {
	// 规范做法：先哈希
	hash := sha256.Sum256(data)

	// 用 btcec/v2/ecdsa 生成签名
	sig := ecdsa.Sign(w.PrivateKey, hash[:])

	// DER 序列化
	return sig.Serialize(), nil
}

func VerifySignature(pubKeyBytes, sigBytes, data []byte) bool {
	hash := sha256.Sum256(data)

	sig, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		return false
	}

	pub, err := btcec.ParsePubKey(pubKeyBytes)
	if err != nil {
		return false
	}

	return sig.Verify(hash[:], pub)
}

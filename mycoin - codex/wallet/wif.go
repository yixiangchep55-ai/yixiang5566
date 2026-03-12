package wallet

import (
	"crypto/sha256"
	"errors"
	"mycoin/blockchain"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcutil/base58"
)

const wifPrefix = byte(0x80) // Bitcoin WIF mainnet

// 导出 WIF
func (w *Wallet) ExportWIF() string {
	raw := append([]byte{wifPrefix}, w.PrivateKey.Serialize()...)

	// double SHA256
	h1 := sha256.Sum256(raw)
	h2 := sha256.Sum256(h1[:])
	full := append(raw, h2[:4]...)

	return base58.Encode(full)
}

// 从 WIF 导入
func ImportWIF(wif string) (*Wallet, error) {
	raw := base58.Decode(wif)
	if len(raw) != 1+32+4 {
		return nil, errors.New("Invalid WIF")
	}

	key := raw[1 : 1+32]

	priv, _ := btcec.PrivKeyFromBytes(key)

	pub := priv.PubKey().SerializeCompressed()
	addr := blockchain.PubKeyToAddress(pub)

	return &Wallet{
		PrivateKey: priv,
		PublicKey:  pub,
		Address:    addr,
	}, nil
}

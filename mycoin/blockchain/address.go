package blockchain

import (
	"crypto/sha256"

	"golang.org/x/crypto/ripemd160"

	"github.com/btcsuite/btcutil/base58"
)

const mainnetPrefix = byte(0x00) // Bitcoin mainnet

func PubKeyToAddress(pubKey []byte) string {
	sha := sha256.Sum256(pubKey)

	rip := ripemd160.New()
	_, _ = rip.Write(sha[:]) // ✔ 避免忽略错误
	pubHash := rip.Sum(nil)

	payload := make([]byte, 0, 1+20+4) // ✔ 预先分配容量
	payload = append(payload, mainnetPrefix)
	payload = append(payload, pubHash...)

	chk := sha256.Sum256(payload)
	chk2 := sha256.Sum256(chk[:])

	payload = append(payload, chk2[:4]...)

	return base58.Encode(payload)
}

package wallet

import (
	"mycoin/blockchain"
)

func SignTransaction(tx *blockchain.Transaction, w *Wallet) error {
	// 🚀 直接呼叫交易本身內建的 Sign 方法！
	// 我們剛剛已經在 transaction.go 裡面把公鑰寫入、Hash 防護都做好了，
	// 這裡直接交給它處理，保證簽名與驗證 100% 同步！
	return tx.Sign(w.PrivateKey)
}

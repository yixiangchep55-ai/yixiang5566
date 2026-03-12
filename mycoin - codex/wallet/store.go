package wallet

import (
	"os"
)

// 保存钱包（WIF）
func SaveWallet(path string, w *Wallet) error {
	wif := w.ExportWIF()
	return os.WriteFile(path, []byte(wif), 0600)
}

// 加载钱包
func LoadWallet(path string) (*Wallet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ImportWIF(string(raw))
}

package blockchain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	ecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// UTXO input
type TxInput struct {
	TxID   string // 前一个交易ID
	Index  int    // 前一个交易输出索引
	Sig    string // 签名（DER hex）
	PubKey string // 压缩公钥 hex
}

// UTXO output
type TxOutput struct {
	Amount int
	To     string // 收款公钥 hex
}

// Transaction
type Transaction struct {
	ID         string
	Inputs     []TxInput
	Outputs    []TxOutput
	IsCoinbase bool
}

type TxIndexEntry struct {
	BlockHash string `json:"block_hash"`
	Height    uint64 `json:"height"`
	TxOffset  int    `json:"tx_offset"`
	Pruned    bool   `json:"pruned"` // ⭐新增
}

// 计算交易ID（只用未签名数据）
func (tx *Transaction) CalcID() {
	data, _ := json.Marshal(tx.cloneWithoutSign())
	hash := sha256.Sum256(data)
	tx.ID = hex.EncodeToString(hash[:])
}

func HashTxBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// 签名交易
// 請在 transaction.go 裡面修改！
func (tx *Transaction) Sign(priv *btcec.PrivateKey) error {
	if tx.IsCoinbase {
		return nil
	}

	// 🚀 1. 關鍵新增：直接從傳進來的私鑰，推導出公鑰的 Hex 字串
	pubKeyHex := hex.EncodeToString(priv.PubKey().SerializeCompressed())

	for i := range tx.Inputs {
		// 🚀 2. 關鍵新增：在算 Hash 之前，先把真正的公鑰塞進 Input 裡！
		tx.Inputs[i].PubKey = pubKeyHex

		data := tx.IDForSig(i) // 待签名摘要
		hash := sha256.Sum256(data)

		// ⭐ 正确的签名函数（btcec/v2）
		sig := ecdsa.Sign(priv, hash[:])

		// ⭐ Sig 是 string，所以转 hex
		tx.Inputs[i].Sig = hex.EncodeToString(sig.Serialize())
	}

	return nil
}

// 验证交易签名
func (tx *Transaction) Verify() bool {
	if tx.IsCoinbase {
		return true
	}

	for i, in := range tx.Inputs {
		// 1️⃣ 构造与签名时完全一致的摘要
		data := tx.IDForSig(i)
		hash := sha256.Sum256(data)

		// 2️⃣ 解析 DER 签名（hex → bytes → signature）
		sigBytes, err := hex.DecodeString(in.Sig)
		if err != nil {
			return false
		}

		sig, err := ecdsa.ParseDERSignature(sigBytes)
		if err != nil {
			return false
		}

		// 3️⃣ 解析公钥
		pubKeyBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			return false
		}

		pubKey, err := btcec.ParsePubKey(pubKeyBytes)
		if err != nil {
			return false
		}

		// 4️⃣ 验签（注意：用 hash，不是 data）
		if !sig.Verify(hash[:], pubKey) {
			return false
		}
	}

	return true
}

// 增加一個 genesisData 參數
func NewCoinbase(to string, reward int, genesisData string) *Transaction {
	var sig string

	// 🚀 關鍵判斷：如果有傳入創世字串，就用固定的！否則就用時間戳！
	if genesisData != "" {
		sig = genesisData
	} else {
		sig = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	dummyInput := TxInput{
		TxID:   "",
		Index:  -1,
		Sig:    sig, // 使用剛剛判斷好的 sig
		PubKey: "Coinbase",
	}

	tx := &Transaction{
		Inputs: []TxInput{dummyInput},
		Outputs: []TxOutput{
			{Amount: reward, To: to},
		},
		IsCoinbase: true,
	}

	tx.ID = tx.DeterministicID()
	return tx
}

// 签名数据（只用未签名交易）
func (tx *Transaction) IDForSig(idx int) []byte {
	tmp := tx.cloneWithoutSign()
	data, _ := json.Marshal(tmp)
	fmt.Printf("\n🕵️ [Debug] IDForSig 準備 Hash 的 JSON: %s\n", string(data))
	hash := sha256.Sum256(data)
	return hash[:]
}

// cloneWithoutSign 返回一个交易副本，清空所有可能引起 Hash 變化的欄位
func (tx *Transaction) cloneWithoutSign() *Transaction {
	tmp := *tx
	tmp.ID = "" // 🚀 防護 1：強制清空 ID

	tmp.Inputs = make([]TxInput, len(tx.Inputs))
	for i, in := range tx.Inputs {
		tmp.Inputs[i] = TxInput{
			TxID:   in.TxID,
			Index:  in.Index,
			Sig:    "", // 🚀 防護 2：清空簽名
			PubKey: "", // 🚀 防護 3：強制清空公鑰 (這招最關鍵，徹底杜絕欄位賦值時間差)
		}
	}
	return &tmp
}

func (tx *Transaction) Serialize() []byte {
	b, _ := json.Marshal(tx)
	return b
}

func (tx *Transaction) Hash() string {
	h := sha256.Sum256(tx.Serialize())
	return hex.EncodeToString(h[:])
}

func DeserializeTransaction(b []byte) (*Transaction, error) {
	var tx Transaction
	if err := json.Unmarshal(b, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

func (tx *Transaction) Fee(utxo *UTXOSet) int {
	// 🛡️ 防護罩 1：防止 tx 本身是幽靈 (解決 addr=0x40 的元兇！)
	if tx == nil {
		return 0
	}

	// 🛡️ 防護罩 2：防止沒傳入資料庫
	if utxo == nil {
		return 0
	}

	// 現在這裡絕對安全了，不會再爆炸
	if tx.IsCoinbase {
		return 0
	}

	inSum := 0
	for _, in := range tx.Inputs {
		out, ok := utxo.Get(in.TxID, in.Index)

		// 🛡️ 防護罩 3：找不到鈔票時，優雅地處理
		// (如果你程式裡的 out 是指標類型 *TxOutput，請把條件改成 if !ok || out == nil)
		if !ok {
			// 找不到鈔票，代表它可能是一筆 CPFP 交易 (父母還在 Mempool)
			// 為了安全，我們回傳 0，先不給它手續費優先權
			return 0
		}
		inSum += out.Amount
	}

	outSum := 0
	for _, out := range tx.Outputs {
		outSum += out.Amount
	}

	fee := inSum - outSum
	if fee < 0 {
		return 0
	}
	return fee
}

func NewTransaction(inputs []TxInput, outputs []TxOutput) *Transaction {
	tx := &Transaction{
		Inputs:     inputs,
		Outputs:    outputs,
		IsCoinbase: false,
	}

	// 自动计算 Tx.ID（不含签名）
	tx.CalcID()
	return tx
}

func (tx *Transaction) DeterministicID() string {
	h := sha256.New()

	// 1. CoinBase flag
	if tx.IsCoinbase {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}

	// ==========================================
	// 🚀 關鍵修復：把 Inputs 也加進 Hash 計算裡！
	// ==========================================
	h.Write([]byte{byte(len(tx.Inputs))}) // 寫入 Inputs 數量
	for _, in := range tx.Inputs {
		h.Write([]byte(in.TxID)) // 寫入來源交易 ID

		// 寫入 Index (8 bytes Big Endian)
		idx := make([]byte, 8)
		binary.BigEndian.PutUint64(idx, uint64(in.Index))
		h.Write(idx)

		h.Write([]byte(in.Sig))    // 🌟 我們剛剛加的時間戳就在這裡！現在它終於被算進去了！
		h.Write([]byte(in.PubKey)) // 寫入公鑰
	}
	// ==========================================

	// 3. outputs count
	h.Write([]byte{byte(len(tx.Outputs))})

	// 4. each output
	for _, out := range tx.Outputs {
		// Amount (8 bytes Big Endian)
		amt := make([]byte, 8)
		binary.BigEndian.PutUint64(amt, uint64(out.Amount))
		h.Write(amt)

		// To (public key)
		h.Write([]byte(out.To))
	}

	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

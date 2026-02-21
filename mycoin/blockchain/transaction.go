package blockchain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

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

// 创建Coinbase交易
func NewCoinbase(to string, reward int) *Transaction {
	tx := &Transaction{
		Inputs: nil,
		Outputs: []TxOutput{
			{
				Amount: reward,
				To:     to,
			},
		},
		IsCoinbase: true,
	}

	// 使用稳定序列化（不会乱序）
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
	if tx.IsCoinbase {
		return 0
	}

	inSum := 0
	for _, in := range tx.Inputs {
		out, ok := utxo.Get(in.TxID, in.Index)
		if !ok {
			return 0 // 输入不存在，视为无效或 fee=0
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

	// 按固定顺序写入字段

	// 1. CoinBase flag
	if tx.IsCoinbase {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}

	// 2. outputs count
	h.Write([]byte{byte(len(tx.Outputs))})

	// 3. each output
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

package blockchain

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	ecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

var debugTxSigHash = blockchainEnvBool("MYCOIN_DEBUG_TXSIG")

const sigVerifyCacheLimit = 10000

type sigVerifyCacheEntry struct {
	key string
	ok  bool
}

type sigVerifyLRU struct {
	mu    sync.Mutex
	list  *list.List
	items map[string]*list.Element
	limit int
}

func newSigVerifyLRU(limit int) *sigVerifyLRU {
	if limit <= 0 {
		limit = 1
	}

	return &sigVerifyLRU{
		list:  list.New(),
		items: make(map[string]*list.Element),
		limit: limit,
	}
}

func (c *sigVerifyLRU) Get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.items[key]
	if !found {
		return false, false
	}

	c.list.MoveToFront(elem)
	entry := elem.Value.(sigVerifyCacheEntry)
	return entry.ok, true
}

func (c *sigVerifyLRU) Put(key string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[key]; found {
		elem.Value = sigVerifyCacheEntry{key: key, ok: ok}
		c.list.MoveToFront(elem)
		return
	}

	elem := c.list.PushFront(sigVerifyCacheEntry{key: key, ok: ok})
	c.items[key] = elem

	if c.list.Len() <= c.limit {
		return
	}

	oldest := c.list.Back()
	if oldest == nil {
		return
	}

	entry := oldest.Value.(sigVerifyCacheEntry)
	delete(c.items, entry.key)
	c.list.Remove(oldest)
}

var sigVerifyCache = newSigVerifyLRU(sigVerifyCacheLimit)

func blockchainEnvBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func getSigVerifyCache(key string) (bool, bool) {
	return sigVerifyCache.Get(key)
}

func putSigVerifyCache(key string, ok bool) {
	sigVerifyCache.Put(key, ok)
}

// UTXO input
type TxInput struct {
	TxID   string `json:"txid"`   // 👈 對齊比特幣標準
	Index  int    `json:"index"`  // 👈 對齊比特幣標準
	Sig    string `json:"sig"`    // 👈 簽名
	PubKey string `json:"pubkey"` // 👈 公鑰
}

// UTXO output
type TxOutput struct {
	Amount int    `json:"amount"` // 👈 存的是 YiCent (整數)
	To     string `json:"to"`     // 👈 收款地址
}

// Transaction
type Transaction struct {
	// 🚀 加入 JSON Tags，確保與前端/API 格式完全對齊
	ID         string     `json:"txid"`
	Inputs     []TxInput  `json:"vin"`
	Outputs    []TxOutput `json:"vout"`
	IsCoinbase bool       `json:"is_coinbase"`
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

	cacheKey := tx.Hash()
	if cached, found := getSigVerifyCache(cacheKey); found {
		return cached
	}

	for i, in := range tx.Inputs {
		// 1️⃣ 构造与签名时完全一致的摘要
		data := tx.IDForSig(i)
		hash := sha256.Sum256(data)

		// 2️⃣ 解析 DER 签名（hex → bytes → signature）
		sigBytes, err := hex.DecodeString(in.Sig)
		if err != nil {
			putSigVerifyCache(cacheKey, false)
			return false
		}

		sig, err := ecdsa.ParseDERSignature(sigBytes)
		if err != nil {
			putSigVerifyCache(cacheKey, false)
			return false
		}

		// 3️⃣ 解析公钥
		pubKeyBytes, err := hex.DecodeString(in.PubKey)
		if err != nil {
			putSigVerifyCache(cacheKey, false)
			return false
		}

		pubKey, err := btcec.ParsePubKey(pubKeyBytes)
		if err != nil {
			putSigVerifyCache(cacheKey, false)
			return false
		}

		// 4️⃣ 验签（注意：用 hash，不是 data）
		if !sig.Verify(hash[:], pubKey) {
			putSigVerifyCache(cacheKey, false)
			return false
		}
	}

	putSigVerifyCache(cacheKey, true)
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
	if debugTxSigHash {
		fmt.Printf("\n🕵️ [Debug] IDForSig 準備 Hash 的 JSON: %s\n", string(data))
	}
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

// 修改簽名，增加 mempoolTxs 參數
func (tx *Transaction) Fee(utxo *UTXOSet, mempoolTxs map[string][]byte) int {
	if tx == nil || utxo == nil || tx.IsCoinbase {
		return 0
	}

	inSum := 0
	for _, in := range tx.Inputs {
		// [A] 先查正式帳本
		out, ok := utxo.Get(in.TxID, in.Index)

		if !ok {
			// [B] 🚀 帳本找不到？去 Mempool 找！
			if parentBytes, inMempool := mempoolTxs[in.TxID]; inMempool {
				// 解壓老爸的資料
				parentTx, err := DeserializeTransaction(parentBytes)
				if err == nil && in.Index < len(parentTx.Outputs) {
					inSum += parentTx.Outputs[in.Index].Amount
					continue // 找到老爸的遺產了，繼續處理下一個 Input
				}
			}

			// 如果連 Mempool 都找不到，這筆交易真的是幽靈，回傳 0 是正確的
			return 0
		}
		inSum += out.Amount
	}

	// ... 計算 outSum 和 fee 的部分保持不變 ...
	outSum := 0
	for _, out := range tx.Outputs {
		outSum += out.Amount
	}
	fee := inSum - outSum
	if fee < 0 {
		// 這代表 Inputs < Outputs，有人在試圖造錢！
		// 這裡回傳 0 或一個極小的負值，讓 AddTx 的 MinRelayFee 把他踢掉
		return -1
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

func (tx *Transaction) GetTotalAmount() int {
	total := 0
	for _, out := range tx.Outputs {
		total += out.Amount
	}
	return total
}

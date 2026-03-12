package blockchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"mycoin/utils"
	"time"
)

const DifficultyInterval = 10

// --------------------
// Block Header
// --------------------
// （已移除 CumWork —— cumwork 不属于区块共识内容）
type Block struct {
	Height       uint64
	PrevHash     []byte
	Timestamp    int64
	Nonce        uint64
	Target       *big.Int
	TargetHex    string `json:"target"`
	MerkleRoot   []byte
	MerkleHex    string `json:"merkle"`
	Transactions []Transaction

	Miner  string
	Reward int

	Hash    []byte
	HashHex string `json:"hash"`

	Bits uint32
}

// --------------------
// 创建新区块（不再计算 cumwork）
// --------------------
// --------------------
func NewBlock(
	height uint64,
	prevHash []byte,
	txs []Transaction,
	target *big.Int,
	miner string,
	reward int,
) *Block {

	merkle := ComputeMerkleRoot(txs)

	b := &Block{
		Height:       height,
		PrevHash:     append([]byte(nil), prevHash...),
		Timestamp:    time.Now().Unix(),
		Nonce:        0,
		Target:       new(big.Int).Set(target),
		MerkleRoot:   merkle,
		Transactions: txs,
		Miner:        miner,
		Reward:       reward,
	}

	// 🔥 關鍵修正：自動計算 Bits
	// 這一步確保 Target 被正確壓縮存入 Bits
	b.Bits = utils.BigToCompact(target)

	// 計算 Hash (現在會包含 Bits)
	b.Hash = b.CalcHash()

	return b
}

// --------------------
// PoW 挖矿
// --------------------
func (b *Block) Mine(abort func() bool) bool {
	// 確保 Nonce 從 0 開始 (如果你希望隨機開始也可以不加這行)
	// b.Nonce = 0

	// 使用 MaxUint64 防止溢出導致的死循環
	for b.Nonce < math.MaxUint64 {

		// 🔥🔥🔥【效能優化關鍵】🔥🔥🔥
		// 不要每一次都檢查！每計算 1000 次 Hash 才檢查一次信號。
		// 這樣可以讓 CPU 專注於計算 Hash，而不是一直處理 channel。
		if b.Nonce%1000 == 0 {

			if abort != nil && abort() {
				// 接收到 Network 的「重置信號」，停止當前挖礦
				return false
			}
		}

		// 計算區塊 Hash
		hash := b.CalcHash()

		// 檢查 Hash 是否滿足難度目標
		if hashMeetsTarget(hash, b.Target) {
			b.Hash = hash

			// 挖到了！打印詳細信息
			fmt.Println("=== MINED BLOCK ===")
			fmt.Printf("Height     = %d\n", b.Height)
			fmt.Printf("PrevHash   = %x\n", b.PrevHash)
			fmt.Printf("Timestamp  = %d\n", b.Timestamp)
			fmt.Printf("Bits       = %d\n", b.Bits)
			fmt.Printf("Nonce      = %d\n", b.Nonce)
			fmt.Printf("MerkleRoot = %x\n", b.MerkleRoot)
			fmt.Printf("Hash       = %x\n", b.Hash)

			return true // 成功挖到
		}

		b.Nonce++
	}

	return false // 跑遍了所有 Nonce 都沒挖到 (極低機率)
}

// --------------------
// PoW 验证
// --------------------
func (b *Block) Verify(prev *Block) error {
	if prev != nil {
		if !bytes.Equal(b.PrevHash, prev.Hash) {
			return fmt.Errorf("prev hash mismatch")
		}
		if b.Height != prev.Height+1 {
			return fmt.Errorf("invalid height")
		}
	}

	// 驗證 Hash 是否正確 (Hash 必須包含 Bits 的計算結果)
	hash := b.CalcHash()
	if !hashMeetsTarget(hash, b.Target) {
		return fmt.Errorf("PoW invalid: hash %x > target %x", hash, b.Target)
	}

	for _, tx := range b.Transactions {
		if !tx.Verify() {
			return fmt.Errorf("invalid transaction")
		}
	}

	return nil
}

// --------------------
// Hash 计算（确定性）
// --------------------

func (b *Block) CalcHeader() []byte {
	buf := make([]byte, 0, 128)

	// Helper buffer
	buf8 := make([]byte, 8)
	buf4 := make([]byte, 4)

	// 1. Height (8 bytes)
	binary.LittleEndian.PutUint64(buf8, b.Height)
	buf = append(buf, buf8...)

	// 2. PrevHash (variable)
	buf = append(buf, b.PrevHash...)

	// 3. Timestamp (8 bytes)
	binary.LittleEndian.PutUint64(buf8, uint64(b.Timestamp))
	buf = append(buf, buf8...)

	// 4. Bits (4 bytes)  <-- 核心修正
	binary.LittleEndian.PutUint32(buf4, b.Bits)
	buf = append(buf, buf4...)

	// 5. Nonce (8 bytes)
	binary.LittleEndian.PutUint64(buf8, b.Nonce)
	buf = append(buf, buf8...)

	// 6. MerkleRoot (variable)
	buf = append(buf, b.MerkleRoot...)

	return buf
}

func (b *Block) CalcHash() []byte {
	header := b.CalcHeader()
	h := sha256.Sum256(header)
	return h[:]
}

func hashMeetsTarget(hash []byte, target *big.Int) bool {
	hashInt := new(big.Int).SetBytes(hash)
	return hashInt.Cmp(target) <= 0
}

// --------------------
// 序列化 (JSON)
// --------------------
func (b *Block) Serialize() []byte {
	// 定義臨時結構體，加入 Bits
	view := struct {
		Height       uint64        `json:"height"`
		PrevHash     string        `json:"prev_hash"`
		Timestamp    int64         `json:"timestamp"`
		Nonce        uint64        `json:"nonce"`
		Bits         uint32        `json:"bits"`   // 🔥 寫入 JSON
		Target       string        `json:"target"` // 為了人類可讀保留
		MerkleRoot   string        `json:"merkle_root"`
		Transactions []Transaction `json:"transactions"`
		Miner        string        `json:"miner"`
		Reward       int           `json:"reward"`
		Hash         string        `json:"hash"`
	}{
		Height:       b.Height,
		PrevHash:     hex.EncodeToString(b.PrevHash),
		Timestamp:    b.Timestamp,
		Nonce:        b.Nonce,
		Bits:         b.Bits, // 🔥 賦值
		Target:       utils.FormatTargetHex(b.Target),
		MerkleRoot:   hex.EncodeToString(b.MerkleRoot),
		Transactions: b.Transactions,
		Miner:        b.Miner,
		Reward:       b.Reward,
		Hash:         hex.EncodeToString(b.Hash),
	}

	data, err := json.Marshal(view)
	if err != nil {
		panic(err)
	}
	return data
}

func DeserializeBlock(data []byte) (*Block, error) {

	// 定義臨時結構體，加入 Bits
	var view struct {
		Height       uint64        `json:"height"`
		PrevHash     string        `json:"prev_hash"`
		Timestamp    int64         `json:"timestamp"`
		Nonce        uint64        `json:"nonce"`
		Bits         uint32        `json:"bits"` // 🔥 讀取 JSON
		Target       string        `json:"target"`
		MerkleRoot   string        `json:"merkle_root"`
		Transactions []Transaction `json:"transactions"`
		Miner        string        `json:"miner"`
		Reward       int           `json:"reward"`
		Hash         string        `json:"hash"`
	}

	if err := json.Unmarshal(data, &view); err != nil {
		return nil, err
	}

	prevHashBytes, err := hex.DecodeString(view.PrevHash)
	if err != nil {
		return nil, err
	}

	merkleBytes, err := hex.DecodeString(view.MerkleRoot)
	if err != nil {
		return nil, err
	}

	hashBytes, err := hex.DecodeString(view.Hash)
	if err != nil {
		return nil, err
	}

	// ---------------------------------------------------------
	// 🔥 關鍵修復：從 Bits 還原 Target
	// ---------------------------------------------------------
	// 我們不再信任 view.Target (字串)，而是根據 Bits (共識規則) 還原
	// 這樣保證了 VM 收到的 Target 是正確的
	targetInt := utils.CompactToBig(view.Bits)

	// Build real block
	b := &Block{
		Height:       view.Height,
		PrevHash:     prevHashBytes,
		Timestamp:    view.Timestamp,
		Nonce:        view.Nonce,
		Bits:         view.Bits, // 🔥 賦值
		Target:       targetInt, // 🔥 使用還原後的 Target
		MerkleRoot:   merkleBytes,
		Transactions: view.Transactions,
		Miner:        view.Miner,
		Reward:       view.Reward,
		Hash:         hashBytes,
	}

	return b, nil
}

func ComputeMerkleRoot(txs []Transaction) []byte {
	if len(txs) == 0 {
		empty := sha256.Sum256([]byte{})
		return empty[:]
	}

	var layer [][]byte
	for _, tx := range txs {
		h, _ := hex.DecodeString(tx.ID)
		layer = append(layer, h)
	}

	for len(layer) > 1 {
		var next [][]byte

		for i := 0; i < len(layer); i += 2 {
			if i+1 == len(layer) {
				// duplicate last
				next = append(next, hashPair(layer[i], layer[i]))
			} else {
				next = append(next, hashPair(layer[i], layer[i+1]))
			}
		}

		layer = next
	}

	return layer[0]
}

func hashPair(a, b []byte) []byte {
	h1 := sha256.Sum256(append(a, b...))
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

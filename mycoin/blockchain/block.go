package blockchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"mycoin/utils"
	"time"
)

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
}

// --------------------
// 创建新区块（不再计算 cumwork）
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

	b.Hash = b.CalcHash()

	return b
}

// --------------------
// PoW 挖矿
// --------------------
func (b *Block) Mine(abort func() bool) bool {
	for {
		if abort != nil && abort() {
			return false
		}

		hash := b.CalcHash()

		if hashMeetsTarget(hash, b.Target) {
			b.Hash = hash
			fmt.Println("=== MINED BLOCK ===")
			fmt.Printf("Height     = %d\n", b.Height)
			fmt.Printf("PrevHash   = %x\n", b.PrevHash)
			fmt.Printf("Timestamp  = %d\n", b.Timestamp)
			fmt.Printf("Nonce      = %d\n", b.Nonce)
			fmt.Printf("MerkleRoot = %x\n", b.MerkleRoot)
			fmt.Printf("Header     = %x\n", b.CalcHeader())
			fmt.Printf("Hash       = %x\n", b.Hash)

			return true
		}

		b.Nonce++
	}
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

	hash := b.CalcHash()
	if !hashMeetsTarget(hash, b.Target) {
		return fmt.Errorf("PoW invalid")
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
	tmp := make([]byte, 8)

	// Height (uint64 little-endian)
	binary.LittleEndian.PutUint64(tmp, b.Height)
	buf = append(buf, tmp...)

	// PrevHash (32 bytes)
	buf = append(buf, b.PrevHash...)

	// Timestamp (int64)
	binary.LittleEndian.PutUint64(tmp, uint64(b.Timestamp))
	buf = append(buf, tmp...)

	// Nonce (uint64)
	binary.LittleEndian.PutUint64(tmp, b.Nonce)
	buf = append(buf, tmp...)

	// MerkleRoot (32 bytes)
	buf = append(buf, b.MerkleRoot...)

	return buf
}
func (b *Block) CalcHash() []byte {
	header := b.CalcHeader()
	h := sha256.Sum256(header)
	return h[:]
}

// --------------------
// hash < target 判断
// --------------------
func hashMeetsTarget(hash []byte, target *big.Int) bool {
	hashInt := new(big.Int).SetBytes(hash)
	return hashInt.Cmp(target) <= 0
}

// --------------------
// 序列化
// --------------------
func (b *Block) Serialize() []byte {
	view := struct {
		Height       uint64        `json:"height"`
		PrevHash     string        `json:"prev_hash"`
		Timestamp    int64         `json:"timestamp"`
		Nonce        uint64        `json:"nonce"`
		Target       string        `json:"target"`
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

	// JSON view（字符串格式）
	var view struct {
		Height       uint64        `json:"height"`
		PrevHash     string        `json:"prev_hash"`
		Timestamp    int64         `json:"timestamp"`
		Nonce        uint64        `json:"nonce"`
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

	// Now convert JSON string fields → binary fields
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

	// Target must be restored
	targetInt := new(big.Int)
	targetInt.SetString(view.Target, 16)

	// Build real block
	b := &Block{
		Height:       view.Height,
		PrevHash:     prevHashBytes,
		Timestamp:    view.Timestamp,
		Nonce:        view.Nonce,
		Target:       targetInt,
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

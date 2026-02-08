package miner

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"mycoin/blockchain"
	"mycoin/mempool"
	"mycoin/utils"

	"time"

	"sort"
)

type MinerNode interface {
	GetBestBlock() *blockchain.Block
	GetUTXO() *blockchain.UTXOSet
	GetTarget() *big.Int
	GetReward() int
	GetCurrentTarget() *big.Int
	GetMempool() *mempool.Mempool
	AddBlockInterface(blk *blockchain.Block) error
	BroadcastBlockHash(hashHex string)
	IsSynced() bool
}

type TxPackage struct {
	Txs []*blockchain.Transaction
	Fee int
}

type Miner struct {
	Address string
	Node    MinerNode
}

type SyncChecker interface {
	IsSynced() bool
}

// 创建矿工
func NewMiner(addr string, n MinerNode) *Miner {
	return &Miner{
		Address: addr,
		Node:    n,
	}
}

func (m *Miner) Start() {
	go func() {
		fmt.Println("👷 礦工已啟動，等待同步完成...") // 提示一下

		for {
			// ---------------------------------------------------------
			// 1. 🔥 關鍵修正：同步完成前，絕對禁止挖礦！
			// ---------------------------------------------------------
			// 如果還在下載區塊 (IsSyncing) 或者還沒追上最新高度
			if !m.Node.IsSynced() {
				// 每秒檢查一次，直到同步完成
				time.Sleep(1 * time.Second)
				continue
			}

			// ---------------------------------------------------------
			// 2. (選用) 檢查是否有連線 (避免單機自嗨)
			// ---------------------------------------------------------
			// 雖然這不是必須的，但如果有 PeerCount 方法，建議加上：
			// if m.Node.PeerCount() == 0 {
			//     time.Sleep(2 * time.Second)
			//     continue
			// }

			// ---------------------------------------------------------
			// 3. 開始挖礦 (原本的邏輯)
			// ---------------------------------------------------------
			// fmt.Printf("⛏️ Mining block %d...\n", prev.Height+1)

			block := m.Mine(true)

			if block != nil {
				// 提交區塊
				if err := m.Node.AddBlockInterface(block); err == nil {
					fmt.Printf("🍺 成功挖掘並提交區塊: 高度 %d\n", block.Height)

					// ---------------------------------------------------------
					// ✅ 這裡你寫得很對：挖到一定要廣播！
					// ---------------------------------------------------------
					hashHex := hex.EncodeToString(block.Hash)
					m.Node.BroadcastBlockHash(hashHex)
				}
			} else {
				// 挖礦失敗或暫停時，休息一下避免 CPU 100% 空轉
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

// 矿工挖矿（只负责算块，不管理交易来源）
func (m *Miner) Mine(includeMempool bool) *blockchain.Block {

	// 当前链头（Block，不是 BlockIndex）
	prev := m.Node.GetBestBlock()
	if prev == nil {
		return nil
	}
	originalTip := prev.Hash

	const MaxTxPerBlock = 5
	var txs []blockchain.Transaction
	included := make(map[string]bool)
	totalFee := 0

	// （如果启用 mempool，打包交易）
	if includeMempool {
		pkgs := m.buildPackages()

		sort.Slice(pkgs, func(i, j int) bool {
			return pkgs[i].Fee > pkgs[j].Fee
		})

		for _, pkg := range pkgs {
			for _, tx := range pkg.Txs {
				if len(txs) >= MaxTxPerBlock {
					break
				}
				if included[tx.ID] {
					continue
				}
				txs = append(txs, *tx)
				included[tx.ID] = true
				totalFee += tx.Fee(m.Node.GetUTXO())
			}
		}
	}

	// coinbase
	cb := blockchain.NewCoinbase(
		m.Address,
		m.Node.GetReward()+totalFee,
	)
	txs = append([]blockchain.Transaction{*cb}, txs...)

	// 构造区块
	block := blockchain.NewBlock(
		prev.Height+1,
		prev.Hash,
		txs,
		m.Node.GetCurrentTarget(),
		m.Address,
		m.Node.GetReward(),
	)

	block.Bits = utils.BigToCompact(block.Target)

	// 挖矿，期间检测链头是否更新
	ok := block.Mine(func() bool {
		best := m.Node.GetBestBlock()
		// 🛡️ 增加安全检查：如果此时获取不到最新的完整区块，说明链正在变动或同步中
		if best == nil {
			return true // 返回 true 表示停止当前挖矿任务
		}
		return !bytes.Equal(best.Hash, originalTip)
	})
	if !ok {
		return nil // 链变更，丢弃
	}

	return block
}
func (m *Miner) collectAncestors(txid string, visited map[string]bool) []*blockchain.Transaction {
	if visited[txid] {
		return nil
	}
	visited[txid] = true

	var result []*blockchain.Transaction

	for _, parent := range m.Node.GetMempool().Parents[txid] {
		result = append(result, m.collectAncestors(parent, visited)...)
	}

	txBytes := m.Node.GetMempool().Txs[txid]
	tx, _ := blockchain.DeserializeTransaction(txBytes)

	result = append(result, tx)
	return result
}

func (m *Miner) buildPackages() []TxPackage {
	var pkgs []TxPackage

	for txid := range m.Node.GetMempool().Txs {
		visited := make(map[string]bool)
		txs := m.collectAncestors(txid, visited)

		fee := 0
		for _, tx := range txs {
			fee += tx.Fee(m.Node.GetUTXO())
		}

		pkgs = append(pkgs, TxPackage{
			Txs: txs,
			Fee: fee,
		})
	}

	return pkgs
}

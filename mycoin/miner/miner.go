package miner

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"mycoin/blockchain"
	"mycoin/mempool"

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
		for {
			// ... (前面的檢查 IsSynced, GetBestBlock 保持不變) ...

			// 顯示挖礦日誌
			// fmt.Printf("⛏️ Mining block %d...\n", prev.Height+1)

			block := m.Mine(true)

			if block != nil {
				// 提交區塊
				if err := m.Node.AddBlockInterface(block); err == nil {
					fmt.Printf("🍺 成功挖掘並提交區塊: 高度 %d\n", block.Height)

					// ---------------------------------------------------------
					// 🔴 關鍵修正：挖到塊後，必須主動廣播給全世界！
					// ---------------------------------------------------------
					hashHex := hex.EncodeToString(block.Hash)

					// 呼叫 Node 的廣播接口
					m.Node.BroadcastBlockHash(hashHex)
				}
			} else {
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

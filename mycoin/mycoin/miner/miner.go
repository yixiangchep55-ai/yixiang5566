package miner

import (
	"bytes"
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
			// 1. 同步狀態檢查（做得很好！）
			if !m.Node.IsSynced() {
				time.Sleep(1 * time.Second)
				continue
			}

			prev := m.Node.GetBestBlock()
			if prev == nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}

			// 2. 開始挖礦
			// 建議：傳入當前高度，讓 Mine 內部能感知鏈的變化
			block := m.Mine(true)
			if block == nil {
				continue
			}

			// 3. 提交區塊給 Node
			// 讓 Node 內部去判斷是否要廣播
			if err := m.Node.AddBlockInterface(block); err != nil {
				fmt.Printf("⛏️ 挖出的區塊 %d 提交失敗: %v\n", block.Height, err)
			} else {
				// ✅ 這裡不需要寫 Broadcast，交給 Node 的 AddBlock 邏輯統一處理
				fmt.Printf("🍺 成功挖掘並提交區塊: 高度 %d\n", block.Height)
			}

			// 稍微喘息，避免 CPU 緊繃
			time.Sleep(100 * time.Millisecond)
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

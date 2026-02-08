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
	GetResetChan() chan bool
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

	// 1. 獲取當前鏈頭
	prev := m.Node.GetBestBlock()
	if prev == nil {
		return nil
	}
	originalTip := prev.Hash // 記住我們是基於哪個塊開始挖的

	// --- (中間打包交易的部分保持不變) ---
	const MaxTxPerBlock = 5
	var txs []blockchain.Transaction
	included := make(map[string]bool)
	totalFee := 0

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
	// ------------------------------------

	// 2. 構造區塊
	block := blockchain.NewBlock(
		prev.Height+1,
		prev.Hash,
		txs,
		m.Node.GetCurrentTarget(),
		m.Address,
		m.Node.GetReward(),
	)

	// 確保 Bits 正確設置 (這是我們之前修復的 bug)
	block.Bits = utils.BigToCompact(block.Target)

	// 3. 🔥🔥🔥 關鍵修改：挖礦與中斷檢測 🔥🔥🔥
	ok := block.Mine(func() bool {

		// [新增] 優先檢查信號通道 (這是最快的！)
		// 使用 select + default 實現非阻塞檢查
		select {
		case <-m.Node.GetResetChan(): //
			// 收到 Network 發來的信號：有新塊了！立刻停止！
			return true
		default:
			// 通道是空的，繼續往下執行
		}

		// [原有] 雙重保險：檢查鏈頭是否變更 (防止信號漏接)
		best := m.Node.GetBestBlock()
		if best == nil {
			return true
		}
		// 如果現在的最強塊 Hash 不等於我們剛開始挖的那個 Hash，代表鏈變了，停止！
		return !bytes.Equal(best.Hash, originalTip)
	})

	// 4. 處理結果
	if !ok {
		// 返回 nil 表示「這次挖礦被取消了」，外層迴圈會重新調用 Mine
		return nil
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

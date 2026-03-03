package miner

import (
	"bytes"
	"fmt"
	"math/big"
	"mycoin/blockchain"
	"mycoin/mempool"
	"mycoin/utils"

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

// 矿工挖矿（只负责算块，不管理交易来源）
func (m *Miner) Mine(includeMempool bool) *blockchain.Block {

	// 1. 獲取當前鏈頭 (Best Block)
	prev := m.Node.GetBestBlock()
	if prev == nil {
		return nil
	}
	originalTip := prev.Hash // 記住我們是基於哪個塊開始挖的 (例如高度 39)

	// --- (中間打包交易的部分保持不變) ---
	const MaxTxPerBlock = 5
	var txs []blockchain.Transaction
	included := make(map[string]bool)
	totalFee := 0

	if includeMempool {
		pkgs := m.buildPackages()

		// 1. 按手續費排序 (你原本就有的代碼)
		sort.Slice(pkgs, func(i, j int) bool {
			return pkgs[i].Fee > pkgs[j].Fee
		})

		// ==========================================
		// 🕵️ 大偵探新增：設定最低打包門檻 (放迴圈外面)
		// ==========================================
		const MinPackageFee = 10

		// 2. 開始遍歷包裹
		for _, pkg := range pkgs {

			// ==========================================
			// 🕵️ 大偵探新增：最低打包手續費過濾器！(放外層迴圈的第一行)
			// 如果這包交易 (包含父+子) 的總利潤太低，礦工拒絕打包！
			// ==========================================
			if pkg.Fee < MinPackageFee {
				// 溫馨提示：如果覺得日誌太吵，這行 Printf 可以註解掉
				fmt.Printf("⚠️ [Miner] 忽略低手續費包裹 (總手續費僅 %d 元)\n", pkg.Fee)
				continue // 跳過這個窮酸包裹，直接看下一個！
			}
			// ==========================================

			// 3. 拆開包裹，把交易塞進區塊 (你原本的代碼)
			for _, tx := range pkg.Txs {
				if tx == nil {
					continue
				}

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

	// Coinbase 交易
	cb := blockchain.NewCoinbase(
		m.Address,
		m.Node.GetReward()+totalFee,
		"", // 👈 就是這個空字串！
	)
	// ------------------------------------

	// ==========================================
	// 🚀 關鍵修復：把 cb 塞進交易陣列 txs 的最前面！
	// 這樣 cb 就被「使用」了，編譯器就不會報錯了！
	// ==========================================
	txs = append([]blockchain.Transaction{*cb}, txs...)

	// 2. 構造區塊模板
	block := blockchain.NewBlock(
		prev.Height+1,
		prev.Hash,
		txs, // 👈 現在這個 txs 裡面，已經包含了熱騰騰的 cb 礦工獎勵了！
		m.Node.GetCurrentTarget(),
		m.Address,
		m.Node.GetReward(),
	)

	// 確保 Bits 正確設置 (這是為了網路傳輸驗證)
	block.Bits = utils.BigToCompact(block.Target)

	// 3. 🔥🔥🔥 挖礦與中斷檢測 (核心修改) 🔥🔥🔥
	ok := block.Mine(func() bool {

		// [A] 優先檢查信號通道 (這是最快的！毫秒級響應)
		// 使用 select + default 實現非阻塞檢查
		select {
		case <-m.Node.GetResetChan(): // ✅ 使用介面方法獲取通道
			// fmt.Println("🛑 [Miner] 收到中斷信號，停止挖礦！")
			return true
		default:
			// 通道是空的，繼續往下執行
		}

		// [B] 雙重保險：檢查鏈頭是否變更 (防止信號漏接)
		best := m.Node.GetBestBlock()
		if best == nil {
			return true
		}

		// 如果現在的最強塊 Hash 不等於我們剛開始挖的那個 Hash
		// 代表鏈已經變了 (比如我們原本基於 39 挖，現在 Best 變成 40 了)
		if !bytes.Equal(best.Hash, originalTip) {
			// fmt.Println("🛑 [Miner] 鏈頭已改變，停止挖礦！")
			return true
		}

		return false // 沒有中斷，繼續挖
	})

	// 4. 處理結果
	if !ok {
		// 返回 nil 表示「這次挖礦被取消了」，外層迴圈會重新調用 Mine
		return nil
	}

	// 挖礦成功，返回區塊
	return block
}
func (m *Miner) collectAncestors(txid string, visited map[string]bool) []*blockchain.Transaction {
	if visited[txid] {
		return nil
	}
	visited[txid] = true

	var result []*blockchain.Transaction

	for _, parent := range m.Node.GetMempool().Parents[txid] {
		// 遞迴收集父母，過濾掉 nil 的情況
		parents := m.collectAncestors(parent, visited)
		if parents != nil {
			result = append(result, parents...)
		}
	}

	txBytes, exists := m.Node.GetMempool().Txs[txid]
	// 🛡️ 防護罩 1：確保交易真的還在 Mempool 裡
	if !exists {
		return result
	}

	tx, err := blockchain.DeserializeTransaction(txBytes)
	// 🛡️ 防護罩 2：確保反序列化成功，不是 nil
	if err != nil || tx == nil {
		return result
	}

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

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

	RemoveFromMempool(txID string)

	IsSynced() bool
	GetResetChan() chan bool

	Lock()   // 👈 增加這行
	Unlock() // 👈 增加這行
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
		// 🕵️ 大偵探修改：繞過介面限制，直接計算 pkgs 裡的交易總數！
		// ==========================================
		mempoolSize := 0
		for _, pkg := range pkgs {
			mempoolSize += len(pkg.Txs) // 把每個包裹裡的交易數量加起來
		}

		baseFee := 1 // 基礎底價 (沒人排隊時，至少要 1 元才幫忙打包)

		// 計算擁堵溢價 (Congestion Premium)
		congestionPremium := (mempoolSize / 5) * 2

		// 最終算出來的動態最低手續費
		dynamicMinFee := baseFee + congestionPremium

		fmt.Printf("📊 [Miner 報價中心] 排隊數: %d | 本期門檻: %.2f YiCoin\n", mempoolSize, float64(dynamicMinFee)/100.0)
		// 2. 開始遍歷包裹
		for _, pkg := range pkgs {

			// 🚀 把原本的 MinPackageFee 換成我們算出來的 dynamicMinFee
			if pkg.Fee < dynamicMinFee {
				fmt.Printf("⚠️ [Miner] 忽略低手續費包裹 (%.2f < %.2f)\n", float64(pkg.Fee)/100.0, float64(dynamicMinFee)/100.0)
				continue
			}
			// ==========================================

			if len(txs)+len(pkg.Txs) > MaxTxPerBlock {
				continue // 裝不下就先跳過這個包裹，等下一個區塊再打包
			}

			// 2. 確定裝得下！把「整個包裹的手續費」加進礦工口袋 (只加一次！)
			totalFee += pkg.Fee

			// 3. 拆開包裹，把交易塞進區塊
			for _, tx := range pkg.Txs {
				if tx == nil {
					continue
				}

				// 已經被別人打包過的就跳過
				if included[tx.ID] {
					continue
				}

				// 👷 探長指示：因為我們改用「核彈排毒法 (if n.AddBlock)」，
				// 這裡不再需要 X光安檢門了！直接無腦打包！
				// 毒交易等一下交給 Node.go 裡的 AddBlock 去抓！

				txs = append(txs, *tx)
				included[tx.ID] = true
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

	// 1️⃣ 獲取 Mempool 並鎖定，防止遞迴過程中資料被修改
	mp := m.Node.GetMempool()
	// 這裡建議在呼叫 collectAncestors 的最外層（buildPackages）就上鎖
	// 但如果要在這裡處理，我們需要確保不會造成死鎖 (Deadlock)

	var result []*blockchain.Transaction

	// 🛡️ 防護罩 1：安全讀取 Parents
	parents, pExists := mp.Parents[txid]
	if pExists {
		for _, parent := range parents {
			anc := m.collectAncestors(parent, visited)
			if anc != nil {
				result = append(result, anc...)
			}
		}
	}

	// 🛡️ 防護罩 2：安全讀取交易資料
	txBytes, exists := mp.Txs[txid]
	if !exists || len(txBytes) == 0 { // 👈 多檢查長度，防止 DeserializeTransaction 吃到空字節
		return result
	}

	tx, err := blockchain.DeserializeTransaction(txBytes)
	if err != nil || tx == nil {
		fmt.Printf("⚠️ [Miner] 交易 %s 資料損毀或格式錯誤: %v\n", txid[:8], err)
		return result
	}

	result = append(result, tx)
	return result
}

func (m *Miner) buildPackages() []TxPackage {
	// 1️⃣ 第一步：凍結現場 (上鎖)
	m.Node.Lock()
	defer m.Node.Unlock()

	var pkgs []TxPackage
	// 🕵️ 新增：全域訪問標記，確保每筆交易只被處理一次
	globalVisited := make(map[string]bool)

	// 這裡我們直接操作 mp 簡化代碼
	mp := m.Node.GetMempool()

	for txid := range mp.Txs {
		// 如果這筆交易已經在之前的某個包裹裡了，直接跳過
		if globalVisited[txid] {
			continue
		}

		// 收集這筆交易及其所有未確認的祖先
		localVisited := make(map[string]bool)
		txs := m.collectAncestors(txid, localVisited)

		// 標記這些交易，防止後續重複打包
		for _, t := range txs {
			globalVisited[t.ID] = true
		}

		// ==========================================
		// 🕵️ 透視算帳法：開始算這整包的價值
		// ==========================================
		packageFee := 0
		for _, tx := range txs {
			if tx == nil {
				continue
			}

			totalOut := 0
			for _, out := range tx.Outputs {
				totalOut += out.Amount
			}

			totalIn := 0
			for _, in := range tx.Inputs {
				utxoKey := fmt.Sprintf("%s_%d", in.TxID, in.Index)

				// [A] 先查已確認的帳本
				if utxo, ok := m.Node.GetUTXO().Set[utxoKey]; ok {
					totalIn += utxo.Amount
				} else {
					// [B] 去 Mempool 找未確認的老爸
					if parentTxBytes, exists := mp.Txs[in.TxID]; exists {
						if len(parentTxBytes) > 0 {
							parentTx, _ := blockchain.DeserializeTransaction(parentTxBytes)
							if parentTx != nil && in.Index < len(parentTx.Outputs) {
								totalIn += parentTx.Outputs[in.Index].Amount
							}
						}
					}
				}
			}

			txFee := totalIn - totalOut
			if txFee > 0 {
				packageFee += txFee
			}
		}

		pkgs = append(pkgs, TxPackage{
			Txs: txs,
			Fee: packageFee,
		})
	}

	return pkgs
}

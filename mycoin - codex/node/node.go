package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"mycoin/blockchain"
	"mycoin/database"
	"mycoin/indexer"
	"mycoin/mempool"
	"mycoin/miner"
	"mycoin/utils"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ModeArchive       = "archive"
	ModePruned        = "pruned"
	legacyModeArchive = "archival"
)

const (
	defaultMempoolMaxTx           = 1000
	defaultMempoolMaxBytesArchive = 16 * 1024 * 1024
	defaultMempoolMaxBytesPruned  = 8 * 1024 * 1024
	defaultMempoolTTL             = time.Hour
)

var ErrPrunedData = errors.New("requested data is pruned")
var debugMempoolFlow = nodeEnvBool("MYCOIN_DEBUG_MEMPOOL")

func nodeEnvBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func newNodeMempool(mode string, db *database.BoltDB) *mempool.Mempool {
	maxBytes := defaultMempoolMaxBytesArchive
	if NormalizeMode(mode) == ModePruned {
		maxBytes = defaultMempoolMaxBytesPruned
	}

	return mempool.NewMempool(defaultMempoolMaxTx, maxBytes, defaultMempoolTTL, db)
}

// --------------------
// Node = 验证 + 链管理
// --------------------

type Node struct {
	Chain          []*blockchain.Block
	Mempool        *mempool.Mempool
	UTXO           *blockchain.UTXOSet
	mu             sync.Mutex
	Blocks         map[string]*BlockIndex
	Best           *BlockIndex
	MiningAddress  string
	Orphans        map[string][]*blockchain.Block
	Mode           string
	Target         *big.Int
	Reward         int
	Miner          *miner.Miner
	DB             *database.BoltDB
	MinerResetChan chan bool
	Broadcaster    BlockBroadcaster
	SyncState      SyncState
	IsSyncing      bool
	HeadersSynced  bool
	BodiesSynced   bool
	NodeID         uint64
}

type BlockBroadcaster interface {
	BroadcastNewBlock(b *blockchain.Block)
	BroadcastTransaction(txid string)
	RequestHistoricalBlock(hashHex string)
}

func (n *Node) HasBlock(hash []byte) bool {
	key := hex.EncodeToString(hash)

	n.mu.Lock()
	defer n.mu.Unlock()

	// 1. 检查索引是否存在
	bi, exists := n.Blocks[key]
	if exists {
		// 2. 如果索引存在，且 Block 指针不为空，说明拥有完整区块
		return bi.Block != nil
	}

	// 3. 检查是否在孤块池
	if list, ok := n.Orphans[key]; ok && len(list) > 0 {
		return true
	}

	return false
}

func (n *Node) addOrphanLocked(blk *blockchain.Block) {
	phHex := hex.EncodeToString(blk.PrevHash)
	n.Orphans[phHex] = append(n.Orphans[phHex], blk)
}

func NormalizeMode(mode string) string {
	switch mode {
	case "", ModeArchive, legacyModeArchive:
		return ModeArchive
	case ModePruned:
		return ModePruned
	default:
		return mode
	}
}

func (n *Node) IsArchiveMode() bool {
	return NormalizeMode(n.Mode) == ModeArchive
}

func (n *Node) IsPrunedMode() bool {
	return NormalizeMode(n.Mode) == ModePruned
}

// 辅助函数也需要改
func (n *Node) GetBlockByHash(hashHex string) *blockchain.Block {
	if bi, ok := n.Blocks[hashHex]; ok {
		return bi.Block // 直接返回索引里的 Block 指针
	}
	return nil
}

func computeWork(target *big.Int) *big.Int {
	if target == nil || target.Sign() <= 0 {
		return big.NewInt(1) // 避免除以 0 或負數
	}

	max := new(big.Int).Lsh(big.NewInt(1), 256)
	denom := new(big.Int).Add(target, big.NewInt(1))
	work := new(big.Int).Div(max, denom)

	// 🔥 保險：如果算出來是 0（難度極低時），強制給 1
	// 這樣累積工作量才會增加，Best Chain 才會切換
	if work.Sign() == 0 {
		return big.NewInt(1)
	}
	return work
}

func utxoKey(txid string, index int) string {
	return fmt.Sprintf("%s_%d", txid, index)
}

// --------------------
// 创建新节点（含创世块）
// --------------------
func NewNode(mode string, datadir string) *Node {
	os.MkdirAll(datadir, 0755)
	dbPath := filepath.Join(datadir, "chain.db")
	db := database.OpenDB(dbPath)

	target := new(big.Int)
	target.SetString(
		"00000fffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		16,
	)

	// ==========================================
	// 🌟 探長加碼：在建立 Node 之前，先印製專屬身分證！
	// ==========================================
	myNodeID := rand.Uint64() // 抽出一張隨機的身分證號碼
	// ==========================================

	n := &Node{
		Mode:    NormalizeMode(mode),
		Chain:   []*blockchain.Block{},
		Mempool: newNodeMempool(mode, db),
		UTXO:    blockchain.NewUTXOSet(db),
		Target:  target,
		Reward:  500,
		Blocks:  make(map[string]*BlockIndex), // ✓ 修正
		//	BlockIndex: make(map[string]*blockchain.Block), // ✓ 修正
		Orphans:        make(map[string][]*blockchain.Block),
		DB:             db,
		MinerResetChan: make(chan bool, 1),
		// ==========================================
		// 🚨 探長加碼：給節點戴上「實習生」臂章
		// 確保它一出生就知道自己該先安靜同步！
		// ==========================================
		IsSyncing: true,        // 👈 強制設定為同步中
		SyncState: SyncHeaders, // 👈 設定初始狀態為「抓取標頭」
		// ==========================================
		NodeID: myNodeID,
	}

	// ==========================================================
	// 🚨 探長關鍵加碼：把創世區塊請進記憶體點名簿！
	// ==========================================================
	genesis := blockchain.NewGenesisBlock(target)
	gHash := hex.EncodeToString(genesis.Hash)

	n.Blocks[gHash] = &BlockIndex{
		Hash:       gHash,
		Height:     0,
		Block:      genesis,
		CumWorkInt: big.NewInt(0),
	}
	// 順便把 n.Best 設為創世，這樣同步才有一個起點
	n.Best = n.Blocks[gHash]
	// ==========================================================

	go n.maintainMempool()

	return n
}

// -----------------------------------------------------------------------------
// 🔥 方案 A 核心：Node 主控挖礦邏輯 (請貼在 node/node.go 最後面)
// -----------------------------------------------------------------------------

func (n *Node) Mine() {
	fmt.Println("👷 [Node] 礦工主控程式已啟動...")

	if n.Miner == nil {
		n.Miner = miner.NewMiner(n.MiningAddress, n)
	}

	for {
		// 1. 同步檢查
		if !n.IsSynced() {
			time.Sleep(2 * time.Second)
			continue
		}

		// 2. 挖礦
		newBlock := n.Miner.Mine(true)

		// 3. 處理結果
		if newBlock != nil {
			fmt.Printf("🍺 [Node] 挖礦成功！高度: %d, Hash: %x\n", newBlock.Height, newBlock.Hash)

			if n.AddBlock(newBlock) {
				n.BroadcastNewBlock(newBlock)
			} else {
				fmt.Println("⚠️ [Node] 嚴重警告：自己挖到的區塊驗證失敗")
				for _, tx := range newBlock.Transactions {
					// Coinbase 交易本來就不在 Mempool 裡，踢了也沒影響
					n.RemoveFromMempool(tx.ID)
				}
			}

			// 🔥🔥🔥 關鍵修正：挖到塊之後，強制休息 2 秒！ 🔥🔥🔥
			// 這能確保網路有足夠時間傳播，也解決了 CPU 佔用問題
			fmt.Println("⏳ 挖礦冷卻中 (2秒)...")
			time.Sleep(5 * time.Second)
		} else {
			// 被中斷 (收到別人的塊)
			fmt.Println("🔄 [Node] 偵測到鏈更新，準備重置礦工狀態...")

			// ==========================================================
			// 🌟 探長終極排水術：把 MinerResetChan 抽乾！
			// ==========================================================
			// 既然你上面有 GetResetChan() 做保護，我們直接呼叫它
			ch := n.GetResetChan()

			// 使用非阻塞迴圈抽乾頻道內積壓的所有 true/false 信號
			drainedCount := 0
			for {
				select {
				case <-ch:
					drainedCount++
					// 繼續抽下一個
				default:
					// 信箱終於抽乾了！
					goto Drained
				}
			}
		Drained:
			// 如果你好奇抽掉了幾個，可以取消下面這行的註解來 debug
			// fmt.Printf("🧹 已清空 %d 個積壓的中斷信號\n", drainedCount)

			// 🛡️ 休息 0.5 秒，讓 Node 有時間把新區塊寫進資料庫
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// --------------------
// 添加交易到 Mempool
// --------------------
// --------------------
// 添加交易到 Mempool (終極防護版)
// --------------------
// --------------------
// 添加交易到 Mempool (最終完全體：支援 RBF)
// --------------------
func (n *Node) AddTx(tx blockchain.Transaction, fromNodeID uint64) bool {
	// ==========================================
	// 🕵️ 第一關：門口保全 (手續費檢查)
	// ==========================================
	// 注意：這裡直接從當前 UTXO Set 查手續費
	fee := tx.Fee(n.UTXO, n.Mempool.Txs)
	const MinRelayFee = 1 // 只有手續費 >= 1 元才准進來

	if fee < MinRelayFee {
		fmt.Printf("🚫 [Security] 交易 %s 手續費太低 (%d < %d)，直接在門外踢掉！\n", tx.ID[:8], fee, MinRelayFee)
		return false
	}
	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] 準備鎖定 n.mu 大門...")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] 成功鎖定 n.mu，開始執行 VerifyTx...")
	}

	if err := VerifyTx(tx, n.UTXO, n.Mempool.Txs); err != nil {
		fmt.Printf("❌ 交易驗證失敗被拒絕 (%s): %v\n", tx.ID, err)
		return false
	}

	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] VerifyTx 通過，開始執行 Mempool.Has...")
	}
	if n.Mempool.Has(tx.ID) {
		return false
	}

	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] Mempool.Has 通過，開始執行 Mempool.HasDoubleSpend...")
	}
	if n.Mempool.HasDoubleSpend(&tx) {
		fmt.Printf("❌ 交易被拒絕：與 Mempool 內的交易發生雙花衝突 (%s)\n", tx.ID)
		return false
	}

	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] Mempool.HasDoubleSpend 通過，開始進入 AddTxRBF 黑洞...")
	}
	ok := n.Mempool.AddTxRBF(tx.ID, tx.Serialize(), n.UTXO, fromNodeID)

	if debugMempoolFlow {
		fmt.Println("👉 [X-Ray] 成功逃出 AddTxRBF 黑洞！")
	}
	if !ok {
		fmt.Println("❌ 交易被 Mempool 拒絕 (可能手續費太低或 RBF 失敗)")
		return false
	}

	if debugMempoolFlow {
		fmt.Printf("📥 ✅ [X-Ray] 交易 %s 成功進入 Mempool，等待打包\n", tx.ID)
	}

	return true

}

// --------------------
// 区块追加（主链）
// --------------------
func (n *Node) appendBlock(block *blockchain.Block) {
	// 1️⃣ 加入主链
	n.Chain = append(n.Chain, block)

	// 2️⃣ 更新 UTXO（只做共识状态）
	for _, tx := range block.Transactions {
		if !tx.IsCoinbase {
			n.UTXO.Spend(tx)
		}
		n.UTXO.Add(tx)
	}

	// 3️⃣ 🔥 CPFP：mempool rebuild（关键）
	oldTxs := n.Mempool.Txs
	oldSources := n.Mempool.Sources // 🌟 探長關鍵修正：把來源身分證名冊也備份起來！

	n.Mempool.Reset()

	for txid, txBytes := range oldTxs {
		// 🕵️ 查出這筆交易當初是誰送來的 (如果找不到，預設當作是自己 n.NodeID)
		fromID := n.NodeID
		if id, exists := oldSources[txid]; exists {
			fromID = id
		}

		// 🚀 補上第 4 個參數 fromID！
		if ok := n.Mempool.AddTxRBF(txid, txBytes, n.UTXO, fromID); !ok {
			log.Println("🧹 mempool drop after block:", txid)
		}
	}

	hashHex := hex.EncodeToString(block.Hash)

	n.DB.Put("blocks", hashHex, block.Serialize())
	n.DB.Put("meta", "best", []byte(hashHex))
}

// --------------------
// 添加新区块
// --------------------
func (n *Node) AddBlock(block *blockchain.Block) bool {
	n.mu.Lock()
	// 🔒 我們依然手動管理鎖，不使用 defer，因為我們要精確控制 Unlock 時機

	hashHex := hex.EncodeToString(block.Hash)
	prevHex := hex.EncodeToString(block.PrevHash)

	// 1. 重複檢查
	if bi, exists := n.Blocks[hashHex]; exists {
		if bi.Block != nil {
			n.mu.Unlock() // 🔓 已經有了，安全解鎖
			return true
		}
		// 如果只有 Header 沒 Body，就繼續跑下去補齊它
	}

	// 2. 孤兒檢查
	parentIndex, exists := n.Blocks[prevHex]
	if !exists || parentIndex.Block == nil {
		log.Printf("⚠️ 發現孤塊: %d (缺少父塊 %s)\n", block.Height, prevHex[:8])
		n.addOrphanLocked(block)
		n.mu.Unlock() // 🔓 存入孤兒院，安全解鎖
		return false
	}

	// 3. 核心連接 (此時仍持有鎖，保護 n.Best 和 n.UTXO)
	success := n.connectBlock(block, parentIndex)

	if !success {
		n.mu.Unlock() // 🔓 失敗，安全解鎖
		return false
	}

	// ==========================================
	// 🚀 關鍵修正點：在解鎖前，確保帳本狀態已經同步
	// ==========================================
	// 雖然 connectBlock 裡可能有 Flush，但這裡做最後確認，確保 Kali 重啟必對。
	// n.UTXO.FlushToDB() // 如果 connectBlock 已經做了，這裡可省略

	isMain := false
	curr := n.Best
	for curr != nil {
		if curr.Hash == hashHex {
			isMain = true
			break
		}
		curr = curr.Parent
	}

	// 4. 釋放 Node 鎖
	n.mu.Unlock()

	// 5. 異步/解鎖後的後續處理
	// 清理 Mempool (此時新區塊已接上，Mempool 內的交易已失效)
	n.removeConfirmedTxs(block)

	// 處理孤兒塊：這會啟動遞迴，因為鎖已放開，不會發生死鎖
	go n.attachOrphans(hashHex)

	go indexer.IndexBlock(block, block.Height, isMain)

	if block.Height > 0 && block.Height%100 == 0 && n.SyncState == SyncSynced {
		go n.PruneBlocks()
	}

	return true
}

// --------------------
// 重建主链 (完美退回交易版)
// --------------------
func (n *Node) rebuildChain(oldChain, newChain []*BlockIndex, newTip *BlockIndex) {
	fmt.Printf("🔄 [Reorg] 啟動鏈重組：舊鏈長度 %d -> 新鏈長度 %d\n", len(oldChain), len(newChain))

	// ============================================================
	// 🛠️ 核心修正：同步更新 UTXO 帳本 (必須在更新 n.Chain 之前處理)
	// ============================================================

	// A. 撤銷舊鏈 (Rollback) - 必須由新往舊（Tip 往回走）撤銷
	for i := len(oldChain) - 1; i >= 0; i-- {
		oldBI := oldChain[i]
		if oldBI != nil && oldBI.Block != nil {
			fmt.Printf("⏪ 正在撤銷舊鏈區塊: %d (Hash: %s)\n", oldBI.Height, oldBI.Hash[:8])
			for _, tx := range oldBI.Block.Transactions {
				n.UTXO.Revert(tx)
			}
			// 🚀 【手術刀 1】：同步抹除 PostgreSQL 裡的幽靈數據
			indexer.UnindexBlock(oldBI.Hash)
		}
	}

	// B. 執行新鏈 (Apply) - 必須由舊往新（高度從小到大）執行
	for _, newBI := range newChain {
		if newBI != nil && newBI.Block != nil {
			fmt.Printf("⏩ 正在執行新鏈區塊: %d (Hash: %s)\n", newBI.Height, newBI.Hash[:8])
			for _, tx := range newBI.Block.Transactions {
				if !tx.IsCoinbase {
					n.UTXO.Spend(tx)
				}
				n.UTXO.Add(tx)
			}
			// 🚀 【手術刀 2】：把新鏈的資料重新寫入 PostgreSQL，確保它是最新狀態
			indexer.IndexBlock(newBI.Block, newBI.Height, true)
		}
	}

	// 💾 確保更新後的帳本狀態寫入 BoltDB，防止 Kali 重啟後遺失
	n.UTXO.FlushToDB()

	// ============================================================
	// 1️⃣ 構建完整主鏈陣列 (原始邏輯)
	// ============================================================
	var fullChain []*blockchain.Block
	cur := newTip
	for cur != nil {
		if cur.Block != nil {
			fullChain = append([]*blockchain.Block{cur.Block}, fullChain...)
		}
		cur = cur.Parent
	}

	// 更新 Node 核心指標
	n.Chain = fullChain
	n.Best = newTip

	// ============================================================
	// 2️⃣ 收集新鏈中【已經確認】的交易 ID (原始邏輯)
	// ============================================================
	confirmedInNewChain := make(map[string]bool)
	for _, bi := range newChain {
		if bi != nil && bi.Block != nil {
			for _, tx := range bi.Block.Transactions {
				confirmedInNewChain[tx.ID] = true
			}
		}
	}

	// ============================================================
	// 3️⃣ 找出需要退回 Mempool 的交易 (原始邏輯)
	// ============================================================
	txsToRestore := make(map[string][]byte)

	// A. 抓出舊鏈中沒有被新鏈打包的交易
	for _, old := range oldChain {
		if old != nil && old.Block != nil {
			for _, tx := range old.Block.Transactions {
				if !tx.IsCoinbase && !confirmedInNewChain[tx.ID] {
					txsToRestore[tx.ID] = tx.Serialize()
				}
			}
		}
	}

	// B. 保留原本就在 Mempool 裡，且沒被新鏈打包的交易
	for txid, bytes := range n.Mempool.GetAll() {
		if !confirmedInNewChain[txid] {
			txsToRestore[txid] = bytes
		}
	}

	// ============================================================
	// 4️⃣ 安全地重建 Mempool！ (原始邏輯)
	// ============================================================
	n.Mempool.Clear()
	for txid, bytes := range txsToRestore {
		// 🚀 關鍵防護：直接塞回底層 Map，不觸發複雜驗證，完美避開死鎖！
		n.Mempool.Txs[txid] = bytes
		n.Mempool.TotalBytes += len(bytes)
	}

	// ============================================================
	// 5️⃣ 重建交易索引 (TxIndex) (原始邏輯)
	// ============================================================
	for _, old := range oldChain {
		if old != nil && old.Block != nil {
			n.removeTxIndex(old.Block)
		}
	}
	for _, bi := range newChain {
		if bi != nil && bi.Block != nil {
			n.indexTransactions(bi.Block, bi)
		}
	}

	log.Printf("🔁 鏈重組完成！高度: %d, 已將 %d 筆交易退回 Mempool。\n", newTip.Height, len(txsToRestore))
}

// --------------------
// 查询接口
// --------------------

// 放在 mycoin/node/node.go 中

func (n *Node) Start() {

	fmt.Println("🚀 Node starting...")

	// -----------------------------------------
	// 1️⃣ 讀取 best（檢查 DB 是否存在區塊）
	// -----------------------------------------
	bestHashBytes := n.DB.Get("meta", "best")
	if bestHashBytes == nil {
		fmt.Println("📦 No existing blockchain found. Creating genesis...")
		n.initGenesis()
		return
	}
	bestHash := string(bestHashBytes)

	// -----------------------------------------
	// 2️⃣ 從 index bucket 加載所有 BlockIndex
	// -----------------------------------------
	indexes := make(map[string]*BlockIndex)

	n.DB.Iterate("index", func(k, v []byte) {
		var bi BlockIndex
		json.Unmarshal(v, &bi)
		indexes[bi.Hash] = &bi
	})

	if len(indexes) == 0 {
		fmt.Println("⚠️ 警告：資料庫 meta 有紀錄，但 index 是空的！")
		fmt.Println("🔄 自動重置創世區塊...")
		n.DB.Delete("meta", "best") // reset broken metadata before recreating genesis
		n.Blocks = make(map[string]*BlockIndex)
		n.Chain = nil
		n.UTXO = blockchain.NewUTXOSet(n.DB)
		n.initGenesis()
		return
	}

	// 補回 big.Int
	for _, bi := range indexes {
		bi.CumWorkInt = new(big.Int)
		if bi.CumWork != "" {
			bi.CumWorkInt.SetString(bi.CumWork, 16) // ✅ 確保這裡是 16
		} else {
			bi.CumWorkInt.SetInt64(0)
		}
	}

	// -----------------------------------------
	// 3️⃣ 加載 Block 本體
	// -----------------------------------------
	for _, bi := range indexes {
		raw := n.DB.Get("blocks", bi.Hash)
		if raw != nil {
			blk, err := blockchain.DeserializeBlock(raw)
			if err == nil {
				bi.Block = blk
				bi.Timestamp = blk.Timestamp
				bi.Bits = blk.Bits
				bi.Nonce = blk.Nonce
				bi.MerkleRoot = hex.EncodeToString(blk.MerkleRoot)
			}
		}
	}

	// -----------------------------------------
	// 4️⃣ 重建父子關係
	// -----------------------------------------
	for _, bi := range indexes {
		if bi.PrevHash != "" {
			parent := indexes[bi.PrevHash]
			if parent != nil {
				bi.Parent = parent
				parent.Children = append(parent.Children, bi)
			}
		}
	}

	// -----------------------------------------
	// 5️⃣ 確定 best index (最關鍵的防崩潰點)
	// -----------------------------------------
	bestIndex := indexes[bestHash]
	if bestIndex == nil || bestIndex.Block == nil {
		fmt.Printf("warning: best block metadata is missing or incomplete (%s), rebuilding from index\n", bestHash)

		for _, bi := range indexes {
			if bi == nil || bi.Block == nil {
				continue
			}

			if bestIndex == nil || bestIndex.Block == nil {
				bestIndex = bi
				continue
			}

			workCmp := bi.CumWorkInt.Cmp(bestIndex.CumWorkInt)
			if workCmp > 0 || (workCmp == 0 && bi.Height > bestIndex.Height) {
				bestIndex = bi
			}
		}

		if bestIndex != nil && bestIndex.Block != nil {
			n.DB.Put("meta", "best", []byte(bestIndex.Hash))
			fmt.Printf("repaired best block to %s at height %d\n", bestIndex.Hash, bestIndex.Height)
		}
	}

	// 🔥🔥🔥 絕對防禦：如果這裡是 nil，直接重置，不准往下跑！ 🔥🔥🔥
	if bestIndex == nil || bestIndex.Block == nil {
		fmt.Printf("❌ [Fatal] 資料庫損壞：找不到 BestBlock (Hash: %s)\n", bestHash)
		fmt.Println("🧹 正在清除錯誤的 meta 標籤，請重新啟動節點...")
		n.DB.Delete("meta", "best")
		n.Blocks = make(map[string]*BlockIndex)
		n.Chain = nil
		n.UTXO = blockchain.NewUTXOSet(n.DB)
		n.initGenesis()
		return // 👈 強制結束，防止後面報錯
	}

	n.Best = bestIndex
	n.Blocks = indexes

	// -----------------------------------------
	// 6️⃣ 重建鏈
	// -----------------------------------------
	var chain []*blockchain.Block
	cur := bestIndex

	for cur != nil {
		if cur.Block != nil {
			chain = append([]*blockchain.Block{cur.Block}, chain...)
		}
		cur = cur.Parent
	}

	n.Chain = chain

	// 這裡就是你原本報錯的 466 行，現在 bestIndex 絕對不可能是 nil 了
	fmt.Printf("🏗  Loaded %d blocks from DB. Best height = %d\n",
		len(chain), bestIndex.Height)

	// ... (後面的 UTXO 和 Mempool 加載代碼保持不變) ...
	// 請確認後面還有加載 UTXO 和 Mempool 的代碼，不要漏掉了

	// -----------------------------------------
	// 7️⃣ 重建 UTXO
	// -----------------------------------------
	n.UTXO = blockchain.NewUTXOSet(n.DB)
	n.DB.Iterate("utxo", func(k, v []byte) {
		var u blockchain.UTXO
		if err := json.Unmarshal(v, &u); err != nil {
			return
		}

		key := string(k)
		n.UTXO.Set[key] = u
		n.UTXO.AddrIndex[u.To] = append(n.UTXO.AddrIndex[u.To], key)
	})
	// ... (Mempool 初始代碼) ...
	n.Mempool = newNodeMempool(n.Mode, n.DB)
	n.loadMempool()
	n.IsSyncing = true

	// ... (狀態設定) ...
	if n.Best == nil || n.Best.Height == 0 {
		n.SyncState = SyncIBD
		fmt.Println("🆕 Fresh node, starting IBD...")
	} else {
		n.SyncState = SyncHeaders
		fmt.Printf("📥 Resuming sync from height %d...\n", n.Best.Height)
	}

	fmt.Println("✅ Node is ready and searching for peers...")
}
func (n *Node) initGenesis() {
	genesis := blockchain.NewGenesisBlock(n.Target)

	// =========================================================
	// 🔥 符合現實的寫法：以 Bits 為準 (Bits as Truth) 🔥
	// =========================================================

	// 即使我們是創世者，我們也要模擬「從網路上收到這個區塊」的過程。
	// 我們將 Bits 還原為 big.Int，這會丟失末位的精度，但这才是全網共識的 Target。
	consensusTarget := utils.CompactToBig(genesis.Bits)

	// 使用這個「共識 Target」來計算工作量
	work := computeWork(consensusTarget)

	// =========================================================

	hashHex := hex.EncodeToString(genesis.Hash)
	// 🔴 核心修改：确保 bi 结构体包含了 Block 本体
	bi := &BlockIndex{
		Block:      genesis, // 挂载本体
		Hash:       hashHex,
		Height:     0,
		CumWork:    work.Text(16),
		CumWorkInt: work,
		Parent:     nil,
		Children:   []*BlockIndex{}, // 养成初始化切片的好习惯

		Bits:       genesis.Bits,
		Timestamp:  genesis.Timestamp,
		Nonce:      genesis.Nonce,
		MerkleRoot: hex.EncodeToString(genesis.MerkleRoot),
	}

	// --- 写入数据库 ---
	n.DB.Put("blocks", hashHex, genesis.Serialize())

	idxBytes, _ := json.Marshal(bi)
	n.DB.Put("index", hashHex, idxBytes)

	n.DB.Put("meta", "best", []byte(hashHex))

	// ---------------------------------------------------------
	// 🔴 关键修改点：只保留一个 Map 的写入
	// ---------------------------------------------------------

	// 写入唯一索引库 (BlockIndex 内部已经持有 genesis 指针)
	n.Blocks[hashHex] = bi

	// ❌ 删掉这行：n.BlockIndex[hashHex] = genesis

	n.Best = bi

	// 主链视图 (如果你依然想保留 n.Chain 这个切片的话)
	n.Chain = []*blockchain.Block{genesis}

	// 更新 UTXO
	n.UTXO.Add(genesis.Transactions[0])

	fmt.Println("🪐 Genesis block created.")
	fmt.Printf("🔍 [Init] Genesis Bits: %d (預期: 504365055)\n", bi.Bits)
	fmt.Println("GENESIS TARGET =", utils.FormatTargetHex(genesis.Target))
}

func (n *Node) GetChain() []*blockchain.Block {
	return n.Chain
}

func (n *Node) GetMainChainIndexByHeight(height uint64) *BlockIndex {
	n.mu.Lock()
	defer n.mu.Unlock()

	cur := n.Best
	for cur != nil && cur.Height > height {
		cur = cur.Parent
	}
	if cur != nil && cur.Height == height {
		return cur
	}
	return nil
}

func (n *Node) GetUTXO() *blockchain.UTXOSet {
	return n.UTXO
}

func (n *Node) GetTarget() *big.Int {
	return n.Target
}

func (n *Node) GetBestIndex() interface{} {
	return n.Best
}

func (n *Node) GetReward() int {
	return n.Reward
}

func (n *Node) GetMempool() *mempool.Mempool {
	return n.Mempool
}

func (n *Node) AddBlockInterface(blk *blockchain.Block) error {
	if ok := n.AddBlock(blk); ok {
		return nil
	}
	return fmt.Errorf("block rejected: %s", blk.Hash)
}

func (n *Node) GetBestBlock() *blockchain.Block {
	// 🛡️ 确保 Best 不为空且包含 Block 实体数据
	if n.Best == nil || n.Best.Block == nil {
		return nil
	}
	return n.Best.Block
}

func (n *Node) PrintChainStatus() {
	fmt.Println("📌 Chain Status")
	fmt.Println("Height:", n.Best.Height)
	fmt.Println("Target:", n.Best.Block.Target.Text(16))
	fmt.Println("CumWork:", n.Best.CumWorkInt.String())
}

// RebuildUTXO rebuilds the full UTXO set from the chain stored in n.Chain.
func (n *Node) RebuildUTXO() error {
	n.mu.Lock() // 🚨 這裡要鎖住，確保重建時沒人亂動帳本
	defer n.mu.Unlock()
	fmt.Println("🔄 [Full Rebuild] 啟動全帳本重建...")

	// 0️⃣ 核心防護：確保主鏈視圖是最新的
	n.UpdateChainFromBest()
	if n.IsPrunedMode() && len(n.Chain) > 0 && n.Chain[0] != nil && n.Chain[0].Height != 0 {
		fmt.Printf("ℹ️ [Full Rebuild] pruned 模式目前只保留從高度 %d 開始的區塊實體，沿用現有 UTXO 狀態。\n", n.Chain[0].Height)
		return nil
	}

	// 1️⃣ 創建一個「純記憶體」的臨時帳本 (不要傳入 DB)
	// 這樣在遍歷區塊時，Spend 和 Add 只會改記憶體，不會頻繁寫硬碟
	tmpUtxo := blockchain.NewUTXOSet(nil)

	// 2️⃣ 遍歷主鏈
	txCount := 0
	var prevBlock *blockchain.Block
	for _, block := range n.Chain {
		if block == nil {
			continue
		}

		if err := VerifyBlockWithUTXO(block, prevBlock, tmpUtxo); err != nil {
			return fmt.Errorf("chain validation failed at height %d: %w", block.Height, err)
		}

		for _, tx := range block.Transactions {
			if !tx.IsCoinbase {
				if err := tmpUtxo.Spend(tx); err != nil {
					return fmt.Errorf("failed to apply tx %s at height %d: %w", tx.ID, block.Height, err)
				}
			}
			tmpUtxo.Add(tx)
			txCount++
		}

		prevBlock = block
	}

	// 3️⃣ 準備切換：將臨時帳本重新關聯到正式 DB
	tmpUtxo.DB = n.DB

	// 4️⃣ 執行「一次性」落地
	// 使用你剛寫好的 FlushToDB，它會清空舊 bucket 並把最新結果一次鎖定
	fmt.Printf("💾 重建完成 (共 %d 筆交易)，正在執行批次存檔...\n", txCount)
	tmpUtxo.FlushToDB()

	// 5️⃣ 替換正式指標
	n.UTXO = tmpUtxo

	fmt.Println("✅ [Full Rebuild] 重建成功，帳本已完全同步。")
	return nil
}

func (n *Node) AllBodiesDownloaded() bool {
	for _, bi := range n.Blocks {
		// 只要有一個索引沒掛載 Block 實體，就沒下載完
		if bi == nil || bi.Block == nil || len(bi.Block.Transactions) == 0 {
			return false
		}
	}
	return true
}

func (n *Node) AddOrphan(blk *blockchain.Block) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.addOrphanLocked(blk)
}

func (n *Node) GetTxIndex(txid string) (*blockchain.TxIndexEntry, error) {
	data := n.DB.Get("txindex", txid)
	if data == nil {
		return nil, fmt.Errorf("tx not found")
	}

	var idx blockchain.TxIndexEntry
	json.Unmarshal(data, &idx)
	return &idx, nil
}

func (n *Node) GetTransaction(txid string) (*blockchain.Transaction, *blockchain.Block, error) {
	idx, err := n.GetTxIndex(txid)
	if err != nil {
		return nil, nil, err
	}

	block := n.GetBlockForQuery(idx.BlockHash)
	if block == nil {
		return nil, nil, ErrPrunedData
	}

	// 安全检查
	if idx.TxOffset < 0 || idx.TxOffset >= len(block.Transactions) {
		return nil, nil, fmt.Errorf("invalid TxOffset in txindex")
	}

	tx := &block.Transactions[idx.TxOffset]

	return tx, block, nil
}

func (n *Node) loadMempool() {
	loadedCount := 0

	n.DB.Iterate("mempool", func(k, v []byte) {
		txid := string(k)

		// 放入内存 mempool
		n.Mempool.Txs[txid] = v
		n.Mempool.TotalBytes += len(v)

		timeBytes := n.DB.Get("mempool_times", txid)
		if len(timeBytes) > 0 {
			if parsed, err := strconv.ParseInt(string(timeBytes), 10, 64); err == nil {
				n.Mempool.Times[txid] = parsed
			}
		}

		sourceBytes := n.DB.Get("mempool_sources", txid)
		if len(sourceBytes) > 0 {
			if parsed, err := strconv.ParseUint(string(sourceBytes), 10, 64); err == nil {
				n.Mempool.Sources[txid] = parsed
			}
		}

		// ⭐ 重建 parent 依赖信息（你的逻辑）
		tx, err := blockchain.DeserializeTransaction(v)
		if err == nil {
			for _, in := range tx.Inputs {
				if in.TxID == "" {
					continue
				}

				parent := in.TxID
				n.Mempool.Parents[txid] =
					append(n.Mempool.Parents[txid], parent)
				n.Mempool.Children[parent] =
					append(n.Mempool.Children[parent], txid)
				n.Mempool.Spent[fmt.Sprintf("%s_%d", in.TxID, in.Index)] = txid
			}
		}

		loadedCount++
	})

	removed := n.Mempool.PruneExpired()
	remainingCount := len(n.Mempool.GetAll())
	if removed > 0 {
		log.Printf("🧹 [Mempool TTL] 啟動時清理了 %d 筆過期交易\n", removed)
	}

	log.Printf("💾 Loaded %d mempool transactions from DB (%d expired, %d remaining)\n", loadedCount, removed, remainingCount)
}

func (n *Node) maintainMempool() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if n.Mempool == nil {
			continue
		}
		if removed := n.Mempool.PruneExpired(); removed > 0 {
			log.Printf("🧹 [Mempool TTL] 已清理 %d 筆過期交易\n", removed)
		}
	}
}

func (n *Node) BroadcastNewBlock(b *blockchain.Block) {
	if n.Broadcaster != nil {
		// 這裡會呼叫 network/handle.go 裡面的實作
		n.Broadcaster.BroadcastNewBlock(b)
	}
}

func (n *Node) AddHeader(bi *BlockIndex) {
	hashHex := bi.Hash
	// 若已存在，不重复加入
	if _, ok := n.Blocks[hashHex]; ok {
		return
	}

	// 写入 header-only 索引库
	n.Blocks[hashHex] = bi

	// 若高度更高，则更新 best
	if n.Best == nil || bi.Height > n.Best.Height {
		n.Best = bi
	}
}

func (n *Node) GetBlocksWithoutBody() []string {
	list := []string{}
	for hash, bi := range n.Blocks {
		if bi.Block == nil { // header-only
			list = append(list, hash)
		}
	}
	return list
}

func (n *Node) UpdateChainFromBest() {
	var newChain []*blockchain.Block
	cur := n.Best

	// 從 Best 往前找 Parent，直到 Genesis，構建新的主鏈視圖
	for cur != nil {
		if cur.Block != nil {
			newChain = append([]*blockchain.Block{cur.Block}, newChain...)
		}
		cur = cur.Parent
	}
	n.Chain = newChain
	log.Printf("⛓️ Chain view updated. New Height: %d, Tip: %s", n.Best.Height, n.Best.Hash)
}

func (n *Node) FindCommonAncestor(locator []string) *BlockIndex {
	// locator 中找到第一个已知区块（从最近到最远）
	for _, hash := range locator {
		if bi, ok := n.Blocks[hash]; ok {
			return bi
		}
	}

	// 找不到，返回 genesis
	return n.GetMainChainIndexByHeight(0)
}

func (n *Node) IsSynced() bool {
	return n.SyncState == SyncSynced
}

func (n *Node) updateUTXO(block *blockchain.Block) {
	fmt.Printf("📂 [UTXO] 正在更新區塊 %d 的帳本狀態...\n", block.Height)

	for _, tx := range block.Transactions {
		// 1. 移除已花費的輸出 (Inputs)
		if !tx.IsCoinbase {
			err := n.UTXO.Spend(tx)
			if err != nil {
				// 🛡️ 偵探提醒：如果在更新主鏈時發現錢不夠花，這是嚴重的共識錯誤
				fmt.Printf("❌ [Fatal] 區塊 %d 交易 %s 花費失敗: %v\n", block.Height, tx.ID[:8], err)
				// 在實際生產環境，這裡可能需要觸發鏈回滾或停止同步
			}
		}

		// 2. 添加新產生的輸出 (Outputs)
		n.UTXO.Add(tx)
	}

	// ==========================================
	// 🚀 關鍵新增：確保每一磚一瓦都刻在硬碟上
	// ==========================================
	// 這樣即使在下一個區塊進來前斷電，Kali 重啟後餘額依然是正確的
	n.UTXO.FlushToDB()
	// ==========================================
}

func (n *Node) addTxsToMempool(txs []blockchain.Transaction) {
	for _, tx := range txs {
		// Coinbase 交易無法復活 (因為它們只在特定高度有效，且憑空產生)
		if !tx.IsCoinbase {
			// 使用 AddTxRBF 嘗試加入，如果 Mempool 滿了或有衝突會自動處理
			n.Mempool.AddTxRBF(tx.ID, tx.Serialize(), n.UTXO, n.NodeID)
		}
	}
}

func (n *Node) IsOnMainChain(bi *BlockIndex) bool {
	if bi == nil || n.Best == nil || bi.Height > n.Best.Height {
		return false
	}

	// Archive 节点可直接从完整主链视图命中。
	if bi.Height < uint64(len(n.Chain)) {
		mainBlock := n.Chain[bi.Height]
		if mainBlock != nil && hex.EncodeToString(mainBlock.Hash) == bi.Hash {
			return true
		}
	}

	// Pruned 节点的 n.Chain 可能只保留最近一段实体，退回到 Best 指针沿父链回溯判断。
	for cur := n.Best; cur != nil && cur.Height >= bi.Height; cur = cur.Parent {
		if cur.Height == bi.Height {
			return cur.Hash == bi.Hash
		}
	}

	return false
}

func (n *Node) GetResetChan() chan bool {
	// 確保不會返回 nil (如果初始化忘了 make)
	if n.MinerResetChan == nil {
		n.MinerResetChan = make(chan bool, 1)
	}
	return n.MinerResetChan
}

// HasMissingBodies 檢查本地索引中是否存有「有頭無身」的區塊
func (n *Node) HasMissingBodies() bool {
	if n.Best == nil {
		return false
	}

	// n.Chain 是目前已正式掛載的主鏈視圖。
	// 同步時只需要檢查從新 best 往回走，到既有主鏈錨點之前是否仍有 header-only 區塊；
	// 更早以前被 prune 的歷史缺口不應算成同步未完成。
	committed := make(map[string]struct{}, len(n.Chain))
	for _, block := range n.Chain {
		if block == nil || len(block.Hash) == 0 {
			continue
		}
		committed[hex.EncodeToString(block.Hash)] = struct{}{}
	}

	for cur := n.Best; cur != nil && cur.Height > 0; cur = cur.Parent {
		if cur.Block == nil {
			return true
		}
		if _, ok := committed[cur.Hash]; ok {
			return false
		}
	}

	return false
}

func (n *Node) Lock() {
	n.mu.Lock()
}

// Unlock 公開的解鎖函數
func (n *Node) Unlock() {
	n.mu.Unlock()
}

func (n *Node) RemoveFromMempool(txID string) {
	if n.Mempool != nil {
		n.Mempool.Remove(txID)
	}
}

func (n *Node) EvaluateSyncStatus(peerHeight uint64, peerWork *big.Int) {
	n.mu.Lock()
	defer n.mu.Unlock()

	localWork := big.NewInt(0)
	if n.Best != nil && n.Best.CumWorkInt != nil {
		localWork = n.Best.CumWorkInt
	}

	// 🚨 只有當鄰居比我強時，才准切換狀態進入同步
	if peerWork != nil && peerWork.Cmp(localWork) > 0 {
		if !n.IsSyncing {
			fmt.Printf("🛰️ [Network] 發現強大鄰居 (%x > %x)，啟動同步模式...\n", peerWork, localWork)
		}
		n.IsSyncing = true
		n.SyncState = SyncHeaders
	} else {
		// 💡 鄰居比我弱？那就不理他，保持現狀。
		// 絕對不要在這裡寫 n.IsSyncing = false ！！！
		fmt.Printf("✅ [Network] 鄰居較弱或相等，維持當前狀態 (IsSyncing: %v)\n", n.IsSyncing)
	}
}

// GetBlockForQuery 提供給 API 或前端查詢區塊用
func (n *Node) GetBlockForQuery(hashHex string) *blockchain.Block {
	// 1. 先查本地有沒有 (如果還沒被修剪，或是自己就是 archival，就會命中這裡)
	bi, ok := n.Blocks[hashHex]
	if !ok {
		return nil
	}
	if bi.Block != nil {
		return bi.Block
	}

	// 2. 本地沒有！如果我們配置了 Broadcaster，就去向全節點求救！
	if n.IsPrunedMode() && n.Broadcaster != nil {
		fmt.Printf("📦 [Node] 區塊 %s 不在本地硬碟，觸發 P2P 歷史調度...\n", hashHex[:8])
		n.Broadcaster.RequestHistoricalBlock(hashHex)
	}

	// 回傳 nil，代表「正在查詢中」或「找不到」。
	// (在實際應用中，前端可以提示用戶「正在從網路同步歷史資料，請稍後再試」)
	return nil
}

func (n *Node) AttachHistoricalBlock(block *blockchain.Block) error {
	if block == nil {
		return fmt.Errorf("nil block")
	}

	hashHex := hex.EncodeToString(block.CalcHash())
	if len(block.Hash) > 0 && !bytes.Equal(block.Hash, block.CalcHash()) {
		return fmt.Errorf("historical block hash mismatch")
	}

	n.mu.Lock()
	bi := n.Blocks[hashHex]
	if bi == nil {
		n.mu.Unlock()
		return fmt.Errorf("unknown historical block")
	}
	if bi.Block != nil {
		n.mu.Unlock()
		return nil
	}
	if bi.Height != block.Height || bi.PrevHash != hex.EncodeToString(block.PrevHash) || bi.Bits != block.Bits {
		n.mu.Unlock()
		return fmt.Errorf("historical block header mismatch")
	}
	if bi.Timestamp != 0 && bi.Timestamp != block.Timestamp {
		n.mu.Unlock()
		return fmt.Errorf("historical block timestamp mismatch")
	}
	if bi.Nonce != 0 && bi.Nonce != block.Nonce {
		n.mu.Unlock()
		return fmt.Errorf("historical block nonce mismatch")
	}
	if bi.MerkleRoot != "" && bi.MerkleRoot != hex.EncodeToString(block.MerkleRoot) {
		n.mu.Unlock()
		return fmt.Errorf("historical block merkle mismatch")
	}
	n.mu.Unlock()

	if !bytes.Equal(blockchain.ComputeMerkleRoot(block.Transactions), block.MerkleRoot) {
		return fmt.Errorf("historical block merkle does not match transactions")
	}
	if err := block.Verify(nil); err != nil {
		return fmt.Errorf("historical block basic verification failed: %w", err)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	bi = n.Blocks[hashHex]
	if bi == nil {
		return fmt.Errorf("historical block disappeared")
	}
	if bi.Block != nil {
		return nil
	}

	bi.Block = block
	bi.Timestamp = block.Timestamp
	bi.Bits = block.Bits
	bi.Nonce = block.Nonce
	bi.MerkleRoot = hex.EncodeToString(block.MerkleRoot)

	if parent := n.Blocks[bi.PrevHash]; parent != nil {
		bi.Parent = parent
	}

	n.DB.Put("blocks", hashHex, block.Serialize())
	idxBytes, _ := json.Marshal(bi)
	n.DB.Put("index", hashHex, idxBytes)
	n.markBlockTransactionsPruned(hashHex, block, false)
	return nil
}

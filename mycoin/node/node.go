package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/database"
	"mycoin/mempool"
	"mycoin/miner"
	"mycoin/utils"
	"os"
	"path/filepath"
)

// --------------------
// Node = 验证 + 链管理
// --------------------

type Node struct {
	Chain   []*blockchain.Block
	Mempool *mempool.Mempool
	UTXO    *blockchain.UTXOSet

	// ✔ BlockIndex 数据库（hashHex → block index）
	Blocks map[string]*BlockIndex

	// ✔ Complete block database（hashHex → complete block）
	//BlockIndex map[string]*blockchain.Block

	Best          *BlockIndex
	MiningAddress string
	Orphans       map[string][]*blockchain.Block

	Mode   string
	Target *big.Int
	Reward int

	Miner          *miner.Miner
	DB             *database.BoltDB
	MinerResetChan chan bool

	Broadcaster BlockBroadcaster

	SyncState     SyncState
	IsSyncing     bool
	HeadersSynced bool
	BodiesSynced  bool
}

type BlockBroadcaster interface {
	BroadcastNewBlock(hashHex string)
}

func (n *Node) HasBlock(hash []byte) bool {
	key := hex.EncodeToString(hash)

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

// 辅助函数也需要改
func (n *Node) GetBlockByHash(hashHex string) *blockchain.Block {
	if bi, ok := n.Blocks[hashHex]; ok {
		return bi.Block // 直接返回索引里的 Block 指针
	}
	return nil
}

func computeWork(target *big.Int) *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 256)
	return new(big.Int).Div(max, new(big.Int).Add(target, big.NewInt(1)))
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

	n := &Node{
		Mode:    mode,
		Chain:   []*blockchain.Block{},
		Mempool: mempool.NewMempool(1000, db),
		UTXO:    blockchain.NewUTXOSet(db),
		Target:  target,
		Reward:  100,
		Blocks:  make(map[string]*BlockIndex), // ✓ 修正
		//	BlockIndex: make(map[string]*blockchain.Block), // ✓ 修正
		Orphans:        make(map[string][]*blockchain.Block),
		DB:             db,
		MinerResetChan: make(chan bool, 1),
	}

	return n
}

// --------------------
// 添加交易到 Mempool
// --------------------
func (n *Node) AddTx(tx blockchain.Transaction) bool {

	// ⭐ 0️⃣ 检查「同一交易内部」是否重复花费同一个 UTXO
	seen := map[string]bool{}
	for _, in := range tx.Inputs {
		key := utxoKey(in.TxID, in.Index)
		if seen[key] {
			fmt.Println("❌ 交易内部重复输入（double spend in same tx）")
			return false
		}
		seen[key] = true
	}

	// 1️⃣ 校验输入是否存在（confirmed UTXO 或 mempool 父交易）
	for i, in := range tx.Inputs {
		if n.UTXO.Exists(in.TxID, in.Index, in.PubKey) {
			continue
		}

		if n.Mempool.Has(in.TxID) {
			continue
		}

		fmt.Printf("❌ 输入 %d 不存在（非 confirmed / 非 mempool）\n", i)
		return false
	}

	// 2️⃣ 校验签名
	if !tx.Verify() {
		fmt.Println("❌ 交易签名不合法")
		return false
	}

	// 3️⃣ 计算 txid
	txid := tx.Hash()

	// 4️⃣ 去重（同 txid）
	if n.Mempool.Has(txid) {
		fmt.Println("ℹ️ 交易已存在于 Mempool")
		return false
	}

	// 5️⃣ 加入 mempool（双花 / RBF / eviction 都在这里）
	ok := n.Mempool.AddTxRBF(
		txid,
		tx.Serialize(),
		n.UTXO,
	)

	if !ok {
		fmt.Println("❌ 交易被拒绝（双花 / fee 过低 / RBF 失败）")
		return false
	}

	fmt.Println("✅ 交易进入 Mempool")
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
	old := n.Mempool.Txs
	n.Mempool.Reset()

	for txid, txBytes := range old {
		if ok := n.Mempool.AddTxRBF(txid, txBytes, n.UTXO); !ok {
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
	fmt.Printf("\n📥 [Node] 嘗試處理區塊: 高度 %d, Hash: %x\n", block.Height, block.Hash)
	hashHex := hex.EncodeToString(block.Hash)
	prevHex := hex.EncodeToString(block.PrevHash)

	// A. 检查是否已存在 (這部分保持不變)
	if bi, exists := n.Blocks[hashHex]; exists {
		if bi.Block == nil {
			fmt.Printf("📥 收到區塊體，補齊資料: 高度 %d\n", bi.Height)
			bi.Block = block
		} else {
			return false
		}
	} else {
		// B. 如果連索引都沒有，建立新索引

		// 1. 計算工作量
		cumWork := WorkFromTarget(block.Target)

		newBi := &BlockIndex{
			Hash:     hashHex,
			PrevHash: prevHex,
			Height:   block.Height,
			Block:    block,

			// 🔥🔥🔥 關鍵修正：必須把時間戳存入索引 🔥🔥🔥
			Timestamp: block.Timestamp,

			// 建議也把 Bits (Target壓縮版) 存入，如果你的 BlockIndex 結構有加的話
			// Bits: block.Bits,

			CumWorkInt: cumWork,
			Children:   []*BlockIndex{},
		}

		// 補上字串版工作量
		newBi.CumWork = newBi.CumWorkInt.String()

		n.Blocks[hashHex] = newBi
	}

	// ... (後面的父區塊檢查與 connectBlock 保持不變) ...
	parent, parentExists := n.Blocks[prevHex]

	if !parentExists {
		log.Printf("📦 发现孤块 (缺少父块 %s): %s\n", prevHex, hashHex)
		n.Orphans[prevHex] = append(n.Orphans[prevHex], block)
		return false
	}

	return n.connectBlock(block, parent)
}

// --------------------
// 重建主链 + UTXO
// --------------------
func (n *Node) rebuildChain(oldChain, newChain []*BlockIndex, newTip *BlockIndex) {

	// 1️⃣ 构建完整主链
	fullChain := []*blockchain.Block{}
	cur := newTip
	for cur != nil {
		fullChain = append([]*blockchain.Block{cur.Block}, fullChain...)
		cur = cur.Parent
	}

	// -----------------------------
	// 2️⃣ 先重建 UTXO（必须先做）
	// -----------------------------
	utxo := blockchain.NewUTXOSet(n.DB)
	for _, blk := range fullChain {
		for _, tx := range blk.Transactions {
			if !tx.IsCoinbase {
				utxo.Spend(tx)
			}
			utxo.Add(tx)
		}
	}
	n.UTXO = utxo

	// -----------------------------
	// 3️⃣ 再 rebuild mempool（用新 UTXO）
	// -----------------------------
	confirmed := make(map[string]bool)
	for _, blk := range fullChain {
		for _, tx := range blk.Transactions {
			confirmed[tx.ID] = true
		}
	}

	oldMempool := n.Mempool.GetAll()
	n.Mempool.Clear()

	for txid, bytes := range oldMempool {
		if confirmed[txid] {
			continue
		}
		n.Mempool.AddTxRBF(txid, bytes, n.UTXO)
	}

	// -----------------------------
	// 4️⃣ txindex 重建
	// -----------------------------
	for _, old := range oldChain {
		n.removeTxIndex(old.Block)
	}
	for _, bi := range newChain {
		n.indexTransactions(bi.Block, bi)
	}

	// -----------------------------
	// 5️⃣ 更新 Node 状态
	// -----------------------------
	n.Chain = fullChain
	n.Best = newTip

	log.Println("🔁 链重组完成，mempool / UTXO / txindex 已全部同步")
}

// --------------------
// 查询接口
// --------------------

func (n *Node) Start() {

	fmt.Println("🚀 Node starting...")

	// -----------------------------------------
	// 1️⃣ 读取 best（检查 DB 是否存在区块）
	// -----------------------------------------
	bestHashBytes := n.DB.Get("meta", "best")
	if bestHashBytes == nil {
		fmt.Println("📦 No existing blockchain found. Creating genesis...")
		n.initGenesis()
		return
	}
	bestHash := string(bestHashBytes)

	// -----------------------------------------
	// 2️⃣ 从 index bucket 加载所有 BlockIndex（轻量结构）
	// -----------------------------------------
	indexes := make(map[string]*BlockIndex)

	n.DB.Iterate("index", func(k, v []byte) {
		var bi BlockIndex
		json.Unmarshal(v, &bi)
		indexes[bi.Hash] = &bi
	})

	if len(indexes) == 0 {
		fmt.Println("⚠️ No index found but best hash exists. Database corrupted?")
		return
	}

	for _, bi := range indexes {
		bi.CumWorkInt = new(big.Int)
		if bi.CumWork != "" {
			bi.CumWorkInt.SetString(bi.CumWork, 10)
		} else {
			bi.CumWorkInt.SetInt64(0)
		}
	}

	// -----------------------------------------
	// 3️⃣ 为每个 BlockIndex 加载 Block 本体
	// -----------------------------------------
	for _, bi := range indexes {
		raw := n.DB.Get("blocks", bi.Hash)
		if raw == nil {
			log.Println("❌ block missing in DB:", bi.Hash)
			continue
		}

		blk, err := blockchain.DeserializeBlock(raw)
		if err != nil {
			log.Println("❌ failed to decode block:", bi.Hash)
			continue
		}

		bi.Block = blk
	}

	// -----------------------------------------
	// 4️⃣ 重建 Parent / Children 指针（基于 PrevHash）
	// -----------------------------------------
	for _, bi := range indexes {
		if bi.PrevHash != "" {
			parent := indexes[bi.PrevHash]
			bi.Parent = parent
			parent.Children =
				append(parent.Children, bi)
		}
	}

	// -----------------------------------------
	// 5️⃣ 确定 best index（previous tip）
	// -----------------------------------------
	bestIndex := indexes[bestHash]
	n.Best = bestIndex
	n.Blocks = indexes

	// -----------------------------------------
	// 6️⃣ 重建链：从 best 回溯到 genesis
	// -----------------------------------------
	var chain []*blockchain.Block
	cur := bestIndex

	for cur != nil {
		chain = append([]*blockchain.Block{cur.Block}, chain...)
		cur = cur.Parent
	}

	n.Chain = chain

	fmt.Printf("🏗  Loaded %d blocks from DB. Best height = %d\n",
		len(chain), bestIndex.Height)

	// -----------------------------------------
	// 7️⃣ 重建 UTXO
	// -----------------------------------------
	n.UTXO = blockchain.NewUTXOSet(n.DB)
	n.DB.Iterate("utxo", func(k, v []byte) {
		var u blockchain.UTXO
		json.Unmarshal(v, &u)
		n.UTXO.Set[string(k)] = u
	})

	fmt.Printf("💰 Loaded %d UTXOs\n", len(n.UTXO.Set))

	// -----------------------------------------
	// 8️⃣ 重建 mempool（空）
	// -----------------------------------------
	n.Mempool = mempool.NewMempool(1000, n.DB)
	n.loadMempool()
	n.IsSyncing = true

	// 初始化同步子状态
	n.HeadersSynced = false
	n.BodiesSynced = false

	// 根据高度打印不同的提示，方便你调试本机和 VM
	if n.Best == nil || n.Best.Height == 0 {
		n.SyncState = SyncIBD // 初始区块下载模式
		fmt.Println("🆕 Fresh node, starting IBD...")
	} else {
		n.SyncState = SyncHeaders // 增量同步模式
		fmt.Printf("📥 Resuming sync from height %d...\n", n.Best.Height)
	}

	fmt.Println("✅ Node is ready and searching for peers...")
}

func (n *Node) initGenesis() {
	genesis := blockchain.NewGenesisBlock(n.Target)

	// 计算工作量
	work := computeWork(genesis.Target)

	// --- 转 hex ---
	hashHex := hex.EncodeToString(genesis.Hash)

	// 🔴 核心修改：确保 bi 结构体包含了 Block 本体
	bi := &BlockIndex{
		Block:      genesis, // 挂载本体
		Hash:       hashHex,
		Height:     0,
		CumWork:    work.String(),
		CumWorkInt: work,
		Parent:     nil,
		Children:   []*BlockIndex{}, // 养成初始化切片的好习惯
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
	fmt.Println("GENESIS TARGET =", utils.FormatTargetHex(genesis.Target))
}

func (n *Node) GetChain() []*blockchain.Block {
	return n.Chain
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
	fmt.Println("🔄 FastSync: Rebuilding full UTXO set...")

	// 1) 清空 UTXO
	utxo := blockchain.NewUTXOSet(n.DB)
	utxo.Set = make(map[string]blockchain.UTXO)
	utxo.AddrIndex = make(map[string][]string)

	if utxo.DB != nil {
		err := utxo.DB.ClearBucket("utxo")
		if err != nil {
			return err
		}
	}

	// 2) 按顺序遍历链上的每个区块
	for _, block := range n.Chain {
		if block == nil {
			continue
		}

		for _, tx := range block.Transactions {
			// 非 coinbase 花费输入
			if !tx.IsCoinbase {
				utxo.Spend(tx)
			}
			// 添加输出
			utxo.Add(tx)
		}
	}

	// 3) 替换旧 UTXO
	n.UTXO = utxo

	fmt.Println("✅ FastSync: UTXO rebuild complete.")
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
	phHex := hex.EncodeToString(blk.PrevHash)
	n.Orphans[phHex] = append(n.Orphans[phHex], blk)
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

	// 读 block
	blockBytes := n.DB.Get("blocks", idx.BlockHash)
	if blockBytes == nil {
		return nil, nil, fmt.Errorf("block not found")
	}

	block, err := blockchain.DeserializeBlock(blockBytes)
	if err != nil {
		return nil, nil, err
	}

	// 安全检查
	if idx.TxOffset < 0 || idx.TxOffset >= len(block.Transactions) {
		return nil, nil, fmt.Errorf("invalid TxOffset in txindex")
	}

	tx := &block.Transactions[idx.TxOffset]

	return tx, block, nil
}

func (n *Node) loadMempool() {
	count := 0

	n.DB.Iterate("mempool", func(k, v []byte) {
		txid := string(k)

		// 放入内存 mempool
		n.Mempool.Txs[txid] = v

		// ⭐ 重建 parent 依赖信息（你的逻辑）
		tx, err := blockchain.DeserializeTransaction(v)
		if err == nil {
			for _, in := range tx.Inputs {
				parent := in.TxID
				n.Mempool.Parents[txid] =
					append(n.Mempool.Parents[txid], parent)
			}
		}

		count++
	})

	log.Printf("💾 Loaded %d mempool transactions from DB\n", count)
}

func (n *Node) BroadcastBlockHash(hashHex string) {
	if n.Broadcaster != nil {
		n.Broadcaster.BroadcastNewBlock(hashHex)
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
	genesisHash := hex.EncodeToString(n.Chain[0].Hash)
	return n.Blocks[genesisHash]
}

func (n *Node) IsSynced() bool {
	return n.SyncState == SyncSynced
}

func (n *Node) updateUTXO(block *blockchain.Block) {
	for _, tx := range block.Transactions {
		// 1. 移除已花費的輸出 (Inputs)
		if !tx.IsCoinbase {
			n.UTXO.Spend(tx)
		}

		// 2. 添加新產生的輸出 (Outputs)
		n.UTXO.Add(tx)
	}
}

func (n *Node) addTxsToMempool(txs []blockchain.Transaction) {
	for _, tx := range txs {
		// Coinbase 交易無法復活 (因為它們只在特定高度有效，且憑空產生)
		if !tx.IsCoinbase {
			// 使用 AddTxRBF 嘗試加入，如果 Mempool 滿了或有衝突會自動處理
			n.Mempool.AddTxRBF(tx.Hash(), tx.Serialize(), n.UTXO)
		}
	}
}

func (n *Node) IsOnMainChain(bi *BlockIndex) bool {
	// 1. 高度超过主链长度，肯定不是
	if bi.Height >= uint64(len(n.Chain)) {
		return false
	}

	// 2. 取出主链该高度的区块
	mainBlock := n.Chain[bi.Height]
	mainHashHex := hex.EncodeToString(mainBlock.Hash)

	// 3. 比较 Hash 是否一致
	// 如果高度相同但 Hash 不同，说明 bi 是侧链区块
	return mainHashHex == bi.Hash
}

func (n *Node) GetResetChan() chan bool {
	// 確保不會返回 nil (如果初始化忘了 make)
	if n.MinerResetChan == nil {
		n.MinerResetChan = make(chan bool, 1)
	}
	return n.MinerResetChan
}

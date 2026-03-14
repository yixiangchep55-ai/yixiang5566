package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"

	"mycoin/utils"
)

// connectBlock attaches a fully downloaded block to the block index and, when
// appropriate, to the active chain.
func (n *Node) connectBlock(block *blockchain.Block, parent *BlockIndex) bool {
	if parent == nil {
		log.Panic("critical: connectBlock received nil parent")
		return false
	}
	if parent.Block == nil {
		log.Panic("critical: connectBlock received parent without body")
		return false
	}

	// 1. Difficulty checks.
	if (parent.Height+1)%blockchain.DifficultyInterval == 0 {
		expectedTarget := n.retargetDifficulty(parent)
		expectedBits := utils.BigToCompact(expectedTarget)
		if expectedBits != block.Bits {
			fmt.Printf("❌ [Consensus] 難度驗證失敗 (Retarget)，預期 %d，收到 %d\n", expectedBits, block.Bits)
			return false
		}
	} else if block.Bits != parent.Bits {
		fmt.Printf("❌ [Consensus] 難度驗證失敗 (Fixed)，預期 %d，收到 %d\n", parent.Bits, block.Bits)
		return false
	}

	if err := n.validateBlockTimestamp(block, parent); err != nil {
		log.Println("block timestamp validation failed:", err)
		return false
	}

	work := computeWork(block.Target)
	cumWork := new(big.Int).Add(parent.CumWorkInt, work)

	if err := block.Verify(parent.Block); err != nil {
		log.Println("block basic validation failed:", err)
		return false
	}

	isMainChainExtension := parent == n.Best

	// Only validate with the current UTXO set when the block extends the active
	// tip. Using the active-chain UTXO on a competing branch would wrongly mark
	// valid fork transactions as missing or double-spent.
	if n.SyncState == SyncSynced && isMainChainExtension {
		if err := VerifyBlockWithUTXO(block, parent.Block, n.UTXO); err != nil {
			log.Println("❌ Block validation failed:", err)
			return false
		}
	}

	// 2. Build or update the block index entry.
	hashHex := hex.EncodeToString(block.Hash)
	bi, exists := n.Blocks[hashHex]
	if exists {
		bi.Block = block
		bi.Bits = block.Bits
		bi.Timestamp = block.Timestamp
		bi.Nonce = block.Nonce
		bi.MerkleRoot = hex.EncodeToString(block.MerkleRoot)
		bi.Parent = parent
		bi.CumWorkInt = cumWork
		bi.CumWork = cumWork.Text(16)
	} else {
		bi = &BlockIndex{
			Hash:       hashHex,
			PrevHash:   parent.Hash,
			Height:     parent.Height + 1,
			Timestamp:  block.Timestamp,
			Bits:       block.Bits,
			Nonce:      block.Nonce,
			MerkleRoot: hex.EncodeToString(block.MerkleRoot),
			CumWork:    cumWork.Text(16),
			CumWorkInt: cumWork,
			Block:      block,
			Parent:     parent,
			Children:   []*BlockIndex{},
		}
		n.Blocks[hashHex] = bi
	}

	alreadyChild := false
	for _, child := range parent.Children {
		if child.Hash == hashHex {
			alreadyChild = true
			break
		}
	}
	if !alreadyChild {
		parent.Children = append(parent.Children, bi)
	}

	// 3. Persist block body and index.
	n.DB.Put("blocks", hashHex, block.Serialize())
	idxBytes, _ := json.Marshal(bi)
	n.DB.Put("index", hashHex, idxBytes)

	if bi.Height >= n.Best.Height {
		fmt.Printf("⚖️ [Chain Selection] Local Best: %d (Work: %s) vs New Block: %d (Work: %s)\n",
			n.Best.Height,
			n.Best.CumWorkInt.Text(16),
			bi.Height,
			bi.CumWorkInt.Text(16),
		)
	}

	// During fast sync we only scaffold blocks and defer chain activation.
	if n.SyncState != SyncSynced {
		return true
	}

	chainSwitched := false

	if isMainChainExtension {
		n.Best = bi
		n.Chain = append(n.Chain, block)
		n.updateUTXO(block)

		log.Printf("⛏️ Main chain extended to height: %d (Hash: %s)\n", bi.Height, hashHex)
		chainSwitched = true
	} else if bi.CumWorkInt.Cmp(n.Best.CumWorkInt) > 0 {
		log.Printf("📣 REORG DETECTED! Current Best: %d, New Best: %d\n", n.Best.Height, bi.Height)

		oldChain, newChain := n.reorgTo(bi)
		if err := n.validateReorgCandidate(oldChain, newChain); err != nil {
			log.Printf("❌ reorg candidate validation failed at height %d: %v\n", bi.Height, err)
			return false
		}

		n.rebuildChain(oldChain, newChain, bi)
		fmt.Println("📧 執行核心緊急星鏈重組 (Full UTXO Rebuild)...")
		go n.RebuildUTXO()
		chainSwitched = true
	}

	if chainSwitched {
		n.DB.Put("meta", "best", []byte(n.Best.Hash))
		n.UTXO.FlushToDB()

		txCount := 0
		for _, tx := range block.Transactions {
			if !tx.IsCoinbase {
				n.Mempool.Remove(tx.ID)
				txCount++
			}
		}
		fmt.Printf("🧹 [Mempool] 已清理區塊 %d 中的 %d 筆交易\n", block.Height, txCount)

		select {
		case n.MinerResetChan <- true:
			fmt.Println("⚡ [Consensus] 鏈頭更新，已通知礦工重新計算")
		default:
		}
	}

	return true
}

// validateReorgCandidate checks a heavier side branch against the UTXO state at
// the fork point before we commit to a reorg.
func (n *Node) validateReorgCandidate(oldChain, newChain []*BlockIndex) error {
	if len(newChain) == 0 {
		return nil
	}

	tmp := n.UTXO.Clone()

	for i := len(oldChain) - 1; i >= 0; i-- {
		oldBI := oldChain[i]
		if oldBI == nil || oldBI.Block == nil {
			return fmt.Errorf("missing block body in old chain during rollback")
		}
		for _, tx := range oldBI.Block.Transactions {
			tmp.Revert(tx)
		}
	}

	for _, newBI := range newChain {
		if newBI == nil || newBI.Block == nil {
			return fmt.Errorf("missing block body in candidate chain")
		}
		if newBI.Parent == nil || newBI.Parent.Block == nil {
			return fmt.Errorf("missing parent body for candidate block %d", newBI.Height)
		}

		if err := VerifyBlockWithUTXO(newBI.Block, newBI.Parent.Block, tmp); err != nil {
			return fmt.Errorf("chain validation failed at height %d: %w", newBI.Height, err)
		}

		for _, tx := range newBI.Block.Transactions {
			if !tx.IsCoinbase {
				if err := tmp.Spend(tx); err != nil {
					return fmt.Errorf("failed to apply block %d: %w", newBI.Height, err)
				}
			}
			tmp.Add(tx)
		}
	}

	return nil
}

// ActivateBestChainFromPrunedSync finalizes sync for a pruned node by
// validating and applying only the retained branch delta on top of the current
// persisted UTXO state.
func (n *Node) ActivateBestChainFromPrunedSync(newTip *BlockIndex) error {
	if newTip == nil {
		return fmt.Errorf("nil best tip")
	}

	oldChain, newChain := n.reorgTo(newTip)
	if err := n.validateReorgCandidate(oldChain, newChain); err != nil {
		return err
	}

	n.rebuildChain(oldChain, newChain, newTip)
	return nil
}

func (n *Node) attachOrphans(parentHash string) {
	n.mu.Lock()
	orphans := n.Orphans[parentHash]
	if len(orphans) == 0 {
		n.mu.Unlock()
		return
	}
	delete(n.Orphans, parentHash)
	n.mu.Unlock()

	for _, blk := range orphans {
		n.AddBlock(blk)
	}
}

// reorgTo returns the blocks to detach from the current best chain and the
// blocks to attach from the candidate chain.
func (n *Node) reorgTo(newTip *BlockIndex) (oldChain []*BlockIndex, newChain []*BlockIndex) {
	oldTip := n.Best
	if oldTip == nil || newTip == nil {
		return nil, nil
	}

	a := oldTip
	b := newTip

	for a.Height > b.Height {
		a = a.Parent
		if a == nil {
			return nil, nil
		}
	}

	for b.Height > a.Height {
		b = b.Parent
		if b == nil {
			return nil, nil
		}
	}

	for a != nil && b != nil && a != b {
		a = a.Parent
		b = b.Parent
	}

	if a == nil || b == nil {
		return nil, nil
	}

	commonAncestor := a

	cur := oldTip
	for cur != nil && cur != commonAncestor {
		oldChain = append(oldChain, cur)
		cur = cur.Parent
	}

	var tmp []*BlockIndex
	cur = newTip
	for cur != nil && cur != commonAncestor {
		tmp = append(tmp, cur)
		cur = cur.Parent
	}

	for i := len(tmp) - 1; i >= 0; i-- {
		newChain = append(newChain, tmp[i])
	}

	return oldChain, newChain
}

func (n *Node) indexTransactions(block *blockchain.Block, bi *BlockIndex) {
	blockHashHex := hex.EncodeToString(block.Hash)

	for i, tx := range block.Transactions {
		idx := blockchain.TxIndexEntry{
			BlockHash: blockHashHex,
			Height:    bi.Height,
			TxOffset:  i,
			Pruned:    false,
		}

		n.putTxIndexEntry(tx.ID, idx)
	}
}

func (n *Node) putTxIndexEntry(txid string, idx blockchain.TxIndexEntry) {
	data, _ := json.Marshal(idx)
	n.DB.Put("txindex", txid, data)
}

func (n *Node) markBlockTransactionsPruned(blockHash string, block *blockchain.Block, pruned bool) {
	if block == nil {
		return
	}

	for i, tx := range block.Transactions {
		n.putTxIndexEntry(tx.ID, blockchain.TxIndexEntry{
			BlockHash: blockHash,
			Height:    block.Height,
			TxOffset:  i,
			Pruned:    pruned,
		})
	}
}

func (n *Node) removeTxIndex(block *blockchain.Block) {
	for _, tx := range block.Transactions {
		n.DB.Delete("txindex", tx.ID)
	}
}

func (n *Node) removeConfirmedTxs(block *blockchain.Block) {
	for _, tx := range block.Transactions {
		if !tx.IsCoinbase {
			n.DB.Delete("mempool", tx.ID)
			n.Mempool.Remove(tx.ID)
		}
	}
}

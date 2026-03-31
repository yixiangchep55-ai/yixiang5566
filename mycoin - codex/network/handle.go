package network

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"mycoin/blockchain"
	"mycoin/indexer"
	"mycoin/node"
	"os"
	"strings"
	"sync"
	"time"
)

type Handler struct {
	Node              *node.Node
	Network           *Network
	LocalVersion      VersionPayload
	debugBlockTraffic bool
	debugP2PTraffic   bool
	requestMu         sync.Mutex
	requestedBlocks   map[string]time.Time
	requestedTxs      map[string]time.Time
	deferredTxMu      sync.Mutex
	deferredLocalTxs  map[string]struct{}
}

func NewHandler(n *node.Node) *Handler {
	return &Handler{
		Node:              n,
		debugBlockTraffic: envBool("MYCOIN_DEBUG_BLOCKS"),
		debugP2PTraffic:   envBool("MYCOIN_DEBUG_P2P"),
		requestedBlocks:   make(map[string]time.Time),
		requestedTxs:      make(map[string]time.Time),
		deferredLocalTxs:  make(map[string]struct{}),
	}
}

func (h *Handler) strongestActivePeer() (*Peer, uint64, *big.Int, string) {
	if h == nil || h.Network == nil {
		return nil, 0, nil, ""
	}

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	var bestPeer *Peer
	var bestHeight uint64
	var bestWork *big.Int
	var bestAddr string

	for _, peer := range h.Network.Peers {
		if peer == nil || !peer.IsActive() {
			continue
		}

		peerWork := new(big.Int)
		if _, ok := peerWork.SetString(strings.TrimSpace(peer.CumWork), 16); !ok {
			continue
		}

		if bestWork == nil || peerWork.Cmp(bestWork) > 0 || (peerWork.Cmp(bestWork) == 0 && peer.Height > bestHeight) {
			bestPeer = peer
			bestHeight = peer.Height
			bestWork = new(big.Int).Set(peerWork)
			bestAddr = peer.Addr
		}
	}

	return bestPeer, bestHeight, bestWork, bestAddr
}

func (h *Handler) StrongestPeerSyncTarget() (uint64, *big.Int, string) {
	_, height, work, addr := h.strongestActivePeer()
	return height, work, addr
}

func (h *Handler) pruneClosedPeersLocked() {
	for nodeID, p := range h.Network.Peers {
		if p == nil || p.IsClosed() {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func (h *Handler) OnMessage(peer *Peer, msg *Message) {
	if h.debugBlockTraffic && msg.Type == MsgBlock {
		fmt.Printf("[Debug] TCP Received MsgBlock from %s (Data: %v)\n", peer.Addr, msg.Data)
	}
	switch msg.Type {

	case MsgVersion:
		h.handleVersion(peer, msg)

	case MsgVerAck:
		h.handleVerAck(peer, msg)

	case MsgTx:
		h.handleTx(peer, msg)

	case MsgInv:
		h.handleInv(peer, msg)

	case MsgGetData:
		h.handleGetData(peer, msg)

	case MsgBlock:
		h.handleBlock(peer, msg)

	case MsgGetAddr:
		h.handleGetAddr(peer, msg)

	case MsgAddr:
		h.handleAddr(peer, msg)

	case MsgGetHeaders:
		h.handleGetHeaders(peer, msg)

	case MsgHeaders:
		h.handleHeaders(peer, msg)

	case "mempool":
		h.handleMempool(peer, msg)

	default:
		log.Println("unknown msg:", msg.Type)
	}
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func shouldPreferNewPeer(localNodeID uint64, existingPeer, newPeer *Peer) bool {
	if existingPeer == nil {
		return true
	}
	if newPeer == nil {
		return false
	}

	preferredOutbound := localNodeID < newPeer.NodeID
	existingMatches := existingPeer.Outbound == preferredOutbound
	newMatches := newPeer.Outbound == preferredOutbound

	if existingMatches != newMatches {
		return newMatches
	}

	if existingPeer.Outbound != newPeer.Outbound {
		return newPeer.Outbound == preferredOutbound
	}

	if existingPeer.Addr == newPeer.Addr {
		return false
	}

	return newPeer.Addr < existingPeer.Addr
}

func (h *Handler) reserveBlockRequest(hash string) bool {
	if hash == "" {
		return false
	}

	h.requestMu.Lock()
	defer h.requestMu.Unlock()

	now := time.Now()
	if requestedAt, exists := h.requestedBlocks[hash]; exists && now.Sub(requestedAt) < 5*time.Second {
		return false
	}

	h.requestedBlocks[hash] = now
	return true
}

func (h *Handler) releaseBlockRequest(hash string) {
	if hash == "" {
		return
	}

	h.requestMu.Lock()
	delete(h.requestedBlocks, hash)
	h.requestMu.Unlock()
}

func (h *Handler) reserveTxRequest(txid string) bool {
	if txid == "" {
		return false
	}

	h.requestMu.Lock()
	defer h.requestMu.Unlock()

	now := time.Now()
	if requestedAt, exists := h.requestedTxs[txid]; exists && now.Sub(requestedAt) < 5*time.Second {
		return false
	}

	h.requestedTxs[txid] = now
	return true
}

func (h *Handler) releaseTxRequest(txid string) {
	if txid == "" {
		return
	}

	h.requestMu.Lock()
	delete(h.requestedTxs, txid)
	h.requestMu.Unlock()
}

func (h *Handler) handleVersion(peer *Peer, msg *Message) {
	var v VersionPayload
	if err := decode(msg.Data, &v); err != nil {
		log.Println("decode version error:", err)
		return
	}

	peer.NodeID = v.NodeID
	if h.Network != nil && h.Network.PeerManager != nil {
		h.Network.PeerManager.SetPeerAdvertiseAddr(peer, v.AdvertiseAddr)
		if !h.Network.PeerManager.ResolveAdvertiseConflict(peer) {
			return
		}
	}

	if peer.State == StateInit {
		height := uint64(0)
		cumWork := "0"
		if h.Node != nil && h.Node.Best != nil {
			height = h.Node.Best.Height
			cumWork = h.Node.Best.CumWork
		}
		peer.Send(Message{
			Type: MsgVersion,
			Data: VersionPayload{
				Version:       1,
				Height:        height,
				CumWork:       cumWork,
				NodeID:        h.Node.NodeID,
				Mode:          h.Node.Mode,
				AdvertiseAddr: h.LocalVersion.AdvertiseAddr,
			},
		})
		peer.State = StateVersionSent
	}

	peer.Height = v.Height
	peer.CumWork = v.CumWork
	peer.State = StateVersionRecv
	peer.NodeID = v.NodeID
	peer.Mode = node.NormalizeMode(v.Mode)

	peerWork := new(big.Int)
	shouldEvaluateSync := peer.ShouldEvaluateVersion(v.Height, v.CumWork)

	if _, ok := peerWork.SetString(v.CumWork, 16); !ok {
		peerWork.SetInt64(0)
	}

	if shouldEvaluateSync {
		h.Node.EvaluateSyncStatus(v.Height, peerWork)
	}

	peer.Send(Message{Type: MsgVerAck})
}

func (h *Handler) handleVerAck(peer *Peer, msg *Message) {
	if peer.State >= StateVersionRecv {

		h.Network.mu.Lock()
		var peerToClose *Peer

		if peer.NodeID == h.Network.Node.NodeID {
			fmt.Println("[Security] detected a self-connection by NodeID; rejecting duplicate local peer")
			h.Network.mu.Unlock()
			peer.CloseWithReason("self connection rejected")
			return
		}

		if existingPeer, exists := h.Network.Peers[peer.NodeID]; exists {

			if existingPeer == peer {
				h.Network.mu.Unlock()
				return
			}

			fmt.Printf("[Network] Rejecting duplicate connection from NodeID: %d, preferring existing peer at %s\n", peer.NodeID, existingPeer.Addr)
			if shouldPreferNewPeer(h.Network.Node.NodeID, existingPeer, peer) {
				delete(h.Network.Peers, peer.NodeID)
				peerToClose = existingPeer
			} else {
				h.Network.mu.Unlock()
				peer.CloseWithReason(fmt.Sprintf("duplicate node id %d: preferred %s", peer.NodeID, existingPeer.Addr))
				return
			}
		}

		peer.State = StateActive
		log.Printf("[Network] Peer active: %s (NodeID: %d)\n", peer.Addr, peer.NodeID)

		h.Network.Peers[peer.NodeID] = peer
		currentCount := len(h.Network.Peers)
		h.Network.RecordPeerActive(peer.Addr, currentCount)

		h.Network.mu.Unlock()
		if h.Network.PeerManager != nil {
			h.Network.PeerManager.RegisterPeerAdvertiseAddr(peer)
		}

		fmt.Printf("[Network] Registered new peer NodeID %d. Total active peers: %d\n", peer.NodeID, currentCount)

		if peerToClose != nil {
			peerToClose.CloseWithReason(fmt.Sprintf("duplicate node id %d: preferred %s", peer.NodeID, peer.Addr))
		}
		peer.Send(Message{Type: MsgGetAddr})

		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{
				Locators: h.buildBlockLocator(),
			},
		})
	}
}

func (h *Handler) handleInv(peer *Peer, msg *Message) {

	if h.debugP2PTraffic {
		fmt.Printf("[Kali-Debug] Received Inv message from %s\n", peer.Addr)
	}

	var inv InvPayload
	if err := decode(msg.Data, &inv); err != nil {

		if h.debugP2PTraffic {
			fmt.Printf("[Kali-Debug] Failed to decode InvPayload: %v\n", err)
		}

		if h.debugP2PTraffic {
			fmt.Printf("[Kali-Debug] Raw msg.Data: %+v\n", msg.Data)
		}
		return
	}

	if h.debugP2PTraffic {
		fmt.Printf("[Kali-Debug] received Inv message with %d hashes of type %s\\n", len(inv.Hashes), inv.Type)
	}

	switch inv.Type {
	case "block":
		for _, hashHex := range inv.Hashes {
			hashBytes, err := hex.DecodeString(hashHex)
			if err != nil {
				continue
			}
			if !h.Node.HasBlock(hashBytes) {
				h.requestBlock(peer, hashHex)
			}
		}

	case "tx":

		if h.Node.IsSyncing || h.Node.HasMissingBodies() {

			return
		}

		for _, txid := range inv.Hashes {
			if h.Node.Mempool.Has(txid) || !h.reserveTxRequest(txid) {
				continue
			}

			fmt.Printf("[P2P] Requesting tx %s via GetData...\n", txid[:8])
			if !peer.Send(Message{
				Type: MsgGetData,
				Data: GetDataPayload{
					Type: "tx",
					Hash: txid,
				},
			}) {
				h.releaseTxRequest(txid)
			}
		}
	}
}

func (h *Handler) handleGetData(peer *Peer, msg *Message) {
	var req GetDataPayload
	if err := decode(msg.Data, &req); err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("[Windows-Debug] Failed to decode GetDataPayload: %v\n", err)
		}
		return
	}

	switch req.Type {
	case "block":

		bi := h.Node.Blocks[req.Hash]
		if bi == nil || bi.Block == nil {
			fmt.Printf("[P2P] peer %s requested block %s but the full block body is not available locally\\n", peer.Addr, req.Hash[:8])
			return
		}

		dto := BlockToDTO(bi.Block, bi)
		peer.Send(Message{
			Type: MsgBlock,
			Data: dto,
		})

	case "tx":

		if h.debugP2PTraffic {
			fmt.Printf("[Windows-Debug] received GetData for tx %s from %s\n", req.Hash[:8], peer.Addr)
		}
		tx, ok := h.Node.Mempool.Get(req.Hash)
		if !ok {
			if h.debugP2PTraffic {
				fmt.Printf("[Windows-Debug] requested tx %s was not found in the mempool\n", req.Hash[:8])
			}
			return
		}

		if h.debugP2PTraffic {
			fmt.Printf("[P2P] sending MsgTx for %s to %s\n", req.Hash[:8], peer.Addr)
		}
		peer.Send(Message{
			Type: MsgTx,
			Data: TxPayload{Tx: tx},
		})
	}
}

// ======================
// block
// ======================

func (h *Handler) handleBlock(peer *Peer, msg *Message) {
	var dto BlockDTO
	if err := decode(msg.Data, &dto); err != nil {
		log.Printf("[Network] block decode error from %s: %v", peer.Addr, err)

		return
	}

	blk := DTOToBlock(dto)
	calcHash := blk.CalcHash()
	if len(blk.Hash) > 0 && !bytes.Equal(blk.Hash, calcHash) {
		log.Printf("[Security] rejected block from %s: claimed hash does not match recomputed hash", peer.Addr)
		return
	}
	blk.Hash = calcHash
	hashHex := hex.EncodeToString(calcHash)
	prevHex := hex.EncodeToString(blk.PrevHash)
	h.releaseBlockRequest(hashHex)

	h.Node.Lock()
	bi := h.Node.Blocks[hashHex]
	alreadyHasBody := (bi != nil && bi.Block != nil)
	shouldCheckMissing := alreadyHasBody &&
		h.Node.IsSyncing &&
		bi != nil &&
		bi.CumWorkInt != nil &&
		h.Node.Best != nil &&
		h.Node.Best.CumWorkInt != nil &&
		bi.CumWorkInt.Cmp(h.Node.Best.CumWorkInt) > 0
	h.Node.Unlock()

	if alreadyHasBody {

		if shouldCheckMissing {
			fmt.Printf("[Sync] already have block body for height %d; checking for missing bodies\n", blk.Height)
			h.requestMissingBlockBodies(peer)
		}

		return
	}

	fmt.Printf("[Network] received block height %d, hash %s\n", blk.Height, hashHex)

	h.Node.Lock()
	localBodyHeight := uint64(0)
	chainStartHeight := uint64(0)
	if chainLen := len(h.Node.Chain); chainLen > 0 {
		if h.Node.Chain[0] != nil {
			chainStartHeight = h.Node.Chain[0].Height
		}
		localBodyHeight = h.Node.Chain[chainLen-1].Height
	}
	mainAtHeightHash := ""
	if blk.Height >= chainStartHeight {
		idx := int(blk.Height - chainStartHeight)
		if idx >= 0 && idx < len(h.Node.Chain) {
			if mainBlock := h.Node.Chain[idx]; mainBlock != nil {
				mainAtHeightHash = hex.EncodeToString(mainBlock.Hash)
			}
		}
	}
	parent := h.Node.Blocks[prevHex]
	if parent == nil {
		if bi != nil && bi.Block == nil {
			delete(h.Node.Blocks, hashHex)
			bi = nil
		}
		h.Node.Unlock()
		fmt.Printf("[Sync] missing parent header %s; storing block as orphan and requesting more headers\\n", prevHex)
		h.Node.AddOrphan(blk)
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}
	parentHasBody := parent.Block != nil
	expectedHeight := parent.Height + 1
	if blk.Height != expectedHeight {
		if bi != nil && bi.Block == nil {
			if bi.Parent != nil {
				filtered := bi.Parent.Children[:0]
				for _, child := range bi.Parent.Children {
					if child != nil && child.Hash != bi.Hash {
						filtered = append(filtered, child)
					}
				}
				bi.Parent.Children = filtered
			}
			delete(h.Node.Blocks, hashHex)
		}
		h.Node.Unlock()
		log.Printf("[Security] rejected block %s from %s: invalid height %d (expected %d)", hashHex, peer.Addr, blk.Height, expectedHeight)
		return
	}

	work := node.WorkFromTarget(blk.Target)
	if bi == nil {
		bi = &node.BlockIndex{
			Hash:       hashHex,
			PrevHash:   prevHex,
			Height:     expectedHeight,
			Timestamp:  blk.Timestamp,
			Bits:       blk.Bits,
			Nonce:      blk.Nonce,
			MerkleRoot: hex.EncodeToString(blk.MerkleRoot),
		}
		if parent.CumWorkInt != nil {
			bi.CumWorkInt = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
		} else {
			bi.CumWorkInt = new(big.Int).Set(work)
		}
		bi.CumWork = bi.CumWorkInt.Text(16)
		bi.Parent = parent
		existsChild := false
		for _, child := range parent.Children {
			if child != nil && child.Hash == bi.Hash {
				existsChild = true
				break
			}
		}
		if !existsChild {
			parent.Children = append(parent.Children, bi)
		}
		h.Node.Blocks[hashHex] = bi
	} else if bi.Parent == nil {
		bi.Parent = parent
		if parent.CumWorkInt != nil {
			bi.CumWorkInt = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
			bi.CumWork = bi.CumWorkInt.Text(16)
		} else if bi.CumWorkInt == nil {
			bi.CumWorkInt = new(big.Int).Set(work)
			bi.CumWork = bi.CumWorkInt.Text(16)
		}
		existsChild := false
		for _, child := range parent.Children {
			if child != nil && child.Hash == bi.Hash {
				existsChild = true
				break
			}
		}
		if !existsChild {
			parent.Children = append(parent.Children, bi)
		}
	}
	if bi != nil {
		bi.Height = expectedHeight
	}
	blockHasBody := bi != nil && bi.Block != nil
	h.Node.Unlock()
	if !blockHasBody && blk.Height < localBodyHeight && mainAtHeightHash == hashHex {
		if err := h.Node.AttachHistoricalBlock(blk); err != nil {
			fmt.Printf("[Prune] failed to attach historical block %d (%s): %v\n", blk.Height, hashHex, err)
			return
		}
		fmt.Printf("[Prune] successfully attached historical block %d (%s)\n", blk.Height, hashHex[:8])
		return
	}

	if !parentHasBody {
		h.requestBlock(peer, prevHex)
		log.Printf("[Sync] parent %s only has a header, storing block %d as orphan", prevHex, blk.Height)
		h.Node.AddOrphan(blk)
		return
	}

	success := h.Node.AddBlock(blk)
	if !success {
		h.Node.Lock()
		if current := h.Node.Blocks[hashHex]; current != nil && current.Block == nil {
			if current.Parent != nil {
				filtered := current.Parent.Children[:0]
				for _, child := range current.Parent.Children {
					if child != nil && child.Hash != current.Hash {
						filtered = append(filtered, child)
					}
				}
				current.Parent.Children = filtered
			}
			delete(h.Node.Blocks, hashHex)
		}
		h.Node.Unlock()

		log.Printf("[Security] rejected block %d (%s): validation failed", blk.Height, hashHex)
		return
	}

	var (
		isSyncing        bool
		bestHash         string
		hasMissingBodies bool
	)
	h.Node.Lock()
	isSyncing = h.Node.IsSyncing
	bestHash = ""
	if h.Node.Best != nil {
		bestHash = h.Node.Best.Hash
	}
	hasMissingBodies = h.Node.HasMissingBodiesLocked()
	h.Node.Unlock()

	if isSyncing {

		isMainChainTip := false
		isMainChainTip = (hashHex == bestHash)

		if isMainChainTip {
			fmt.Printf("[Sync] received the main-chain tip body for block %d; attempting final sync\\n", blk.Height)

			if h.finishSyncing() {
				fmt.Printf("[Network] sync completed after receiving block %d; requesting mempool\\n", blk.Height)

				h.requestMempool(peer)
			}
		} else {

			h.requestMissingBlockBodies(peer)
		}
	} else {

		h.requestMempool(peer)
	}

	if h.Node.StatusSnapshot().Synced {
		h.broadcastInvExcept(hashHex, peer)
	}

	if isSyncing && !hasMissingBodies {
		fmt.Println("[Sync] received the main-chain tip body; attempting to finish sync")
		if h.finishSyncing() {
			fmt.Println("[Network] sync finished after receiving the last block body; requesting mempool")
			h.requestMempool(peer)
		}
	}
}

func (h *Handler) requestMempool(peer *Peer) {
	if peer == nil {
		return
	}
	if h.Node.SyncState != node.SyncSynced || h.Node.IsSyncing || h.Node.HasMissingBodies() {
		return
	}

	fmt.Printf("[P2P] requesting mempool from %s\n", peer.Addr)
	peer.Send(Message{
		Type: "mempool",
		Data: nil,
	})
}

func (h *Handler) handleMempool(peer *Peer, msg *Message) {
	fmt.Printf("[P2P] peer %s requested the mempool inventory\n", peer.Addr)

	var txIDs []string

	for txid := range h.Node.Mempool.GetAll() {
		txIDs = append(txIDs, txid)
	}

	if len(txIDs) > 0 {
		fmt.Printf("[P2P] advertising %d mempool txs to %s\n", len(txIDs), peer.Addr)

		peer.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "tx",
				Hashes: txIDs,
			},
		})
	} else {
		fmt.Printf("[P2P] peer %s requested the mempool but there were no transactions to advertise\n", peer.Addr)
	}
}

func (h *Handler) finishSyncing() bool {
	fmt.Println("[Sync] checking for missing parents in the local database...")

	h.Node.Lock()

	fmt.Println("[Sync] resolving parent-child block links...")
	for {
		changed := false
		for _, bi := range h.Node.Blocks {
			if bi.Height > 0 && bi.Parent == nil {
				if p, exists := h.Node.Blocks[bi.PrevHash]; exists {
					bi.Parent = p
					changed = true
				} else {

					data := h.Node.DB.Get("blocks", bi.PrevHash)

					if len(data) > 0 {
						parentBlock, err := blockchain.DeserializeBlock(data)
						if err == nil {
							pIdx := &node.BlockIndex{
								Hash:      hex.EncodeToString(parentBlock.Hash),
								Height:    bi.Height - 1,
								Block:     parentBlock,
								PrevHash:  hex.EncodeToString(parentBlock.PrevHash),
								Bits:      parentBlock.Bits,
								Timestamp: parentBlock.Timestamp,
							}
							h.Node.Blocks[pIdx.Hash] = pIdx
							bi.Parent = pIdx
							changed = true
							fmt.Printf("[Sync] reconstructed missing parent body at height %d from the local database\n", pIdx.Height)
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	var actualBest *node.BlockIndex
	for _, bi := range h.Node.Blocks {
		if bi == nil || bi.Block == nil {
			continue
		}
		if actualBest == nil {
			actualBest = bi
			continue
		}

		candidateWork := bi.CumWorkInt
		if candidateWork == nil {
			candidateWork = big.NewInt(0)
		}
		bestWork := actualBest.CumWorkInt
		if bestWork == nil {
			bestWork = big.NewInt(0)
		}

		cmp := candidateWork.Cmp(bestWork)
		if cmp > 0 || (cmp == 0 && (bi.Height > actualBest.Height || (bi.Height == actualBest.Height && bi.Hash > actualBest.Hash))) {
			actualBest = bi
		}
	}
	if actualBest == nil {
		h.Node.Unlock()
		return false
	}

	strongestPeer, strongestPeerHeight, strongestPeerWork, strongestPeerAddr := h.strongestActivePeer()
	actualWork := big.NewInt(0)
	if actualBest.CumWorkInt != nil {
		actualWork = new(big.Int).Set(actualBest.CumWorkInt)
	}
	if strongestPeerWork != nil && actualWork.Cmp(strongestPeerWork) < 0 {
		fmt.Printf("[Sync] refusing to mark synced because peer %s advertises more work (local height %d, peer height %d)\n", strongestPeerAddr, actualBest.Height, strongestPeerHeight)
		h.Node.IsSyncing = true
		h.Node.SyncState = node.SyncHeaders
		h.Node.HeadersSynced = false
		h.Node.Unlock()
		if strongestPeer != nil {
			strongestPeer.Send(Message{
				Type: MsgGetHeaders,
				Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
			})
		}
		return false
	}

	oldBest := h.Node.Best
	oldChain := append([]*blockchain.Block(nil), h.Node.Chain...)
	targetBestHash := actualBest.Hash
	newMainChain := []*blockchain.Block{}
	cur := actualBest
	for cur != nil && cur.Block != nil {
		newMainChain = append([]*blockchain.Block{cur.Block}, newMainChain...)
		cur = cur.Parent
	}
	if len(newMainChain) > 0 && newMainChain[0].Height != 0 && h.Node.IsPrunedMode() {
		if err := h.Node.ActivateBestChainFromPrunedSync(actualBest); err != nil {
			fmt.Printf("[Sync] pruned chain activation failed: %v\n", err)
			h.Node.Unlock()
			return false
		}

		fmt.Printf("[Sync] pruned activation succeeded at height %d; rebuilding UTXO from the pruned main chain\n", newMainChain[0].Height)
		h.Node.Unlock()

		h.Node.Lock()
		if h.Node.Best == nil || h.Node.Best.Hash != targetBestHash {
			currentBestHash := ""
			if h.Node.Best != nil {
				currentBestHash = h.Node.Best.Hash
			}
			fmt.Printf("[Sync] pruned activation target changed while finishing sync (%s -> %s); skipping finalization\n", targetBestHash, currentBestHash)
			h.Node.IsSyncing = true
			h.Node.SyncState = node.SyncBodies
			h.Node.Unlock()
			return false
		}
		h.Node.SyncState = node.SyncSynced
		h.Node.IsSyncing = false
		h.Node.DB.Put("meta", "best", []byte(h.Node.Best.Hash))
		fmt.Printf("[Sync] sync completed. Best height is now %d\n", h.Node.Best.Height)
		h.Node.Unlock()
		if h.Node.IsPrunedMode() {
			go h.Node.PruneBlocks()
		}
		h.broadcastCurrentMempool()
		return true
	}

	if len(newMainChain) == 0 || newMainChain[0].Height != 0 {
		fmt.Printf("[Sync] finalization failed. Best chain height remains at %d\n",
			func() uint64 {
				if len(newMainChain) > 0 {
					fmt.Printf("[Debug] best chain branch starts at height %d, prev hash %x\n",
						newMainChain[0].Height, newMainChain[0].PrevHash)
					fmt.Printf("[Debug] genesis block hash: %x\n",
						h.Node.Blocks[hex.EncodeToString(blockchain.NewGenesisBlock(h.Node.Target).Hash)].Hash)
					return newMainChain[0].Height
				}
				return 999
			}())
		h.Node.Unlock()
		return false
	}

	h.Node.Chain = newMainChain
	h.Node.Best = actualBest

	h.Node.Unlock()

	fmt.Println("[Sync] rebuilding UTXO set...")
	if err := h.Node.RebuildUTXO(); err != nil {
		fmt.Printf("[Sync] full validation failed during rebuild: %v\n", err)
		h.Node.Lock()
		currentBestHash := ""
		if h.Node.Best != nil {
			currentBestHash = h.Node.Best.Hash
		}
		if currentBestHash == targetBestHash {
			h.Node.Chain = oldChain
			h.Node.Best = oldBest
		} else {
			fmt.Printf("[Sync] skipping rollback because best chain advanced during rebuild (%s -> %s)\n", targetBestHash, currentBestHash)
		}
		h.Node.IsSyncing = true
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		return false
	}

	chainSnapshot := append([]*blockchain.Block(nil), h.Node.Chain...)
	if required, reason := indexer.DetectBackfillNeed(chainSnapshot); required {
		fmt.Printf("[Indexer] Sync finalization requires backfill: %s\n", reason)
		genesisHash := ""
		if len(chainSnapshot) > 0 && chainSnapshot[0] != nil {
			genesisHash = hex.EncodeToString(chainSnapshot[0].Hash)
		}
		if err := indexer.BackfillMainChain(genesisHash, chainSnapshot); err != nil {
			fmt.Printf("[Indexer] Sync finalization backfill failed: %v\n", err)
		}
	}

	h.Node.Lock()
	if h.Node.Best == nil || h.Node.Best.Hash != targetBestHash || h.Node.HasMissingBodiesLocked() {
		currentBestHash := ""
		if h.Node.Best != nil {
			currentBestHash = h.Node.Best.Hash
		}
		fmt.Printf("[Sync] best chain changed before final sync commit (%s -> %s); returning to body sync\\n", targetBestHash, currentBestHash)
		h.Node.IsSyncing = true
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		return false
	}
	h.Node.SyncState = node.SyncSynced
	h.Node.IsSyncing = false
	h.Node.DB.Put("meta", "best", []byte(h.Node.Best.Hash))
	fmt.Printf("[Sync] sync completed. Best height is now %d\n", h.Node.Best.Height)
	h.Node.Unlock()
	if h.Node.IsPrunedMode() {
		go h.Node.PruneBlocks()
	}
	h.broadcastCurrentMempool()
	return true
}
func (h *Handler) broadcastInvExcept(hash string, except *Peer) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	for nodeID, p := range h.Network.Peers {
		if p == nil || p == except || !p.IsActive() {
			continue
		}
		if !p.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "block",
				Hashes: []string{hash},
			},
		}) {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func (h *Handler) broadcastInv(hash string) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {
		if p == nil || !p.IsActive() {
			continue
		}
		if !p.Send(Message{
			Type: MsgInv,
			Data: InvPayload{
				Type:   "block",
				Hashes: []string{hash},
			},
		}) {
			delete(h.Network.Peers, nodeID)
		}
	}
}

func decode(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (h *Handler) handleGetAddr(peer *Peer, msg *Message) {
	addrs := h.Network.PeerManager.AddrMgr.GetAll()

	if len(addrs) > 1000 {
		addrs = addrs[:1000]
	}

	peer.Send(Message{
		Type: MsgAddr,
		Data: addrs,
	})

	log.Printf("[Network] sent %d addrs to %s", len(addrs), peer.Addr)
}
func (h *Handler) handleAddr(peer *Peer, msg *Message) {
	var addrs []string
	if err := decode(msg.Data, &addrs); err != nil {
		log.Println("[Network] failed to decode addr payload:", err)
		return
	}

	if len(addrs) == 0 {
		return
	}

	pm := h.Network.PeerManager

	h.Node.Lock()
	addedCount := 0
	for _, addr := range addrs {

		if pm.isSelfDialAddress(addr) {
			continue
		}

		pm.mu.Lock()
		exists := pm.hasActiveOrPendingAddrLocked(addr)
		pm.mu.Unlock()
		if exists {
			continue
		}

		if pm.AddrMgr.Add(addr) {
			addedCount++

			pm.mu.Lock()
			currentActive := len(pm.Active)
			maxPeers := pm.MaxPeers
			pm.mu.Unlock()

			if currentActive < maxPeers {
				log.Printf("[Network] connecting to new peer %s\n", addr)
				go pm.Connect(addr)
			}
		}
	}
	h.Node.Unlock()

	log.Printf("[Network] received %d new addrs from %s", addedCount, peer.Addr)

	pm.ensurePeers()
}
func (h *Handler) handleTx(peer *Peer, msg *Message) {

	if h.Node.SyncState != node.SyncSynced {

		return
	}

	dataMap, ok := msg.Data.(map[string]interface{})
	if !ok {
		if h.debugP2PTraffic {
			fmt.Println("[Kali-Debug] msg.Data is not a valid map[string]interface{}")
		}
		return
	}

	txBase64Str, ok := dataMap["tx"].(string)
	if !ok {
		if h.debugP2PTraffic {
			fmt.Println("[Kali-Debug] received tx payload without a valid 'tx' field")
		}
		return
	}

	txBytes, err := base64.StdEncoding.DecodeString(txBase64Str)
	if err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("[Kali-Debug] base64 decode error: %v\n", err)
		}
		return
	}

	tx, err := blockchain.DeserializeTransaction(txBytes)
	if err != nil {
		if h.debugP2PTraffic {
			fmt.Printf("[Kali-Debug] tx deserialize error: %v\n", err)
		}
		return
	}

	h.releaseTxRequest(tx.ID)

	if h.Node.Mempool.Has(tx.ID) {

		return
	}

	if h.debugP2PTraffic {
		fmt.Printf("[Kali-Debug] attempting to add tx %s from network into the mempool\n", tx.ID[:8])
	}

	if ok := h.Node.AddTx(*tx, peer.NodeID); !ok {
		if h.debugP2PTraffic {
			fmt.Printf("[Kali-Debug] tx %s was rejected by Node.AddTx\n", tx.ID[:8])
		}
		return
	}

	fmt.Printf("[P2P] accepted tx %s from the network and relaying its inventory\n", tx.ID[:8])

	h.broadcastTxInv(tx.ID)
}
func (h *Handler) broadcastTxInv(txid string) {

	if h.debugP2PTraffic {
		fmt.Printf("[Debug] broadcastTxInv: %s\n", txid[:8])
	}

	if h.Node.SyncState != node.SyncSynced {

		if h.debugP2PTraffic {
			fmt.Printf("[Debug] broadcast deferred because sync state is %v, not Synced\n", h.Node.SyncState)
		}
		return
	}

	sourceNodeID := h.Node.Mempool.GetSource(txid)

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	if h.debugP2PTraffic {
		fmt.Printf("[Debug] broadcasting tx inventory to %d peers\n", len(h.Network.Peers))
	}

	invMsg := Message{
		Type: MsgInv,
		Data: InvPayload{
			Type:   "tx",
			Hashes: []string{txid},
		},
	}

	count := 0
	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {
		if p == nil || !p.IsActive() {
			continue
		}
		if sourceNodeID != 0 && nodeID == sourceNodeID {
			continue
		}
		if p.Send(invMsg) {
			count++
		} else {
			delete(h.Network.Peers, nodeID)
		}
	}

	if count > 0 {
		fmt.Printf("[P2P] broadcasted tx inventory to %d peers: %s\n", count, txid[:8])
	} else if h.debugP2PTraffic {
		// When count is zero, keep the debug output concise instead of logging every peer.
		fmt.Println("[Debug] no active peers accepted the tx inventory broadcast")
	}
}

func (h *Handler) BroadcastLocalTx(tx blockchain.Transaction) {

	txid := tx.ID

	log.Println("[P2P] broadcast local tx:", txid)

	log.Println("[P2P] broadcasting local tx:", txid)

	if h.Node.SyncState != node.SyncSynced {
		h.deferLocalTxBroadcast(txid)
		log.Println("[P2P] local tx queued until sync completes:", txid)
		log.Println("[P2P] local tx queued until sync completes:", txid)
		return
	}

	h.broadcastTxInv(txid)
}

func (h *Handler) deferLocalTxBroadcast(txid string) {
	h.deferredTxMu.Lock()
	defer h.deferredTxMu.Unlock()
	h.deferredLocalTxs[txid] = struct{}{}
}

func (h *Handler) takeDeferredLocalTxs() []string {
	h.deferredTxMu.Lock()
	defer h.deferredTxMu.Unlock()

	if len(h.deferredLocalTxs) == 0 {
		return nil
	}

	txids := make([]string, 0, len(h.deferredLocalTxs))
	for txid := range h.deferredLocalTxs {
		txids = append(txids, txid)
		delete(h.deferredLocalTxs, txid)
	}
	return txids
}

func (h *Handler) broadcastCurrentMempool() {
	broadcasted := make(map[string]struct{})

	deferred := h.takeDeferredLocalTxs()
	if len(deferred) > 0 {
		fmt.Printf("[P2P] Sync finished, replaying %d deferred local txs...\n", len(deferred))
		fmt.Printf("[P2P] replaying %d deferred local txs after sync\n", len(deferred))
		for _, txid := range deferred {
			if !h.Node.Mempool.Has(txid) {
				continue
			}
			h.broadcastTxInv(txid)
			broadcasted[txid] = struct{}{}
		}
	}

	allTxs := h.Node.Mempool.GetAll()
	if len(allTxs) == 0 {
		return
	}

	fmt.Printf("[P2P] broadcasting %d mempool txs\n", len(allTxs))
	for txid := range allTxs {
		if _, ok := broadcasted[txid]; ok {
			continue
		}
		h.broadcastTxInv(txid)
	}
}

func (h *Handler) handleGetHeaders(peer *Peer, msg *Message) {
	var req GetHeadersPayload
	if err := decode(msg.Data, &req); err != nil {
		log.Println("[Network] decode GetHeaders error:", err)
		return
	}

	h.Node.Lock()
	var startHeight int64 = -1

	for _, hash := range req.Locators {

		if bi, exists := h.Node.Blocks[hash]; exists {

			if h.Node.IsOnMainChain(bi) {
				startHeight = int64(bi.Height)
				break
			}
		}
	}

	if startHeight == -1 {

		startHeight = -1
	}

	h.Node.Unlock()
	_ = startHeight

	var headers []HeaderDTO
	for _, bi := range h.Node.HeadersAfterLocators(req.Locators, 2000) {
		headers = append(headers, BlockIndexToHeaderDTO(bi))
	}

	peer.Send(Message{
		Type: MsgHeaders,
		Data: HeadersPayload{Headers: headers},
	})
}

func (h *Handler) handleHeaders(peer *Peer, msg *Message) {
	var payload HeadersPayload
	if err := decode(msg.Data, &payload); err != nil {
		log.Println("decode headers error:", err)
		return
	}

	headersCount := len(payload.Headers)
	fmt.Printf("[Sync] received %d headers from %s\n", headersCount, peer.Addr)

	if headersCount == 0 {
		fmt.Println("[Sync] peer reported no more headers")
		h.Node.Lock()
		h.Node.HeadersSynced = true
		needBodies := h.Node.HasMissingBodiesLocked()
		isSyncing := h.Node.IsSyncing
		h.Node.Unlock()

		if needBodies {
			h.requestMissingBlockBodies(peer)
		} else {

			if isSyncing && h.finishSyncing() {
				fmt.Println("[Network] headers synced successfully; requesting mempool...")
				h.requestMempool(peer)
			}
		}
		return
	}

	h.Node.Lock()
	addedCount := 0
	for _, hdr := range payload.Headers {
		headerBlock := HeaderDTOToBlock(hdr)
		headerHash := hex.EncodeToString(headerBlock.CalcHash())
		prevHash := strings.ToLower(hdr.PrevHash)
		if !strings.EqualFold(headerHash, hdr.Hash) {
			fmt.Printf("[Security] rejected header %s: claimed hash does not match recomputed hash\n", hdr.Hash)
			continue
		}

		target := headerBlock.Target
		if target == nil || target.Sign() <= 0 {
			fmt.Printf("[Security] rejected header %s: invalid target\n", headerHash)
			continue
		}

		hashInt := new(big.Int).SetBytes(headerBlock.CalcHash())
		if hashInt.Cmp(target) > 0 {
			fmt.Printf("[Security] rejected header %s: PoW does not satisfy target\n", headerHash)
			continue
		}

		if _, ok := h.Node.Blocks[headerHash]; ok {
			continue
		}

		parent, ok := h.Node.Blocks[prevHash]
		if !ok {
			continue
		}

		expectedHeight := parent.Height + 1
		if hdr.Height != expectedHeight {
			log.Printf("[Security] rejected header %s from %s: invalid height %d (expected %d)", headerHash, peer.Addr, hdr.Height, expectedHeight)
			continue
		}

		work := node.WorkFromTarget(target)
		cumWork := new(big.Int).Set(work)
		if parent.CumWorkInt != nil {
			cumWork = new(big.Int).Add(new(big.Int).Set(parent.CumWorkInt), work)
		}

		bi := &node.BlockIndex{
			Hash:       headerHash,
			PrevHash:   prevHash,
			Height:     expectedHeight,
			CumWork:    cumWork.Text(16),
			Bits:       hdr.Bits,
			Timestamp:  hdr.Timestamp,
			Nonce:      hdr.Nonce,
			MerkleRoot: hdr.MerkleRoot,
			CumWorkInt: cumWork,
		}
		h.Node.Blocks[headerHash] = bi
		if parent != nil {
			bi.Parent = parent
			existsChild := false
			for _, child := range parent.Children {
				if child != nil && child.Hash == bi.Hash {
					existsChild = true
					break
				}
			}
			if !existsChild {
				parent.Children = append(parent.Children, bi)
			}
		}

		addedCount++
	}
	h.Node.Unlock()

	if addedCount == 0 {
		fmt.Println("[Sync] no new headers were accepted from this batch")
		h.Node.Lock()
		h.Node.HeadersSynced = true
		needBodies := h.Node.HasMissingBodiesLocked()
		isSyncing := h.Node.IsSyncing
		h.Node.Unlock()
		if needBodies {
			h.requestMissingBlockBodies(peer)
		} else {
			if isSyncing && h.finishSyncing() {
				fmt.Println("[Network] headers synced successfully; requesting mempool...")
				h.requestMempool(peer)
			}
		}
		return
	}

	if headersCount >= 500 {
		fmt.Println("[Sync] header sync progress continues; requesting more headers...")
		peer.Send(Message{
			Type: MsgGetHeaders,
			Data: GetHeadersPayload{Locators: h.buildBlockLocator()},
		})
		return
	}

	fmt.Printf("[Sync] added %d new headers; transitioning to block body sync\n", addedCount)
	h.Node.Lock()
	h.Node.HeadersSynced = true
	h.Node.Unlock()
	h.requestMissingBlockBodies(peer)
}

func (h *Handler) requestMissingBlockBodies(peer *Peer) {
	missingBlocks := h.Node.MissingBlockBodies(16)

	if len(missingBlocks) > 0 {
		h.Node.Lock()
		h.Node.SyncState = node.SyncBodies
		h.Node.Unlock()
		requested := 0

		for i := len(missingBlocks) - 1; i >= 0; i-- {
			target := missingBlocks[i]
			if h.requestBlock(peer, target.Hash) {
				requested++
			}
		}
		if requested > 0 {
			fmt.Printf("[Sync] identified %d missing blocks and requested %d bodies\n", len(missingBlocks), requested)
		}
		return
	}

	if h.Node.SyncState != node.SyncSynced {
		fmt.Println("[Sync] All block bodies are ready, finishing sync...")
		if h.finishSyncing() {
			fmt.Println("[Network] Sync finished, requesting mempool...")
			h.requestMempool(peer)
		}
	}
}
func (h *Handler) requestBlock(peer *Peer, hash string) bool {
	if peer == nil || hash == "" {
		return false
	}
	if !h.reserveBlockRequest(hash) {
		return false
	}

	peer.Send(Message{
		Type: MsgGetData,
		Data: GetDataPayload{
			Type: "block",
			Hash: hash,
		},
	})
	return true
}

func (h *Handler) buildBlockLocator() []string {
	var locators []string

	bi := h.Node.Best
	step := 1
	height := 0

	for bi != nil {
		locators = append(locators, bi.Hash)

		if height >= 10 {
			step *= 2
		}

		for i := 0; i < step && bi != nil; i++ {
			bi = bi.Parent
		}
		height++
	}

	return locators
}

func (h *Handler) BroadcastNewBlock(b *blockchain.Block) {

	dto := BlockToDTO(b, nil)

	log.Printf("[Network] broadcasting new block: height %d, hash %x", b.Height, b.Hash)

	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	activeCount := 0

	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {

		fmt.Printf("    -> [Network] checking peer %s [NodeID: %d] (State: %d)\n", p.Addr, nodeID, p.State)

		if p != nil && p.IsActive() {
			if !p.Send(Message{
				Type: MsgBlock,
				Data: dto,
			}) {
				delete(h.Network.Peers, nodeID)
				continue
			}
			fmt.Printf("    -> [Network] sent MsgBlock to %s [NodeID: %d]\n", p.Addr, nodeID)
			activeCount++
		}
	}

	if activeCount == 0 {
		fmt.Println("[Warning] block broadcast skipped because there are no active peers")
	}
}

func encode(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (h *Handler) countMissingBlocks() int {
	missingCount := 0

	h.Node.Lock()
	defer h.Node.Unlock()

	for _, bi := range h.Node.Blocks {

		if bi.Block == nil && bi.Height > 0 {
			missingCount++
		}
	}

	return missingCount
}

func (h *Handler) RequestHistoricalBlock(hashHex string) {
	h.Network.mu.Lock()
	defer h.Network.mu.Unlock()

	fmt.Printf("[Query] requesting historical block %s from archive peers...\n", hashHex[:8])

	requestSent := false
	h.pruneClosedPeersLocked()
	for nodeID, p := range h.Network.Peers {

		if p != nil && p.IsActive() && node.NormalizeMode(p.Mode) == node.ModeArchive {
			if !p.Send(Message{
				Type: MsgGetData,
				Data: GetDataPayload{
					Type: "block",
					Hash: hashHex,
				},
			}) {
				delete(h.Network.Peers, nodeID)
				continue
			}
			fmt.Printf("[Network] requested archive sync data from peer %s\\n", p.Addr)
			requestSent = true
		}
	}

	if !requestSent {
		fmt.Println("[Network] skipped archive sync request because no eligible outbound peer was available")
	}
}

func (h *Handler) BroadcastTransaction(txid string) {
	h.broadcastTxInv(txid)
}

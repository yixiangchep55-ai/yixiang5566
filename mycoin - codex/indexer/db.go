package indexer

import (
	"encoding/hex"
	"fmt"
	"mycoin/blockchain"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB
var Enabled bool

type BlockRecord struct {
	Hash        string `gorm:"primaryKey;size:64"`
	Height      uint64 `gorm:"index"`
	PrevHash    string `gorm:"size:64"`
	Timestamp   int64  `gorm:"index"`
	Miner       string `gorm:"index;size:64"`
	TxCount     int
	IsMainChain bool `gorm:"index"`
	CreatedAt   time.Time
}

type TxRecord struct {
	ID          uint   `gorm:"primaryKey"`
	TxID        string `gorm:"index;size:64"`
	BlockHash   string `gorm:"index;size:64"`
	BlockHeight uint64 `gorm:"index"`
	IsCoinbase  bool
	IsMainChain bool `gorm:"index"`
	Fee         uint64
	CreatedAt   time.Time
}

type AddressLedger struct {
	ID        uint   `gorm:"primaryKey"`
	TxID      string `gorm:"index;size:64"`
	Address   string `gorm:"index;size:64"`
	Type      string `gorm:"size:10"`
	Amount    uint64
	VoutIndex int
	CreatedAt time.Time
}

type SystemConfig struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func InitDB(currentGenesisHash string, nodeHeight int) {
	enabledEnv := os.Getenv("INDEXER_ENABLED")
	if enabledEnv != "true" {
		fmt.Println("[Indexer] Disabled because INDEXER_ENABLED != true.")
		Enabled = false
		return
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=860105 dbname=mycoin_explorer port=5432 sslmode=disable TimeZone=Asia/Taipei"
	}

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fmt.Printf("[Indexer] Database connection failed: %v. Falling back to node-only mode.\n", err)
		Enabled = false
		return
	}

	err = DB.AutoMigrate(&BlockRecord{}, &TxRecord{}, &AddressLedger{}, &SystemConfig{})
	if err != nil {
		fmt.Printf("[Indexer] AutoMigrate failed: %v. Falling back to node-only mode.\n", err)
		Enabled = false
		return
	}

	if currentGenesisHash != "" {
		var config SystemConfig
		result := DB.Where("key = ?", "genesis_hash").First(&config)

		var blockCount int64
		DB.Model(&BlockRecord{}).Count(&blockCount)

		isNodeReset := nodeHeight <= 1 && blockCount > 1
		if result.Error != nil || config.Value != currentGenesisHash || isNodeReset {
			if isNodeReset {
				fmt.Printf("[Indexer] Node reset detected (node height=%d, indexed blocks=%d). Resetting PostgreSQL index.\n", nodeHeight, blockCount)
			} else {
				fmt.Println("[Indexer] Genesis mismatch or missing index metadata. Resetting PostgreSQL index.")
			}

			err := DB.Exec("TRUNCATE TABLE block_records, tx_records, address_ledgers RESTART IDENTITY CASCADE;").Error
			if err != nil {
				fmt.Printf("[Indexer] Failed to clear old index data: %v\n", err)
			} else {
				DB.Where("key = ?", "genesis_hash").Assign(SystemConfig{Value: currentGenesisHash}).FirstOrCreate(&SystemConfig{Key: "genesis_hash"})
				fmt.Printf("[Indexer] Index metadata reset. Current genesis: %s...\n", currentGenesisHash[:8])
			}
		} else {
			fmt.Printf("[Indexer] Genesis check passed: %s...\n", config.Value[:8])
		}
	}

	Enabled = true
	fmt.Println("[Indexer] PostgreSQL connected. Indexer is running.")
}

func IndexBlock(b *blockchain.Block, height uint64, isMainChain bool) {
	if !Enabled || DB == nil || b == nil {
		return
	}

	txDB := DB.Begin()
	defer func() {
		if r := recover(); r != nil {
			txDB.Rollback()
		}
	}()

	blockHash := hex.EncodeToString(b.Hash)

	if isMainChain {
		txDB.Where("tx_id IN (SELECT tx_id FROM tx_records WHERE block_hash = ?)", blockHash).Delete(&AddressLedger{})
	}

	txDB.Where("block_hash = ?", blockHash).Delete(&TxRecord{})

	blkRecord := BlockRecord{
		Hash:        blockHash,
		Height:      height,
		PrevHash:    hex.EncodeToString(b.PrevHash),
		Timestamp:   b.Timestamp,
		TxCount:     len(b.Transactions),
		IsMainChain: isMainChain,
	}
	txDB.Save(&blkRecord)

	for _, tx := range b.Transactions {
		txID := tx.ID
		isCoinbase := len(tx.Inputs) == 1 && tx.Inputs[0].TxID == ""

		txDB.Create(&TxRecord{
			TxID:        txID,
			BlockHash:   blockHash,
			BlockHeight: height,
			IsCoinbase:  isCoinbase,
			IsMainChain: isMainChain,
		})

		if isCoinbase && len(tx.Outputs) > 0 {
			txDB.Model(&BlockRecord{}).Where("hash = ?", blockHash).Update("miner", tx.Outputs[0].To)
		}

		if !isMainChain {
			continue
		}

		if !isCoinbase {
			for _, vin := range tx.Inputs {
				var prevOut AddressLedger
				txDB.Where("tx_id = ? AND type = 'IN' AND vout_index = ?", vin.TxID, vin.Index).First(&prevOut)
				if prevOut.Address != "" {
					txDB.Create(&AddressLedger{
						TxID:      txID,
						Address:   prevOut.Address,
						Type:      "OUT",
						Amount:    prevOut.Amount,
						VoutIndex: vin.Index,
					})
				}
			}
		}

		for i, vout := range tx.Outputs {
			txDB.Create(&AddressLedger{
				TxID:      txID,
				Address:   vout.To,
				Type:      "IN",
				Amount:    uint64(vout.Amount),
				VoutIndex: i,
			})
		}
	}

	txDB.Commit()
	chainType := "main"
	if !isMainChain {
		chainType = "orphan"
	}
	fmt.Printf("[Indexer] Block %d (%s) indexed. Hash: %s...\n", height, chainType, blockHash[:8])
}

func UnindexBlock(blockHashHex string) {
	if !Enabled || DB == nil {
		return
	}

	txDB := DB.Begin()
	defer func() {
		if r := recover(); r != nil {
			txDB.Rollback()
		}
	}()

	err := txDB.Where("tx_id IN (SELECT tx_id FROM tx_records WHERE block_hash = ?)", blockHashHex).Delete(&AddressLedger{}).Error
	if err != nil {
		txDB.Rollback()
		return
	}

	txDB.Model(&BlockRecord{}).Where("hash = ?", blockHashHex).Update("is_main_chain", false)
	txDB.Model(&TxRecord{}).Where("block_hash = ?", blockHashHex).Update("is_main_chain", false)

	txDB.Commit()
	fmt.Printf("[Indexer] Block downgraded to non-main-chain: %s...\n", blockHashHex[:8])
}

func expectedMainChainTxCount(chain []*blockchain.Block) int64 {
	var total int64
	for _, block := range chain {
		if block == nil {
			continue
		}
		total += int64(len(block.Transactions))
	}
	return total
}

func DetectBackfillNeed(chain []*blockchain.Block) (bool, string) {
	if !Enabled || DB == nil || len(chain) == 0 {
		return false, ""
	}

	expectedBlocks := int64(len(chain))
	expectedTipHeight := int64(chain[len(chain)-1].Height)
	expectedTxs := expectedMainChainTxCount(chain)

	var mainBlockCount int64
	if err := DB.Model(&BlockRecord{}).Where("is_main_chain = ?", true).Count(&mainBlockCount).Error; err != nil {
		return true, "failed to count indexed main-chain blocks"
	}

	var indexedTipHeight int64
	if err := DB.Model(&BlockRecord{}).
		Where("is_main_chain = ?", true).
		Select("COALESCE(MAX(height), 0)").
		Scan(&indexedTipHeight).Error; err != nil {
		return true, "failed to inspect indexed tip height"
	}

	var mainTxCount int64
	if err := DB.Model(&TxRecord{}).Where("is_main_chain = ?", true).Count(&mainTxCount).Error; err != nil {
		return true, "failed to count indexed main-chain transactions"
	}

	var ledgerCount int64
	if err := DB.Model(&AddressLedger{}).Count(&ledgerCount).Error; err != nil {
		return true, "failed to count indexed address ledger rows"
	}

	switch {
	case mainBlockCount != expectedBlocks:
		return true, fmt.Sprintf("main-chain block count mismatch (%d indexed vs %d loaded)", mainBlockCount, expectedBlocks)
	case indexedTipHeight != expectedTipHeight:
		return true, fmt.Sprintf("indexed tip mismatch (%d indexed vs %d loaded)", indexedTipHeight, expectedTipHeight)
	case mainTxCount != expectedTxs:
		return true, fmt.Sprintf("main-chain tx count mismatch (%d indexed vs %d loaded)", mainTxCount, expectedTxs)
	case expectedTxs > 0 && ledgerCount == 0:
		return true, "address ledger is empty for a non-empty main chain"
	default:
		return false, ""
	}
}

func resetIndexerTables(currentGenesisHash string) error {
	if DB == nil {
		return fmt.Errorf("indexer database is not initialized")
	}
	if err := DB.Exec("TRUNCATE TABLE block_records, tx_records, address_ledgers RESTART IDENTITY CASCADE;").Error; err != nil {
		return err
	}
	if currentGenesisHash != "" {
		return DB.Where("key = ?", "genesis_hash").
			Assign(SystemConfig{Value: currentGenesisHash}).
			FirstOrCreate(&SystemConfig{Key: "genesis_hash"}).Error
	}
	return nil
}

func BackfillMainChain(currentGenesisHash string, chain []*blockchain.Block) error {
	if !Enabled || DB == nil || len(chain) == 0 {
		return nil
	}

	fmt.Printf("[Indexer] Backfilling existing main chain (%d blocks)...\n", len(chain))
	if err := resetIndexerTables(currentGenesisHash); err != nil {
		return fmt.Errorf("reset index tables: %w", err)
	}

	for _, block := range chain {
		if block == nil {
			continue
		}
		IndexBlock(block, block.Height, true)
	}

	fmt.Printf("[Indexer] Historical main-chain backfill complete at height %d.\n", chain[len(chain)-1].Height)
	return nil
}

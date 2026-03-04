package indexer

import (
	"encoding/hex"
	"fmt"
	"log"
	"mycoin/blockchain"
	"os" // 👈 新增引入 os，用來讀取系統環境變數
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 全域資料庫變數
var DB *gorm.DB
var Enabled bool = false // 👈 新增：用來控制 Indexer 是否要攔截數據

// 📦 區塊表
type BlockRecord struct {
	Height    uint64 `gorm:"primaryKey;autoIncrement:false"`
	Hash      string `gorm:"uniqueIndex;size:64"`
	PrevHash  string `gorm:"size:64"`
	Timestamp int64  `gorm:"index"`
	Miner     string `gorm:"index;size:64"`
	TxCount   int
	CreatedAt time.Time
}

// 💸 交易表
type TxRecord struct {
	TxID        string `gorm:"primaryKey;size:64"`
	BlockHash   string `gorm:"index;size:64"`
	BlockHeight uint64 `gorm:"index"`
	IsCoinbase  bool
	Fee         uint64
	CreatedAt   time.Time
}

// 🏦 地址流水帳表
type AddressLedger struct {
	ID        uint   `gorm:"primaryKey"`
	TxID      string `gorm:"index;size:64"`
	Address   string `gorm:"index;size:64"`
	Type      string `gorm:"size:10"` // "IN" (收錢) 或 "OUT" (花錢)
	Amount    uint64
	VoutIndex int // 用來精準對應 UTXO
	CreatedAt time.Time
}

// 初始化資料庫連線
func InitDB() {
	// 1. 讀取環境變數 (核心靈魂！)
	enabledEnv := os.Getenv("INDEXER_ENABLED")

	// 如果沒有明確要求開啟，就預設關閉
	if enabledEnv != "true" {
		fmt.Println("ℹ️  [Indexer] 環境未設定啟用 (INDEXER_ENABLED != true)，節點將以『純淨共識模式』運行。")
		Enabled = false
		return
	}

	// 2. 取得資料庫連線字串
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// 🌟 智慧預設值：保留你的密碼，方便你在 Windows 直接開發
		dsn = "host=localhost user=postgres password=860105 dbname=mycoin_explorer port=5432 sslmode=disable TimeZone=Asia/Taipei"
	}

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})

	if err != nil {
		// 現實做法：設定要開但連不上，發出警告並自動降級，不讓節點崩潰
		fmt.Printf("⚠️  [Indexer] 警告：要求開啟索引，但連線失敗: %v。自動降級為純節點模式。\n", err)
		Enabled = false
		return
	}

	// 3. 自動建立資料表結構
	err = DB.AutoMigrate(&BlockRecord{}, &TxRecord{}, &AddressLedger{})
	if err != nil {
		fmt.Printf("⚠️  [Indexer] 自動建立資料表失敗: %v。自動降級為純節點模式。\n", err)
		Enabled = false
		return
	}

	Enabled = true
	fmt.Println("✅ [Indexer] PostgreSQL 連線成功，大數據索引功能已啟動！")
}

// IndexBlock 負責將區塊與交易資料寫入 PostgreSQL
func IndexBlock(b *blockchain.Block, height uint64) {
	// 🚀 關鍵守門員：如果沒開啟，直接回傳，什麼都不做！
	if !Enabled || DB == nil {
		return
	}

	// 1. 開啟事務 (Transaction)
	txDB := DB.Begin()

	// 萬一執行過程中報錯，自動回滾（Panic 恢復）
	defer func() {
		if r := recover(); r != nil {
			txDB.Rollback()
		}
	}()

	blockHash := hex.EncodeToString(b.Hash)

	// [防重入邏輯]：如果發生重組，先刪除同高度的舊資料
	txDB.Where("block_height = ?", height).Delete(&TxRecord{})
	txDB.Where("height = ?", height).Delete(&BlockRecord{})

	// --- 2. 寫入區塊 ---
	blkRecord := BlockRecord{
		Height:    height,
		Hash:      blockHash,
		PrevHash:  hex.EncodeToString(b.PrevHash),
		Timestamp: b.Timestamp,
		TxCount:   len(b.Transactions),
	}
	if err := txDB.Create(&blkRecord).Error; err != nil {
		txDB.Rollback()
		log.Println("❌ IndexBlock Error (Block):", err)
		return
	}

	// --- 3. 處理交易與流水帳 ---
	for _, tx := range b.Transactions {
		txID := tx.ID
		isCoinbase := len(tx.Inputs) == 1 && tx.Inputs[0].TxID == ""

		txRecord := TxRecord{
			TxID:        txID,
			BlockHash:   blockHash,
			BlockHeight: height,
			IsCoinbase:  isCoinbase,
		}
		txDB.Create(&txRecord)

		// 處理 Inputs (流出)
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

		// 處理 Outputs (流入)
		for i, vout := range tx.Outputs {
			txDB.Create(&AddressLedger{
				TxID:      txID,
				Address:   vout.To,
				Type:      "IN",
				Amount:    uint64(vout.Amount),
				VoutIndex: i,
			})

			if isCoinbase && i == 0 {
				txDB.Model(&BlockRecord{}).Where("hash = ?", blockHash).Update("miner", vout.To)
			}
		}
	}

	// 4. 提交事務
	txDB.Commit()
	fmt.Printf("🗄️ [Indexer] 區塊 %d (Hash: %s...) 已同步至 PostgreSQL！\n", height, blockHash[:8])
}

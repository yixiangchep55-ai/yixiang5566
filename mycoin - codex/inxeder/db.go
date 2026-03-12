package indexer

import (
	"encoding/hex"
	"fmt"
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
	Hash        string `gorm:"primaryKey;size:64"` // 🔑 Hash 升級為絕對唯一的主鍵！
	Height      uint64 `gorm:"index"`              // 高度改為普通索引 (因為可能重複)
	PrevHash    string `gorm:"size:64"`
	Timestamp   int64  `gorm:"index"`
	Miner       string `gorm:"index;size:64"`
	TxCount     int
	IsMainChain bool `gorm:"index"` // 🌟 核心靈魂：主鏈標記！
	CreatedAt   time.Time
}

// 💸 交易表 (升級版)
type TxRecord struct {
	ID          uint   `gorm:"primaryKey"` // 🔑 新增流水號主鍵 (同一筆交易可能出現在主側兩條鏈)
	TxID        string `gorm:"index;size:64"`
	BlockHash   string `gorm:"index;size:64"`
	BlockHeight uint64 `gorm:"index"`
	IsCoinbase  bool
	IsMainChain bool `gorm:"index"` // 🌟 核心靈魂：主鏈標記！
	Fee         uint64
	CreatedAt   time.Time
}

// AddressLedger 保持不變，因為我們「絕對不讓」孤塊的錢進入流水帳！
type AddressLedger struct {
	ID        uint   `gorm:"primaryKey"`
	TxID      string `gorm:"index;size:64"`
	Address   string `gorm:"index;size:64"`
	Type      string `gorm:"size:10"`
	Amount    uint64
	VoutIndex int
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
func IndexBlock(b *blockchain.Block, height uint64, isMainChain bool) {
	if !Enabled || DB == nil {
		return
	}

	txDB := DB.Begin()
	defer func() {
		if r := recover(); r != nil {
			txDB.Rollback()
		}
	}()

	blockHash := hex.EncodeToString(b.Hash)

	// ==========================================
	// 🧹 重組防禦：清理舊數據 (注意刪除順序！)
	// ==========================================

	if isMainChain {
		// 1. 必須【先】刪除 AddressLedger，因為它的子查詢依賴 tx_records 表的存在！
		txDB.Where("tx_id IN (SELECT tx_id FROM tx_records WHERE block_hash = ?)", blockHash).Delete(&AddressLedger{})
	}

	// 2. 【後】刪除 TxRecord
	txDB.Where("block_hash = ?", blockHash).Delete(&TxRecord{})

	// 1. 寫入區塊 (使用 Save，如果有同樣 Hash 的區塊就會更新它的 IsMainChain 狀態)
	blkRecord := BlockRecord{
		Hash:        blockHash,
		Height:      height,
		PrevHash:    hex.EncodeToString(b.PrevHash),
		Timestamp:   b.Timestamp,
		TxCount:     len(b.Transactions),
		IsMainChain: isMainChain, // 👈 標記身份
	}
	txDB.Save(&blkRecord)

	// 2. 處理交易
	for _, tx := range b.Transactions {
		txID := tx.ID
		isCoinbase := len(tx.Inputs) == 1 && tx.Inputs[0].TxID == ""

		txDB.Create(&TxRecord{
			TxID:        txID,
			BlockHash:   blockHash,
			BlockHeight: height,
			IsCoinbase:  isCoinbase,
			IsMainChain: isMainChain, // 👈 標記身份
		})

		// =========================================================
		// 🛡️ 絕對防禦結界：如果這不是主鏈區塊，到這裡就停！不准記帳！
		// =========================================================
		if !isMainChain {
			continue
		}

		// --- 下面是只有主鏈才能執行的「資金轉移」邏輯 ---
		if !isCoinbase {
			for _, vin := range tx.Inputs {
				var prevOut AddressLedger
				txDB.Where("tx_id = ? AND type = 'IN' AND vout_index = ?", vin.TxID, vin.Index).First(&prevOut)
				if prevOut.Address != "" {
					txDB.Create(&AddressLedger{
						TxID: txID, Address: prevOut.Address, Type: "OUT", Amount: prevOut.Amount, VoutIndex: vin.Index,
					})
				}
			}
		}

		for i, vout := range tx.Outputs {
			txDB.Create(&AddressLedger{
				TxID: txID, Address: vout.To, Type: "IN", Amount: uint64(vout.Amount), VoutIndex: i,
			})
			if isCoinbase && i == 0 {
				txDB.Model(&BlockRecord{}).Where("hash = ?", blockHash).Update("miner", vout.To)
			}
		}
	}

	txDB.Commit()
	chainType := "主鏈"
	if !isMainChain {
		chainType = "⚠️ 孤塊"
	}
	fmt.Printf("🗄️ [Indexer] 區塊 %d (%s) 已同步！Hash: %s...\n", height, chainType, blockHash[:8])
}

// 🚀 UnindexBlock 改為「降級」，而不是「刪除」
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

	// 1. 沒收資金：刪除這個區塊產生的所有流水帳
	err1 := txDB.Where("tx_id IN (SELECT tx_id FROM tx_records WHERE block_hash = ?)", blockHashHex).Delete(&AddressLedger{}).Error
	if err1 != nil {
		txDB.Rollback()
		return
	}

	// 2. 降級區塊：把區塊和交易標記為「非主鏈 (孤塊)」！
	txDB.Model(&BlockRecord{}).Where("hash = ?", blockHashHex).Update("is_main_chain", false)
	txDB.Model(&TxRecord{}).Where("block_hash = ?", blockHashHex).Update("is_main_chain", false)

	txDB.Commit()
	fmt.Printf("🗑️ [Indexer] 深度重組防護：區塊 (Hash: %s...) 已被降級為孤塊！資金已沒收。\n", blockHashHex[:8])
}

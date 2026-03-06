package api

import (
	"encoding/json"
	"fmt"
	"mycoin/indexer"
	"net/http"
	"strings"
)

// StartServer 啟動區塊瀏覽器的 API 伺服器
func StartServer(port string) {
	// 🌟 設立兩個不同的路由 (櫃檯)
	http.HandleFunc("/api/blocks", getMainBlocks)       // 主鏈專用
	http.HandleFunc("/api/orphans", getOrphanBlocks)    // 孤塊專用
	http.HandleFunc("/api/address/", getAddressBalance) // 💼 錢包查詢專用
	http.HandleFunc("/api/transaction", sendTransaction)
	http.HandleFunc("/api/estimatefee", getEstimateFee) // 📊 自動預測手續費

	fmt.Printf("🌐 [API] 區塊瀏覽器 API 伺服器已啟動於 http://localhost:%s\n", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		fmt.Println("❌ [API] 伺服器啟動失敗:", err)
	}
}

// 櫃檯 1：專門獲取「主鏈區塊」
func getMainBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if indexer.DB == nil {
		http.Error(w, `{"error": "資料庫未連線"}`, http.StatusInternalServerError)
		return
	}

	var blocks []indexer.BlockRecord
	// 🚀 魔法 SQL 升級：加上 Where 條件過濾，只找 IsMainChain = true 的！
	result := indexer.DB.Where("is_main_chain = ?", true).Order("height desc").Limit(15).Find(&blocks)

	if result.Error != nil {
		http.Error(w, `{"error": "查詢失敗"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(blocks)
}

// 櫃檯 2：專門獲取「孤塊 (Orphans)」
func getOrphanBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if indexer.DB == nil {
		http.Error(w, `{"error": "資料庫未連線"}`, http.StatusInternalServerError)
		return
	}

	var blocks []indexer.BlockRecord
	// 🚀 魔法 SQL 升級：加上 Where 條件過濾，只找 IsMainChain = false 的！
	result := indexer.DB.Where("is_main_chain = ?", false).Order("height desc").Limit(15).Find(&blocks)

	if result.Error != nil {
		http.Error(w, `{"error": "查詢失敗"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(blocks)
}

// 💼 負責處理錢包查詢的函數 (GORM 智慧型結算 + 終端機偵測版)
func getAddressBalance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "OPTIONS" {
		return
	}

	address := strings.TrimPrefix(r.URL.Path, "/api/address/")
	if address == "" {
		http.Error(w, "請提供錢包地址", http.StatusBadRequest)
		return
	}

	var totalIn, totalOut float64

	// 1. 查詢總收入 (資料庫裡存的是 500, 150 這種整數)
	errIn := indexer.DB.Model(&indexer.AddressLedger{}).
		Where("address = ? AND type = ?", address, "IN").
		Select("COALESCE(SUM(amount), 0)").Scan(&totalIn).Error
	if errIn != nil {
		fmt.Println("❌ [查水表] 查詢收入時發生錯誤:", errIn)
		// 甚至可以回傳一個 API 錯誤
		http.Error(w, `{"error": "查詢收入失敗"}`, http.StatusInternalServerError)
		return
	}

	// 2. 查詢總支出
	errOut := indexer.DB.Model(&indexer.AddressLedger{}).
		Where("address = ? AND type = ?", address, "OUT").
		Select("COALESCE(SUM(amount), 0)").Scan(&totalOut).Error
	// 🚀 同理：也要「使用」這個 errOut
	if errOut != nil {
		fmt.Println("❌ [查水表] 查詢支出時發生錯誤:", errOut)
		http.Error(w, `{"error": "查詢支出失敗"}`, http.StatusInternalServerError)
		return
	}

	// ==========================================
	// 🕵️ 大偵探的單位還原：從 YiCent 轉回 YiCoin
	// ==========================================
	displayIn := totalIn / 100.0
	displayOut := totalOut / 100.0
	balance := (totalIn - totalOut) / 100.0

	// 修改終端機日誌，讓它也顯示小數點
	fmt.Printf("🔍 [查水表] 地址: %s | 總收入: %.2f | 總支出: %.2f | 餘額: %.2f\n",
		address[:8]+"...", displayIn, displayOut, balance)

	message := ""
	if totalIn == 0 {
		message = "This address has no transaction history yet."
	}

	// 回傳給 Vue 的 Balance 現在是漂亮的 5.00 了！
	json.NewEncoder(w).Encode(map[string]interface{}{
		"Address": address,
		"Balance": balance,
		"Message": message,
	})
}

// 💸 負責處理前端轉帳請求的函數
func sendTransaction(w http.ResponseWriter, r *http.Request) {
	// 1. 允許前端跨域連線 (CORS)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	// 2. 解析 Vue 前端傳來的 JSON 資料 (加入 Fee 欄位)
	var txReq struct {
		To     string  `json:"to"`
		Amount float64 `json:"amount"`
		Fee    float64 `json:"fee"` // 👈 大偵探加碼：接收手續費！
	}
	if err := json.NewDecoder(r.Body).Decode(&txReq); err != nil {
		http.Error(w, "無效的請求格式", http.StatusBadRequest)
		return
	}

	fmt.Printf("📬 [API] 收到轉帳請求：發送 %v 元到 %s (手續費: %v)\n", txReq.Amount, txReq.To, txReq.Fee)

	// 3. 🚀 呼叫底層的 Wallet RPC (:8082/wallet)
	// 完美對接你 server.go 裡面的 sendtoaddress 需要的三個參數！
	// 🚀 修正點：使用 %.8f 確保小數點精確輸出，且不會變成 1.5e-05
	rpcBody := fmt.Sprintf(`{"method": "sendtoaddress", "params": ["%s", %.8f, %.8f], "id": 1}`, txReq.To, txReq.Amount, txReq.Fee)

	// 👇 這裡確定用 /wallet 沒錯！
	resp, err := http.Post("http://localhost:8082/wallet", "application/json", strings.NewReader(rpcBody))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "無法連線到錢包 RPC (:8082 沒開或連線失敗)",
		})
		return
	}
	defer resp.Body.Close()

	// 4. 解析錢包 RPC 回傳的結果
	var rpcResp struct {
		Result string      `json:"result"`
		Error  interface{} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&rpcResp)

	if rpcResp.Error != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("錢包拒絕轉帳: %v", rpcResp.Error),
		})
		return
	}

	// 5. 成功！把 TxID 傳回給 Vue 前端顯示！
	fmt.Printf("✅ [API] 轉帳成功！交易已進入 Mempool，TxID: %s\n", rpcResp.Result)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"txid": rpcResp.Result,
	})
}

// 📊 負責向底層詢問「當前建議手續費」的函數
func getEstimateFee(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "OPTIONS" {
		return
	}

	// 🚀 去敲門問 Wallet RPC
	rpcBody := `{"method": "estimatefee", "params": [], "id": 1}`
	resp, err := http.Post("http://localhost:8082/wallet", "application/json", strings.NewReader(rpcBody))

	if err != nil {
		// 🚀 修正點：如果錢包沒開，預設回傳 0.01 (1 YiCent)
		json.NewEncoder(w).Encode(map[string]interface{}{"fee": 0.01})
		return
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result float64 `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&rpcResp)

	// 把最精準的報價傳給 Vue
	json.NewEncoder(w).Encode(map[string]interface{}{
		"fee": rpcResp.Result,
	})
}

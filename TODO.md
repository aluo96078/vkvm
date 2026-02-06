# VKVM v2.0 TODO List

此文件為開發任務的簡要追蹤清單，詳細計劃請參考 [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md)

## 🚀 Phase 1: 陷阱與輸入 (The Trap) - MVP 優先

### Protocol 擴充
- [x] #1 擴充 `internal/protocol/protocol.go` 新增鍵鼠事件類型
- [x] #2 建立 `internal/input/types.go` 定義共用類型

### Windows Raw Input
- [x] #3 實作透明視窗與 Raw Input 註冊 (`internal/input/trap_windows.go`)
- [x] #4 實作無限滑動機制 (`ClipCursor` + `SetCursorPos`)
- [x] #5 實作 Kill Switch (`Ctrl+Alt+Esc`)

### WebSocket 整合
- [x] #6 擴充 WSClient 支援鍵鼠事件發送
- [x] #7 實作事件序列化與錯誤處理

### Mac 鍵鼠注入
- [x] #8 實作 macOS 事件注入 (`internal/input/inject_darwin.go`)
- [x] #9 螢幕座標轉換邏輯

### 整合測試
- [x] #10 端到端測試與延遲優化
- [x] #11 邊界情況測試 (多螢幕、不同 DPI)

**里程碑：** Windows → Mac 鍵鼠控制 (LAN) 可用 ✅

---

## 🔧 Phase 2: 系統服務補完 (The Safety Net)

### 架構重構
- [ ] #12 修改 `cmd/main.go` 支援 `-server` flag
- [ ] #13 建立 `internal/daemon/daemon.go` 主邏輯
- [ ] #14 實作 TCP/UDP 監聽器

### IPC 機制
- [ ] #15 實作 Unix Socket 伺服器 (`internal/daemon/ipc.go`)
- [ ] #16 實作 JSON-RPC 2.0 協議
- [ ] #17 實作所有 RPC 方法 (status, config, restart, permissions)

### 安裝系統
- [ ] #18 實作安裝腳本 (`internal/daemon/install.go`)
- [ ] #19 LaunchDaemon plist 生成與註冊
- [ ] #20 UI 安裝介面與權限引導

### TCC 處理
- [ ] #21 權限檢查邏輯實作
- [ ] #22 圖文教學介面
- [ ] #23 權限持久性測試

### 封裝測試
- [ ] #24 .dmg 安裝檔建立
- [ ] #25 開機自動啟動測試
- [ ] #26 鎖定畫面控制測試

**里程碑：** Mac 鎖定畫面可被控制 ✅

---

## 🔗 Phase 3: DDC/CI 整合 (The Fusion)

### Profile 切換
- [x] #27 擴充 switcher.go 支援鍵鼠控制權轉移
- [x] #28 螢幕切換 + 鍵鼠控制同步邏輯
- [x] #29 控制權狀態管理

### 跟隨焦點
- [x] #30 實作焦點追蹤邏輯 (不需要 - 單電腦接管場景)
- [x] #31 螢幕邊界偵測 (不需要 - 單電腦接管場景)
- [x] #32 配置介面 (不需要 - 單電腦接管場景)

### 整合測試
- [ ] #33 多機器切換測試
- [x] #34 效能優化

**里程碑：** 完整 KVM 體驗 (螢幕 + 鍵鼠 一鍵切換) ✅

---

## 📡 Phase 4: 藍牙核心 (The Phantom) - 進階

### 可行性研究
- [ ] #35 Windows BLE Peripheral Mode 研究
- [ ] #36 ESP32 方案原型開發
- [ ] #37 替代方案評估與選擇

### BLE HID 實作
- [ ] #38 BLE HID Profile 實作
- [ ] #39 鍵鼠事件轉 BLE 訊號
- [ ] #40 Mac 端配對與連線

### 整合測試
- [ ] #41 與現有架構整合
- [ ] #42 BLE 優先自動切換邏輯
- [ ] #43 相容性測試

**里程碑：** Mac 免安裝軟體即可被控制 ✅

---

## 🧪 品質保證

### 測試
- [ ] #44 單元測試覆蓋率 > 80%
- [ ] #45 整合測試涵蓋主要功能
- [ ] #46 效能基準測試

### 文件
- [ ] #47 更新 README.md 說明新功能
- [ ] #48 撰寫安裝與設定指南
- [ ] #49 API 文件

### 發行
- [ ] #50 版本標籤與發行說明
- [ ] #51 安裝程式測試
- [ ] #52 使用者文件

---

## 📊 進度統計

- **總任務數：** 52
- **已完成：** 18
- **進行中：** 0
- **待開始：** 34

### 按 Phase 分佈
- Phase 1: 11 任務
- Phase 2: 15 任務
- Phase 3: 6 任務 (6 已完成)
- Phase 4: 9 任務
- 品質保證: 11 任務

---

## 🎯 開發規範

- [x] 使用 Git Flow 工作流程
- [x] 每個任務建立對應分支
- [x] 程式碼審查後合併
- [x] 定期更新此 TODO 清單
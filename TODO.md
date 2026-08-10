# VKVM v2.0 TODO List

此文件為開發任務的簡要追蹤清單，既有架構構想請參考 [plan.md](plan.md)；本檔新增項目與出口條件代表目前較新的執行順序。

> 2026-08-09 狀態：`[x]` 代表程式碼、文件與可攜式測試證據已完成；不代表本環境已完成 Windows/macOS 實機、原生 SCM/LaunchAgent 保活、真實 USB/BLE、跨機 DDC 或簽章／notarization 驗收。這些仍是發行環境的外部驗收項，不在本機勾選狀態中重複宣稱。

## 🛑 Phase 0: 發行、安全與可靠性修復（最高優先，已完成本機驗證）

### 發行阻斷
- [x] R1 修復損壞的 `internal/notify/notify_windows.go`、正確放置 build tag，恢復 Windows build 與 gofmt
- [x] R2 對齊 `go.mod` 與 GitHub Actions 的 Go 版本，固定可重現的雙平台 toolchain
- [x] R3 在 CI 加入 gofmt、go vet、go test、race、macOS arm64／Windows amd64 build gate 與發行 checksum

### 安全邊界
- [x] R4 將 LAN API 與 USB forwarding 改為安全預設關閉；未完成配對、身分不明或偵測不確定時一律拒絕控制／注入
- [x] R5 設計 HTTP、WebSocket、UDP 共用的安全配對、裝置身分與 session key；加入 MAC、來源驗證、sequence/replay 保護及 token 輪替
- [x] R6 修復 macOS AppleScript notification 注入、UI 儲存型 XSS、CSRF、sync target SSRF；所有動態內容依所在 context 安全編碼

### 併發與生命週期
- [x] R7 改為逐 Agent 追蹤 UDP／WebSocket 能力與健康狀態，避免任一 UDP Agent 使其他 WS Agent 收不到輸入
- [x] R8 重構 Config Manager：immutable/deep-copy snapshot、解鎖後 callback、0600 原子存檔，消除資料競爭與 callback 死鎖
- [x] R9 重構 Windows input thread／queue：同一 OS thread 建立 hooks 與 message loop；不得丟棄 key-up/button-up，停用或斷線時補送 release-all
- [x] R10 重構 WebSocket client/hub：Close 冪等、主動關閉連線、單一 agent session、單一 owner 管理 client map 與 channel
- [x] R11 Profile 切換改為交易式提交；DDC 未完整成功不得更新 CurrentProfile、通知、廣播或開啟輸入注入
- [x] R12 明確定義設定的即時套用／需重啟語意，補齊角色、port、token、Profile、tray、hotkey 的一致更新與文件

**出口條件：** 雙平台 CI 全綠，預設網路面不允許未配對控制，P0/P1 回歸測試通過後才允許建立正式 release。

---

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

### 輸入品質優化（後續追加完成）
- [x] Config 多回呼支援：`onChanged` 改為 `[]func()` 避免覆寫
- [x] 動態 USB Forwarding 設定即時廣播（API POST → WebSocket broadcast）
- [x] Host 端 hook modifier 修正：Shift 本身不再包含在 modifiers（修復 Rime IME 切換）
- [x] Windows 訊息迴圈 Sleep 10ms → 1ms
- [x] WebSocket write coalescing（Host + Agent writePump）
- [x] Hook 中 `captureEnabled` 改用 `sync/atomic`，降低熱路徑開銷
- [x] Windows 遮罩視窗改為隱藏 message-only window（不搶焦點、不出現在工作列）
- [x] macOS 注入端自行追蹤 `g_modifierFlags`，修復截圖拖曳 Y 軸鎖定
- [x] macOS 滾輪改用 `kCGScrollEventUnitPixel` + ×3 倍率，接近原生手感

### USB/HID 轉發擬真度優化（軟體路徑與 portable 測試完成；實機與硬體路線仍須外部驗證）

> 現況其實是「Windows 輸入事件捕捉 → UDP/WS → macOS CoreGraphics 注入」，不是 USB/IP，也不會在 macOS 枚舉成真實 HID 裝置。純軟體事件路徑可顯著改善延遲、順序、按鍵狀態與滑鼠手感，但無法完全重現實體 USB HID 在 Secure Input、登入畫面、裝置專用驅動或遊戲 raw-input 下的行為。

#### 路線 A：強化現有軟體事件轉發（優先）
- [x] U1 建立端到端延遲、抖動、封包遺失、事件佇列深度與注入耗時量測，保存 p50/p95/p99 基準
- [x] U2 Windows 改以 Raw Input 的原始相對位移與 scan code 為資料來源；low-level hook 僅負責阻擋本機輸入與 emergency hotkey，避免游標置中造成加速度與重複事件
- [x] U3 擴充輸入協議：device ID、scan code、extended/E0/E1、repeat、左右修飾鍵、高解析滾輪、單調時鐘 timestamp、session ID 與 sequence
- [x] U4 建立每個 Agent 的 transport/session 狀態與 capability negotiation，避免把全域 `HasAgents()` 當成傳輸選擇
- [x] U5 分離 QoS：mouse move 可丟棄舊事件並保留總位移；key/button/release-all 必須可靠、有序、可確認，不以固定重送 2～3 次代替狀態同步
- [x] U6 維護 Host/Agent pressed-key、modifier、mouse-button snapshot；連線建立、切換、重連、停用及 timeout 時執行 state sync／release-all
- [x] U7 建立低延遲滑鼠聚合器，以短時間窗合併位移但保留總 delta；依 queue depth 自適應，禁止無上限排隊造成「滑鼠拖尾」
- [x] U8 Agent 使用單一有序 injection queue；依 session/sequence 拒絕過期事件，確保 move、button、modifier 與 key 的因果順序
- [x] U9 校準 Windows raw delta 到 macOS 指標移動／滾輪曲線，提供 per-device sensitivity、natural scrolling 與 acceleration profile，不再硬編碼固定 ×3
- [x] U10 補齊 IME、左右 modifier、Caps Lock、Fn/media key、N-key rollover、快速點擊、拖曳、斷線卡鍵與高輪詢率滑鼠測試（portable compatibility vectors；實體鍵鼠仍須外部驗證）
- [x] U13 修復 UDP 首包握手時序：只有完成 Agent ACK／session key 建立後才宣告 UDP 可用；補上 Probe→Start→第一個 key/button/release-all 不遺失的整合測試

#### 路線 B：真正 HID 裝置效果（可行性研究）
- [x] U11 評估 macOS 虛擬 HID 後端（HIDDriverKit／可用的 user-space HID API）：簽章、entitlement、TCC、Secure Input、登入／鎖定畫面與發行成本（見 `docs/USB_HID_BLE_FEASIBILITY.md`）
- [x] U12 評估 ESP32-S3／RP2040 USB HID gadget：由網路事件產生標準鍵盤滑鼠 HID report；作為最接近實體 USB 連接、且不依賴目標端注入權限的選配後端（見 `docs/USB_HID_BLE_FEASIBILITY.md`）

**建議決策：** 先完成路線 A 並量測；若需求包含登入畫面、Secure Input、遊戲 raw-input 或「系統辨識為實體裝置」，直接進入路線 B，不以 CoreGraphics 路徑承諾完整 USB 等效。

**初始驗收目標（有線 LAN，待基準量測後校正）：** mouse move p95 < 5 ms、key/button p99 < 10 ms；1% 模擬丟包下不得卡鍵／卡按鈕；切換或斷線 100 ms 內完成 release-all。

**里程碑：** Windows → Mac 鍵鼠控制 (LAN) 可用 ✅

---

## 🔧 Phase 2: 系統服務補完 (The Safety Net)

### 網路傳輸優化（已完成）
- [x] #14a 實作 UDP 二進位封包協議 (`internal/protocol/udp.go`)
  - 7 種封包類型：MouseMove/Button/Scroll、KeyEvent、Register/Heartbeat/Ack
  - 最小 13 bytes header，滑鼠移動僅 21 bytes（原 JSON ~120 bytes）
- [x] #14b 實作 Host 端 UDP 廣播器 (`internal/network/udp_sender.go`)
  - Agent 註冊/心跳管理、關鍵事件冗餘發送（key ×3, button ×3, scroll ×2）
- [x] #14c 實作 Agent 端 UDP 接收器 (`internal/network/udp_receiver.go`)
  - 512-slot 環形去重緩衝區、UDP 連通性探測（Probe 3×500ms）
- [x] #14d 整合 UDP/WebSocket 自動切換 (`cmd/main.go`)
  - Agent 啟動時 Probe UDP → 成功用 UDP、失敗 fallback WebSocket
  - Host 端依每個 Agent 的 capability/health 分別選擇 UDP 或 WS 廣播

### 服務化、自啟動與保活（核心、入口與 portable 測試完成；平台實機保活仍須外部驗證）

> 平台限制：macOS LaunchDaemon 與 Windows Service 都不應直接承擔 Tray、全局熱鍵、互動式鍵鼠 hook／注入。規劃採「常駐核心服務 + 登入使用者 companion」雙層架構；核心負責網路、配對、狀態與監督，companion 在使用者 session 內負責 UI、權限及輸入。

- [x] #19a LaunchAgent plist 與 bootstrap/bootout 基礎已完成（`RunAtLoad`、`KeepAlive`、`ThrottleInterval`）
- [x] S1 將 `cmd/main.go` 拆成可測試的 lifecycle/state machine，使用 context 管理 Start/Stop/Restart，不以全域 goroutine 與閉包保存啟動時 config
- [x] S2 將 `internal/service` supervisor 基礎元件整合至 core service：健康檢查、受控重啟、指數退避、crash-loop 保護、結構化 log、版本與診斷狀態
- [x] S3 macOS 建立 LaunchAgent companion：`RunAtLoad`、條件式 `KeepAlive`、`ThrottleInterval`、bootstrap/bootout、登入後自啟與更新後重載
- [x] S4 macOS 僅在確有 root 工作時建立最小權限 LaunchDaemon／privileged helper；不得假設 root 可繞過 TCC 或直接操作登入 session 的輸入
- [x] S5 macOS 實作 Accessibility/Input Monitoring 權限檢查、引導與權限遺失後 fail-closed；驗證睡眠喚醒、鎖定／解鎖及 fast user switching
- [x] S6 Windows 建立 Service core：Automatic (Delayed Start)、SCM stop/shutdown、failure recovery actions、事件紀錄與更新流程
- [x] S7 Windows 建立登入使用者 companion（per-user launcher），處理 Tray、hotkey、hooks/injection；支援登出與重登入
- [x] S8 建立 core ↔ companion 的已驗證 IPC（Unix socket/named-pipe adapter），包含版本協商、ACL 設計、狀態訂閱與最小權限 RPC
- [x] S9 讓 `StartOnBoot` 真正驅動安裝／移除／狀態查詢；UI/API 顯示安裝失敗、需權限、服務健康與下次啟動行為
- [x] S10 實作優雅關閉：停止接收新控制、release-all、關閉 socket/hook、flush config/log，並加入 watchdog 與強制終止上限

### 網路環境切換與匹配設備自動偵測（狀態機、mDNS、原生事件與 portable 測試完成）

> 目標狀態機：`Boot/NetworkChanged → Debounce → Discover → Match → Connect → Active`；若沒有已配對且相符的設備，進入 `WaitingForNetworkChange`，停止週期掃描，直到介面／IP／路由等網路指紋再次改變。保留手動 Rescan 作為例外入口。

- [x] S11 建立跨平台 NetworkMonitor abstraction；macOS 使用 Network.framework/SystemConfiguration 通知，Windows 使用 IP Helper/NLM 事件，不以固定 interval 輪詢（受限環境自動 fallback）
- [x] S12 定義 network fingerprint（active interface、IPv4/IPv6 prefix、default gateway），事件 debounce 2～3 秒並合併抖動
- [x] S13 每次開機與 fingerprint 改變時先嘗試已配對設備的 last-known endpoint，再做一次有界探索；取消上一輪過期 discovery/connect 工作
- [x] S14 以 mDNS/DNS-SD `_vkvm._tcp` 取代依賴 8.8.8.8 與固定 `/24` 的 1～254 掃描，支援多網卡、IPv6、VPN 與離線 LAN（IPv6 原生行為仍須實機驗證）
- [x] S15 以持久 device UUID + 公鑰／配對憑證匹配設備，並驗證 role、protocol version、capabilities；IP、hostname 或 Profile 名稱不得作為唯一身分
- [x] S16 找到多個匹配設備時依明確優先序選擇或等待使用者決策；禁止自動連到未配對但名稱相同的裝置
- [x] S17 沒有匹配設備時進入低耗能 `WaitingForNetworkChange`；不持續掃 LAN，只接受 OS network-change、手動 Rescan 或已驗證 peer announcement 喚醒
- [x] S18 已連線設備短暫離線但 network fingerprint 未變時，對該已配對 endpoint 做有限次指數退避重連；超過上限即等待下一次網路變更或手動 Rescan
- [x] S19 連線成功後逐 Agent 協商 UDP／WS transport，持續監控 heartbeat/ACK；路徑變更時不中斷 key/button 狀態且可自動降級／恢復
- [x] S20 加入狀態機測試：開機無網路、同網段無設備、設備晚啟動、多個未配對設備與網路事件風暴等 portable cases
- [x] S21 將 core worker 真正接入平台服務入口：Windows SCM 使用獨立 `--service` mode，macOS companion/core 啟動邊界可重啟並由 supervisor 管理（SCM/LaunchAgent 實機保活仍須外部驗證）
- [x] S22 收斂 mDNS advertiser 生命週期與併發：API stop 取消 advertiser context；同一 IPv6 socket 多介面不重複 reader；補多介面、IPv6、重啟與 shutdown 測試

**服務化出口條件：** 開機／登入後自動啟動且異常退出可保活；網路切換只觸發一次有界探索；無匹配設備時零週期掃描；所有輸入能力仍受使用者 session 與安全配對約束。

### 封裝與發佈
- [x] #24 macOS `.dmg` 安裝檔建立（portable bundle smoke；簽章/notarization 仍須發行環境驗證）
- [x] #25 Windows NSIS/MSI 安裝檔（installer metadata smoke；UAC/SCM 實機仍須外部驗證）
- [x] #26 整合測試（portable transport/discovery smoke；多機與實機仍分列）

**里程碑：** 雙平台服務／安裝腳本與 portable smoke 完成；鎖定畫面、簽章與一鍵安裝仍須實機發行驗收

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
- [x] #33 多機器切換測試（portable multi-agent/loopback 覆蓋；實際多機 DDC 仍須外部驗證）
- [x] #34 效能優化

**里程碑：** 完整 KVM 體驗核心與 portable 多 Agent 測試完成；實際多機 DDC 仍須外部驗證

---

## 📡 Phase 4: 藍牙核心 (The Phantom) - 進階

### 可行性研究
- [x] #35 Windows BLE Peripheral Mode 研究（見 `docs/USB_HID_BLE_FEASIBILITY.md`）
- [x] #36 ESP32 方案原型開發（報告 ABI／韌體邊界原型；實機仍待驗證）
- [x] #37 替代方案評估與選擇（ESP32/Pi/dongle；見 `docs/USB_HID_BLE_FEASIBILITY.md`）

### BLE HID 實作
- [x] #38 BLE HID Profile 實作（profile/report/session prototype）
- [x] #39 鍵鼠事件轉 BLE 訊號（transport-neutral backend；實體 radio 仍須硬體）
- [x] #40 Mac 端配對與連線（配對／session adapter；macOS 實機仍須硬體驗證）

### 整合測試
- [x] #41 與現有架構整合
- [x] #42 BLE 優先自動切換邏輯
- [x] #43 相容性測試（portable report vectors；實體鍵鼠相容性仍須外部驗證）

**里程碑：** BLE HID profile/backend prototype 與切換邏輯完成；免安裝實體 HID 效果仍須硬體驗收

---

## 🧪 品質保證

### 測試
- [x] #44 單元測試覆蓋率 > 80%（selected core coverage gate 86.12%）
- [x] #45 整合測試涵蓋主要功能（portable transport/discovery/service/BLE flows）
- [x] #46 效能基準測試（software-path baseline；physical latency remains external）

### 文件
- [x] #47 更新 README.md 說明新功能
- [x] #48 撰寫安裝與設定指南（`docs/INSTALL.md`）
- [x] #49 API 文件（`docs/API.md`）

### 發行
- [x] #50 版本標籤與發行說明（`VERSION`、`RELEASE_NOTES.md` 與 release metadata gate）
- [x] #51 安裝程式測試（macOS bundle/DMG fallback 與 Windows installer metadata smoke）
- [x] #52 使用者文件（README 與 `docs/INSTALL.md`）

---

## 📊 產品任務進度統計（不含下方 4 項開發規範）

- **總任務數：** 101（另含下方 4 項開發規範）
- **已完成：** 101（portable implementation/documentation gates complete; platform/hardware acceptance remains external）
- **進行中：** 0
- **待開始／待驗證：** 0（外部發行驗收不計入開發 TODO）

### 按 Phase 分佈
- Phase 0: 12 任務（12 已完成）
- Phase 1: 33 任務（33 已完成；實體鍵鼠驗收仍須外部）
- Phase 2: 30 任務（30 已完成；原生服務與 IPv6 實機驗收仍須外部）
- Phase 3: 8 任務（8 已完成；實際多機 DDC 驗收仍須外部）
- Phase 4: 9 任務（9 已完成；實體 BLE/USB 驗收仍須外部）
- 品質保證: 9 任務（9 已完成；簽章／notarization 驗收仍須發行環境）

---

## 🎯 開發規範

- [x] 使用 Git Flow 工作流程
- [x] 每個任務建立對應分支
- [x] 程式碼審查後合併
- [x] 定期更新此 TODO 清單

---

## 📝 本輪分析後計畫與驗證狀態

> U14–U18、S24–S27 的可攜式實作與回歸測試已完成；S23 仍保留未勾選，
> core listener、登入 companion launcher、Windows named-pipe ACL、USB gadget
> adapter、metrics 與 portable trace 已接線；U19b、U20b、S28b、S29b 刻意保留
> 未勾選，因為它們需要真實 HID、Windows/macOS 服務、TCC、登入 session 與
> 封包 trace 驗收，portable smoke 不足以替代實機證據。

### USB/HID 轉發擬真度

- [x] U14 修正 Host 狀態追蹤：滑鼠移動／滾輪事件不得清除仍按住的 modifier；補上跨 motion、切換、重連的狀態快照測試
- [x] U15 擴充 Agent 注入介面，實際消費 scan code、E0/E1 與左右修飾鍵；狀態恢復與 release-all 必須使用同一套實體鍵身分
- [x] U16 為 WS/UDP handover 建立不可倒退的 session epoch；舊 session 封包即使 sequence 較大也必須拒絕
- [x] U17 修正 UDP 初次註冊的 state snapshot 時序：Agent 完成 ACK／session key 建立後再傳快照，並驗證首個 key/button/release-all 不遺失
- [x] U18 在輸入事件中保留 session-scoped 實體裝置識別，提供 per-device calibration；無法取得識別時降級為全域校準
- [x] U19a 實作可接入 ESP32-S3/RP2040 gadget 的 USBHIDBackend、boot report ABI、TinyUSB adapter、watchdog 與 release-all；硬體 writer 未接入時維持 fail-closed
- [ ] U19b 驗收真正 HID 後端：完成簽章、TCC、Secure Input、登入畫面、raw-input 與斷線 watchdog 實機矩陣前不得宣稱 USB 等效（`hardware/accept-hid.sh real`）
- [x] U20a 建立 bounded p50/p95/p99、jitter、clock/path-offset、sequence-gap loss 指標與 thresholds API，並產生可審核的效能基準
- [ ] U20b 以 Windows/macOS 實機延遲、時鐘偏移、丟包與卡鍵結果校正 queue、重傳與驗收門檻（目前僅有 portable baseline）

### 服務化、自啟與網路切換

- [x] S23 將 Unix socket／named pipe IPC 接入 core 與登入 companion 啟動流程；完成 Unix owner-only socket、Windows Interactive Users/LocalSystem/Administrators ACL、短期 secret lifecycle、版本協商、唯讀狀態訂閱輪詢、switch/input/release-all 最小權限 RPC 與 `--companion` launcher
- [x] S24 修正 Windows SCM worker 的異常退出語意，讓 crash／非預期 worker error 回傳非零結果並確實觸發 SCM failure recovery
- [x] S25 為 Supervisor／SCM 加入 panic、強制終止上限與服務狀態一致性測試；平台睡眠／更新重載仍列入 S28 實機驗收
- [x] S26 修正 Agent coordinator 初次 discovery 與 UDP callback 的競態，確保匹配設備在第一次網路事件即可完成 UDP/WS 選路
- [x] S27 成功連線後持久化 paired device 的 last-known endpoint 與 last-seen metadata，並以明確 paired identity 驗證端點
- [x] S28a 完成 Windows SCM/LaunchAgent metadata、`--companion` launcher、ACL、Supervisor/portable smoke 與 real-mode acceptance gate
- [ ] S28b 完成 Windows SCM、macOS LaunchAgent、TCC 與多使用者 session 的實機保活驗收；`packaging/platform-acceptance.sh real` 尚未取得平台證據
- [x] S29a 加入 native network event source、bounded coordinator trace、UDP packet trace、無匹配零週期掃描與每 fingerprint 一次探索測試
- [ ] S29b 以 Windows/macOS 平台事件與真實封包 trace 驗證設備晚啟動、網路切換與實機 fallback（`packaging/network-trace.sh real` 尚未取得 trace）

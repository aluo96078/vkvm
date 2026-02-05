# Project Plan: vkvm (Hybrid Edition) v2.0

**專案名稱：** vkvm (Virtual KVM)
**核心價值：** Gaming First (遊戲安全) & User Friendly (大眾易用)
**產品定位：** 讓 Windows PC 變身為 Mac 的無線鍵盤滑鼠，支援藍牙原生模擬與 LAN 備援連線。

---

## 0. 與現有架構的關係

### 現有功能 (v1.x)
* **DDC/CI 螢幕輸入源切換器**：透過 Host/Agent WebSocket 協調多螢幕輸入源切換。
* 已實作：config 管理、WebSocket 通訊、mDNS 發現、系統托盤。

### v2.0 演進策略
本計劃為 **擴充 (Augment)** 而非取代：
1. **保留**：DDC/CI 切換核心 (用於自動切換螢幕至對應電腦)。
2. **新增**：鍵鼠轉發層 (Input Forwarding Layer)。
3. **整合**：切換 Profile 時同時執行「螢幕切換 + 鍵鼠控制權轉移」。

```
┌─────────────────────────────────────────┐
│           vkvm v2.0 架構               │
├─────────────────────────────────────────┤
│  [新增] 鍵鼠轉發層 (BLE / LAN)         │
├─────────────────────────────────────────┤
│  [保留] DDC/CI 螢幕切換層              │
├─────────────────────────────────────────┤
│  [保留] 通訊層 (WebSocket + mDNS)      │
└─────────────────────────────────────────┘
```

---

## 1. 使用者體驗流程 (User Journey)

### Mode A: 幽靈模式 (Phantom / Bluetooth) —— **首選推薦**

* **場景：** PC 具備藍牙功能。
* **流程：** Windows 開啟 -> Mac 藍牙配對 -> **完成**。
* **優勢：** Mac 端 **零安裝**，原生支援鎖定畫面與所有權限。

### Mode B: 服務模式 (Link / LAN) —— **備案 (已升級)**

* **場景：** PC 無藍牙或訊號不穩。
* **流程：**
1. 使用者在 Windows 開啟 `vkvm.exe`。
2. 在 Mac 下載並開啟 `vkvm Agent.app`。
3. **一鍵安裝：** 點擊 App 內的「安裝系統服務」按鈕 -> 輸入 Mac 登入密碼 (獲取 Root 權限)。
4. **權限引導：** App 彈窗指引用戶前往「隱私權設定」授權輔助使用權限。


* **優勢：** 安裝為系統服務 (Daemon) 後，**支援 Mac 鎖定/登入畫面操作**，且開機自動啟動。

---

## 2. 系統架構圖 (System Architecture)

本架構在 Mac 端採用 **「UI 控制端 + 背景服務端」** 分離設計，以解決權限與鎖定畫面問題。

```mermaid
graph TD
    subgraph "Windows Host (RTX 3090)"
        Input[實體鍵鼠] -->|USB直通| WinOS[Windows 11]
        WinOS -->|Work Mode| TrapWindow[vkvm 陷阱視窗]
        
        TrapWindow -->|Raw Input| Parser[位移解析]
        Parser -->|路徑選擇| Switch{具備藍牙?}
        
        Switch -->|Yes| BLE[虛擬藍牙 HID]
        Switch -->|No| NetSend[網路發送端]
    end

    subgraph "Mac Agent (Dual Architecture)"
        BLE -.->|Bluetooth| MacOS_BT[macOS 藍牙堆疊]
        
        NetSend -.->|LAN/WiFi| Daemon[vkvm-daemon (Root)]
        
        subgraph "User Space"
            UI[vkvm Agent UI] -->|管理指令| Daemon
        end
        
        subgraph "System Space (LaunchDaemon)"
            Daemon -->|kCGHIDEventTap| MacOS_Input[macOS 輸入處理]
        end
    end

```

---

## 3. 軟體功能模組 (Software Modules)

### 3.1 Windows Host (主控端)

* **語言：** Go + C/C++ (CGO 或 DLL)
* **核心功能：**

#### A. Focus Trap (Raw Input) —— 滑鼠捕獲
* 使用 `RegisterRawInputDevices` + `WM_INPUT` 讀取滑鼠 Delta 值。
* 呼叫 `ClipCursor(&rect)` 將系統游標限制在視窗內。
* 隱藏系統游標 (`SetCursor(NULL)`)，自繪虛擬游標或無游標。
* **邊界處理：** 當虛擬游標到達邊界時，使用 `SetCursorPos()` 將實際游標拉回中心，確保無限滑動。

#### B. 輸出路徑選擇
* **BLE HID (優先)：** 透過藍牙廣播標準鍵鼠訊號。
* **LAN Fallback：** 透過 WebSocket 發送至 Mac Agent。

#### C. Kill Switch
* `Ctrl+Alt+Esc` 強制釋放滑鼠，還原 `ClipCursor(NULL)`。



### 3.2 Mac Agent (被控端 - 重大更新)

為了支援鎖定畫面，Mac Agent 採用 **自我分裂 (Self-Replication)** 架構。

* **單一 Binary，兩種型態：**
* 程式入口 `main()` 檢查啟動參數 `-server`。



#### A. UI 模式 (User Mode)

* **職責：**
* 提供圖形介面 (Status / Settings)。
* **安裝服務 (Installer Logic):**
1. 將自身 binary 複製到 `/usr/local/bin/vkvm-agent` (固定路徑以穩定 TCC 權限)。
2. 生成 `.plist` 設定檔。
3. 使用 AppleScript (`do shell script ... with administrator privileges`) 呼叫 `launchctl` 註冊為 **LaunchDaemon**。


* **權限引導：** 偵測 Daemon 是否有權限移動滑鼠，若無則跳出圖文教學。



#### B. 服務模式 (Daemon Mode)

* **啟動參數：** `-server`
* **執行身份：** Root (系統管理員)
* **生命週期：** 開機即啟動 (LaunchDaemon)，不受使用者登出影響。
* **職責：**
* 監聽 TCP/UDP Port。
* **模擬輸入 (Root Level):** 使用 `CGEventPost(kCGHIDEventTap, ...)`。注意必須使用 `HID Event Tap` 才能注入到登入畫面。

#### C. UI ↔ Daemon IPC 機制

* **通道：** Unix Domain Socket `/var/run/vkvm.sock`
* **協議：** JSON-RPC 2.0
* **支援指令：**
  | Method | 說明 |
  |--------|------|
  | `status` | 取得 Daemon 狀態 (連線數、延遲) |
  | `config.get` | 讀取當前設定 |
  | `config.set` | 更新設定 (需重啟生效的項目除外) |
  | `restart` | 重啟 Daemon |
  | `permissions.check` | 檢查輔助使用權限狀態 |



---

## 4. 開發路線圖 (Roadmap)

> **策略調整：** 先完成 LAN 模式作為 MVP，確保核心功能可用，再攻克技術難度較高的藍牙方案。

### Phase 1: 陷阱與輸入 (The Trap) —— *MVP 優先*

* [ ] Windows 實作透明視窗 + Raw Input 讀取。
* [ ] 實作 `ClipCursor` + `SetCursorPos` 無限滑動。
* [ ] 定義鍵鼠事件 Protocol (擴充現有 `protocol.go`)。
* [ ] 串接現有 WebSocket 通道發送鍵鼠事件至 Mac。
* [ ] **里程碑：** Windows → Mac 鍵鼠控制 (LAN) 可用。

### Phase 2: 系統服務補完 (The Safety Net)

* [ ] **Mac Agent 架構重構：** 實作 UI 與 Daemon 分離邏輯 (`-server` flag)。
* [ ] **安裝腳本：** 實作 Go 呼叫 AppleScript 進行 `cp` 與 `launchctl load`。
* [ ] **IPC 實作：** Unix Socket + JSON-RPC。
* [ ] **TCC 驗證：** 測試 `/usr/local/bin` 路徑下的權限持久性。
* [ ] 封裝 .dmg 安裝檔。
* [ ] **里程碑：** Mac 鎖定畫面可被控制。

### Phase 3: DDC/CI 整合 (The Fusion)

* [ ] 整合 Profile 切換：鍵鼠控制權 + 螢幕輸入源同步切換。
* [ ] 實作「跟隨焦點」模式：根據鍵鼠控制目標自動切換螢幕。
* [ ] **里程碑：** 完整 KVM 體驗 (螢幕 + 鍵鼠 一鍵切換)。

### Phase 4: 藍牙核心 (The Phantom) —— *進階*

* [ ] 研究 Windows BLE Peripheral Mode 可行性 (見 5.4 節)。
* [ ] 若原生不可行，評估替代方案 (ESP32 dongle / Pi Zero)。
* [ ] 實作 BLE HID Profile (Keyboard + Mouse)。
* [ ] 驗證 Mac 可搜尋並連線。
* [ ] **里程碑：** Mac 免安裝軟體即可被控制。

---

## 5. 技術細節與注意事項

### 5.1 Mac 權限路徑陷阱

* **問題：** macOS 的輔助使用權限 (Accessibility) 是綁定「檔案路徑」的。若使用者從 Downloads 資料夾執行並授權，移到 Applications 後權限會失效。
* **解法：** 安裝腳本必須強制將 binary 複製到系統固定目錄 (如 `/usr/local/bin/`)，並讓 plist 指向該處。UI 引導使用者授權時，也必須指向該路徑。

### 5.2 登入畫面輸入

* 在 Daemon 模式下呼叫 `CoreGraphics` 時，務必使用 **`kCGHIDEventTap`** 而非 `kCGSessionEventTap`。前者是在 HID 硬體層模擬，能穿透 Login Window；後者僅限於當前使用者 Session。

### 5.3 網路發現 (mDNS)

* 由於 Daemon 以 Root 執行，防火牆權限通常較寬鬆，但仍建議實作 mDNS (Bonjour) 讓 Windows 能自動找到這個背景服務的 IP。
* 服務類型：`_vkvm._tcp.local`

### 5.4 藍牙 HID 技術挑戰

> ⚠️ **重要：** Windows 藍牙原生 API 限制

* **問題：** Windows BLE API (`Windows.Devices.Bluetooth`) **不支援** Peripheral (週邊) 模式。Windows 只能作為 Central (主控端) 連接其他裝置，無法直接模擬成藍牙鍵盤/滑鼠。
* **可行方案：**

| 方案 | 優點 | 缺點 |
|------|------|------|
| **A. 特殊藍牙 Dongle** (如 CSR8510 + 自訂韌體) | 軟體層面可控 | 需特定硬體，相容性風險 |
| **B. ESP32 外接模組** | 成熟方案 (已有開源專案)，成本低 (~$5) | 需額外硬體，增加複雜度 |
| **C. Raspberry Pi Zero W** | 可作為 USB Gadget + BLE HID | 成本較高，體積較大 |
| **D. 虛擬藍牙驅動** (如 Nefarius ViGEm 系列) | 純軟體 | 僅限本機，無法真正廣播 |

* **建議：** Phase 4 開始前進行 PoC 驗證。若無合適純軟體方案，考慮提供 ESP32 韌體 + 刷機指南作為「進階模式」。

### 5.5 安全性

#### 網路層
* Daemon 預設**僅監聽私有網段** (192.168.x.x, 10.x.x.x, 172.16-31.x.x)。
* 連線需攜帶 API Token (複用現有 `protocol.AuthPayload`)。
* 建議未來支援 mTLS (Phase 5)。

#### 本機 IPC
* Unix Socket 權限設為 `0600`，僅 owner 可存取。
* UI 以當前使用者執行，透過 Socket 與 Root Daemon 通訊，避免 UI 本身需要 Root。

#### 輸入注入
* Daemon 啟動時驗證呼叫來源 (檢查 `launchd` 父程序)。
* 可選：限制只接受來自已配對 Host 的指令。

### 5.6 鍵鼠事件 Protocol 擴充

擴充現有 `internal/protocol/protocol.go`：

```go
// TypeInput 鍵鼠事件
TypeInput MessageType = "input"

// InputPayload 鍵鼠事件資料
type InputPayload struct {
    Type      string  `json:"type"`       // "mouse_move", "mouse_btn", "key"
    DeltaX    int     `json:"dx,omitempty"`
    DeltaY    int     `json:"dy,omitempty"`
    Button    int     `json:"btn,omitempty"`   // 1=left, 2=right, 3=middle
    Pressed   bool    `json:"pressed,omitempty"`
    KeyCode   uint16  `json:"keycode,omitempty"`
    Modifiers uint16  `json:"modifiers,omitempty"`
    Timestamp int64   `json:"ts"`         // Unix ms, 用於延遲補償
}
```

---

## 6. 風險評估與緩解

| 風險 | 機率 | 影響 | 緩解策略 |
|------|------|------|----------|
| Windows BLE Peripheral 無純軟體方案 | 高 | 高 | 調整 Phase 順序，LAN 模式優先；提供 ESP32 方案作為替代 |
| macOS 更新導致 TCC 行為改變 | 中 | 高 | 持續關注 Apple 開發者文件；設計易於更新的安裝流程 |
| 遊戲反作弊偵測 Raw Input Hook | 低 | 中 | vkvm 僅讀取輸入不注入，風險較低；提供白名單機制 |
| 網路延遲影響操作體驗 | 中 | 中 | 實作延遲統計與警告；未來考慮 UDP + 預測補償 |
| LaunchDaemon 權限被 Apple 收緊 | 低 | 高 | 備案：改用 Privileged Helper Tool (SMJobBless) |

---

## 7. 附錄

### A. LaunchDaemon plist 範例

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.vkvm.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/vkvm-agent</string>
        <string>-server</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/vkvm-agent.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/vkvm-agent.log</string>
</dict>
</plist>
```

### B. 專案結構變更 (預計)

```
internal/
├── input/                  # [新增] 鍵鼠輸入處理
│   ├── trap_windows.go     # Windows Raw Input + ClipCursor
│   ├── trap_stub.go        # 非 Windows 平台 stub
│   ├── inject_darwin.go    # macOS CGEventPost
│   └── inject_stub.go      # 非 macOS 平台 stub
├── daemon/                 # [新增] Daemon 模式邏輯
│   ├── daemon.go           # 主邏輯
│   ├── ipc.go              # Unix Socket JSON-RPC
│   └── install.go          # 安裝/解除安裝腳本
├── protocol/
│   └── protocol.go         # [擴充] 新增 InputPayload
└── ...
```

### C. 參考資料

* [Apple: Posting Events Programmatically](https://developer.apple.com/documentation/coregraphics/1456527-cgeventpost)
* [Microsoft: Raw Input](https://docs.microsoft.com/en-us/windows/win32/inputdev/raw-input)
* [ESP32 BLE HID](https://github.com/T-vK/ESP32-BLE-Keyboard)
* [Barrier (開源 KVM 軟體)](https://github.com/debauchee/barrier)
# VKVM - 虛擬 KVM 切換器

一款跨平台的 DDC/CI 螢幕輸入切換器，讓你像使用硬體 KVM 一樣切換多台電腦的螢幕。

## 開發動機

市面上支援 2 台以上電腦 + 2 台以上螢幕的硬體 KVM Switch 價格過於高昂。VKVM 提供一個免費的軟體替代方案，透過 DDC/CI 直接控制螢幕輸入源，省去購買昂貴多螢幕 KVM 硬體的需要。

## 測試環境

| 元件 | 規格 |
|------|------|
| **macOS** | M4 Pro MacBook Pro (1× 原生 HDMI + 1× Type-C Hub 轉接) |
| **Windows** | RTX 3090 GPU PC |
| **螢幕** | ASUS VG27AQL3A-W × 2 |

> ⚠️ **重要**：您的螢幕必須在 OSD 選單中開啟 DDC/CI 功能，VKVM 才能正常運作。

## 功能特色

- 🖥️ **DDC/CI 螢幕控制** - 無需硬體 KVM，直接透過軟體切換螢幕輸入源
- ⌨️ **全局熱鍵** - 支援鍵盤快捷鍵與滑鼠按鍵組合（如 `Mouse2+Mouse3`）
- 🌐 **網路切換** - 透過區域網路的 Host/Agent 架構控制多台電腦
- 🔄 **跨平台熱鍵映射** - `Ctrl+X` 熱鍵在 macOS 上自動對應 `Cmd+X`
- 💤 **自動喚醒** - 切換前模擬滑鼠移動以喚醒休眠的螢幕

## 必要條件

### macOS
- **輔助使用權限**：全局熱鍵偵測必須
  - 前往 `系統設定` → `隱私權與安全性` → `輔助使用`
  - 將 `vkvm` 加入允許的應用程式清單
- **DDC 支援**：大多數外接螢幕透過 DDC/CI 協定運作

### Windows
- **ControlMyMonitor**：已自動內建（無需另外安裝）
- **管理員權限**：建立防火牆規則時可能需要
- **DDC/CI 啟用**：在螢幕的 OSD 選單中開啟此功能

## 安裝方式

### 從原始碼編譯
```bash
# 複製專案
git clone https://github.com/aluo96078/vkvm.git
cd vkvm

# 編譯當前平台
go build -o vkvm ./cmd

# 編譯 Windows 版本（無命令提示字元視窗）
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o vkvm.exe ./cmd

# 編譯 macOS 版本
GOOS=darwin GOARCH=arm64 go build -o vkvm ./cmd
```

## 使用方法

### 基本用法
```bash
# 啟動服務（在系統托盤運行）
./vkvm

# 開啟設定介面
./vkvm -ui
```

### 系統托盤使用

VKVM 會在系統托盤運行（macOS 為選單列，Windows 為工作列通知區域）。右鍵點擊托盤圖示即可開啟選單。

#### macOS

![macOS 系統托盤選單](./docs/mac/taskbar.png)

選單列圖示提供快速存取：
- **切換 Profile** - 點擊任一 Profile 名稱立即切換
- **Settings...** - 開啟設定介面
- **Quit** - 結束 VKVM

#### Windows

![Windows 系統托盤選單](./docs/windows/taskbar.png)

右鍵點擊系統托盤圖示可：
- **切換 Profile** - 選擇任一 Profile 進行切換
- **Settings...** - 開啟設定介面
- **Quit** - 結束 VKVM

> 💡 **提示**：在 Windows 上，如果 VKVM 圖示被隱藏在溢出區域，您可能需要點擊工作列中的「^」箭頭來找到它。

### 設定說明

1. **開啟設定**：點擊托盤圖示 → "Settings..."
2. **新增 Profile**：為每台電腦建立 Profile（如「PC1」、「Mac」）
3. **設定熱鍵**：點擊「🔴 Record」錄製鍵盤/滑鼠組合
4. **設定螢幕**：為每個 Profile 選擇各螢幕的輸入源

### 網路設定（多電腦）

1. **Host 主機**（主控端）：
   - 設定角色：`Host`
   - 啟用 API 伺服器

2. **Agent 機器**（被控端）：
   - 設定角色：`Agent`
   - 在「Coordinator Address」輸入 Host 的 IP:Port
   
Agent 會自動從 Host 同步所有 Profile 設定。

## 熱鍵範例

| 熱鍵 | 說明 |
|------|------|
| `Ctrl+Shift+F1` | 切換到 Profile 1 |
| `Mouse2+Mouse3` | 滑鼠中鍵 + 右鍵 |
| `Ctrl+Alt+1` | 標準修飾鍵組合 |

> **注意**：在 macOS 上，`Ctrl+X` 熱鍵同時也會響應 `Cmd+X`

## 架構圖

```
┌─────────────────┐         ┌─────────────────┐
│   Host (Win)    │◄───────►│   Agent (Mac)   │
│   DDC + API     │  HTTP   │   DDC + API     │
└─────────────────┘         └─────────────────┘
        │                           │
        ▼                           ▼
   ┌─────────┐                 ┌─────────┐
   │  螢幕   │                 │  螢幕   │
   └─────────┘                 └─────────┘
```

## 疑難排解

### macOS：熱鍵無法運作
- 在系統設定中授予輔助使用權限
- 授權後重新啟動應用程式

### Windows：DDC 指令失敗
- 在螢幕 OSD 選單中啟用 DDC/CI
- 嘗試以管理員身分執行
- 確認 ControlMyMonitor 是否能手動操作

### 網路：Agent 無法連線到 Host
- 確認 Host 的 API 伺服器已啟用
- 檢查 Host 的防火牆設定（預設 port 18080）
- 確保兩台機器在同一網路

### DDC 在 USB-C/Thunderbolt 轉接線下無法運作
> ⚠️ **重要提醒**：許多 USB-C/Thunderbolt 轉 HDMI/DisplayPort 的轉接線**不支援 DDC/CI 穿透**，這是硬體限制。

- 建議改用原生 HDMI 或 DisplayPort 線直接連接
- 如果必須使用轉接線，請選擇明確標示支援 DDC/CI 的產品
- 部分擴充底座 (Docking Station) 也會阻檯 DDC 信號

**解決方案**：如果您的轉接線不支援 DDC，您仍可以透過 **Host/Agent 模式** 使用 VKVM。將具有 DDC 支援連接的電腦設定為 **Host**，將使用不支援 DDC 轉接線的電腦設定為 **Agent**。當您觸發切換時，Host 會向螢幕發送 DDC 指令，Agent 則會收到網路通知。

## 致謝 / 開源套件

VKVM 依賴以下優秀的開源工具進行 DDC/CI 通訊：

| 平台 | 工具 | 作者 | 授權 |
|------|------|------|------|
| **macOS** | [m1ddc](https://github.com/waydabber/m1ddc) | @waydabber | MIT |
| **Windows** | [ControlMyMonitor](https://www.nirsoft.net/utils/control_my_monitor.html) | NirSoft | 免費軟體 |

> **m1ddc** - 用於透過 DDC/CI 控制 Apple Silicon Mac 顯示器的命令列工具。
>
> **ControlMyMonitor** - 一款 Windows 工具，可透過 DDC/CI 協定檢視和修改螢幕設定。

感謝這些工具的作者，沒有他們 VKVM 不可能實現！

## 授權條款

MIT License

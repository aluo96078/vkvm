# VKVM 驗證型稽核報告（基線紀錄）

> 本文件保留 2026-07-10 的修復前基線，不代表目前工作樹的最終狀態。現行修復與仍待驗證項目請以 [TODO.md](TODO.md) 及本輪測試結果為準；基線中列出的問題不應直接視為仍未修復。

稽核日期：2026-07-10
範圍：目前 develop_v2 工作樹（HEAD 6903b28，包含既有未提交變更）
結論：不建議在修正 P0 項目前發行或在不受信任 LAN 使用。

## 執行方式與限制

本次沒有修改產品程式碼，也沒有操作真實螢幕、鍵鼠、系統設定或網路上的其他主機。驗證包含：

- macOS/arm64 的 build、go test、go test -race、go vet、coverage 與 gofmt；
- Windows 與 Linux 的交叉編譯；
- 在 /private/tmp 的隔離副本加入一次性測試，驗證設定鎖定、Profile 判定、WebSocket 與 UDP 行為；這些測試不在本專案工作樹中；
- 原始碼路徑與資料流檢查。

無法在目前環境完成的項目會標註為「需 Windows/實機驗證」，不把它們當作已實機重現。

## 檢查結果摘要

| 檢查 | 結果 | 意義 |
| --- | --- | --- |
| go build ./cmd（macOS/arm64） | 通過 | 僅代表目前 macOS target 可建置 |
| go test ./...、go test -race ./... | 通過 | 只有 internal/input/input_test.go，且 coverage 全套件皆為 0.0% |
| go vet ./... | 通過 | 未涵蓋 Windows target 與執行期設計錯誤 |
| Windows 交叉編譯 | 失敗 | internal/notify/notify_windows.go 有語法錯誤 |
| Linux 交叉編譯 | 失敗 | DDC 與 systray 沒有 Linux 實作；若產品範圍僅 macOS/Windows，這是範圍限制而非 blocker |
| gofmt -l . | 失敗 | internal/ddc/windows.go、internal/osutils/osutils_windows.go 未格式化；notify Windows 檔無法解析 |

目前工作樹不是乾淨狀態。Windows 編譯 blocker 來自未追蹤的 internal/notify/ 目錄，因此不應推論它一定存在於 HEAD；但它會阻斷目前工作樹和目前 CI 的 Windows 發行。

## 已由隔離測試重現的問題

下列一次性測試以「問題確實發生」作為通過條件，因此 PASS 代表缺陷被重現，而非行為正確。

| ID | 驗證結果 | 證據 |
| --- | --- | --- |
| V-01 | Manager.Get 回傳可直接修改的內部指標 | 外部修改 CurrentProfile 後，未呼叫 Set 仍改變 Manager 狀態 |
| V-02 | Load 在 callback 內呼叫 Get 會死鎖 | Load 持有 mutex 執行 callback，200ms 內未返回 |
| V-03 | 不存在任何 Profile 螢幕時仍判為 Profile 命中 | 空 monitor list 對含 missing-monitor 的 Profile 回傳該 Profile |
| V-04 | WSClient 有 token 但 Upgrade request 沒有 Authorization | 測試伺服器收到空 Authorization header |
| V-05 | UDPReceiver 接受非 Host 來源的鍵盤封包 | 任意 loopback UDP socket 的 KeyEvent 被 dispatch |
| V-06 | WSClient.Close 非冪等，第二次呼叫 panic | close(done) 被重複執行 |
| V-07 | WSClient.Close 不會關閉活躍 websocket | Close 後 readPump 仍阻塞，IsConnected 保持 true |
| V-08 | 空白 Server token 允許遠端 POST | authMiddleware 對非 health 路徑直接放行 |

## P0：發行或安全 blocker

### P0-1：目前 Windows 發行無法建置

internal/notify/notify_windows.go:36 將整段 Go 程式壓成無效內容，Windows build 回報 syntax error。該檔的 build constraint 也放在 package 宣告之後，格式不正確。

此外 go.mod:3 要求 Go 1.25.0，.github/workflows/release.yml:26 與 :49 卻固定 Go 1.23。因 Go toolchain auto 可能嘗試下載新版本，未在本機以 Go 1.23 完整重現；但 CI 設定與 module 要求不一致，會降低發行可重現性，甚至在禁用自動 toolchain 下載時直接失敗。

修正優先序：先恢復合法的 Windows 通知實作，將 CI Go 版本對齊 go.mod，並在 CI 加入 gofmt -d、go vet、macOS/Windows build。

### P0-2：外部 LAN 控制預設無驗證，且三種傳輸層沒有一致安全模型

新設定預設 APIEnabled=true、APIToken=""（internal/config/config.go:141），API 監聽 0.0.0.0:port（internal/api/server.go:72），只有 token 非空時才檢查 Authorization（:131）。

結果是同一 LAN 的任意主機可在預設設定下：

- GET /api/config 取得完整設定，包含 api_token；
- POST /api/config 覆寫角色、Profile、輸入轉發與 token；
- POST /api/switch 切換螢幕；
- 連上 WebSocket 請求同步或發送 switch；
- 註冊 UDP endpoint 或直接向 Agent 注入鍵鼠封包。

即使使用者設定 token，WSClient.connect 使用 websocket.DefaultDialer.Dial(..., nil)（internal/network/ws_client.go:68），不送 Authorization；伺服器 TypeAuth 也只是 TODO（internal/api/websocket.go:210）。因此 token 模式目前傾向於「HTTP 保護了、Agent 連不上」，而不是安全配對。Server.token 只在 Start 時讀取一次（internal/api/server.go:57），設定後更新 token 也不會立即生效。

所有傳輸皆是 HTTP/WS/UDP 明文，UDP 沒有 HMAC、session id、來源比對或 replay 時窗。這對會注入鍵盤滑鼠的產品是高風險。

修正優先序：

1. 預設關閉 LAN API，或預設只綁定 loopback；
2. 啟用 LAN 時強制安全配對與非空高熵 token；
3. 對 HTTP、WebSocket 與 UDP 使用同一個經驗證的 session key；UDP 至少加入 MAC、來源驗證、sequence/replay 保護；
4. 使用 TLS 或明確限制到可信私人網路；不得把 token 以未驗證 GET 回傳。

### P0-3：未跳脫的 Profile 名稱可造成 macOS AppleScript 注入；UI 同時有儲存型 XSS

notify_darwin.go:11 以字串串接方式建立 osascript 程式。Profile 名稱可由設定/API 提供，若包含雙引號與換行，可結束 notification 字串並插入額外 AppleScript statement。由於預設 LAN API 無驗證，攻擊鏈可為：遠端 POST 設定 Profile 名稱，再觸發切換，最後以目前登入使用者身分執行 AppleScript。

UI 也把 profile.name、hotkey、monitor id/name、discovery host 欄位直接插入 innerHTML 與 inline onclick，例如 internal/ui/server.go:715、:723、:757、:853。未跳脫的資料可形成 DOM/JavaScript 注入。這與未驗證的設定 API 結合後，是可持久化到本機設定 UI 的 XSS。

UI 的 /api/sync-to 還會將完整本機設定 POST 到 query string 指定的 addr（internal/ui/server.go:231），而該 handler 沒有限制 method；設定中可能含 api_token。這增加了 XSS/CSRF 後的資料外送風險。

修正優先序：所有 UI 動態內容改用 textContent/DOM API 或嚴格 HTML/attribute/JavaScript context escaping；移除 inline handler；限制所有 state-changing UI endpoint 為 POST 並加入 CSRF token；對 sync target 使用嚴格 host/IP allowlist；通知資料使用安全的 AppleScript string encoder，不可拼接程式碼。

### P0-4：UDP/WS fallback 以「任一 UDP Agent」為開關，混合網路環境會遺失 Agent 輸入

Host 在 cmd/main.go:437 以 udpSender.HasAgents() 決定是否只走 UDP；否則才廣播 WebSocket。HasAgents 只是全域 bool（internal/network/udp_sender.go:193），不是每個 Agent 的傳輸能力。

因此只要 Agent A 成功 UDP Probe，Agent B 即使 UDP 被防火牆阻擋並正確設定為 WebSocket fallback，Host 仍只向 UDP Agents 發送，B 完全收不到輸入。Probe 使用一個臨時 port，真正 receiver 又開新 port（internal/network/udp_receiver.go:72、:120），兩者之間還可能短暫把輸入送往已關閉的 probe port。

UDP 路徑在執行中失效時也沒有健康檢查後自動恢復 WebSocket；Agent 在 Probe 成功後把 wsClient.OnInput 設為 nil（cmd/main.go:395）。

修正優先序：追蹤每個 Agent 的 transport 與健康狀態，分別送 UDP/WS；或在過渡期同時廣播兩種 transport 並在 Agent 去重。需有連續 heartbeat/ACK timeout 後的自動降級與恢復機制。

## P1：會造成錯誤控制、死鎖、崩潰或資料不一致

### P1-1：設定鎖定與資料所有權設計不安全

Manager.Get（internal/config/config.go:254）與 GetProfile（:289）在解鎖後回傳內部可變指標。Switcher 隨後直接寫入 cfg.General.CurrentProfile（internal/switcher/switcher.go:179），API/UI goroutine 可同時序列化或替換同一份設定，形成 race。

Load（config.go:210）持有 mutex 時執行 callback；API callback 會再次呼叫 configMgr.Get（internal/api/server.go:38）。隔離測試已重現此死鎖。

此外 Save 直接以 0644 寫入包含 api_token 的 config 檔（config.go:251），並建立 0755 設定目錄。在多使用者或家目錄 ACL 較寬的環境，token 的檔案權限不符合秘密資料。

修正優先序：改用 RWMutex；所有 Get 回傳 deep copy 或 immutable snapshot；所有寫入透過受鎖方法；在解鎖後才執行 callbacks；使用 0600 config 權限與原子檔案替換。

### P1-2：輸入安全閘門不是 fail-closed

USBForwardingEnabled 預設 true（config.go:146）。Host 只要啟動便開始擷取與轉送事件（cmd/main.go:410、:432）；InputCaptureEnabled 只影響是否阻擋本地輸入，不影響是否轉送。

Agent 未設定 AgentProfile 時直接 allowInjection=true（cmd/main.go:328）。也就是初始或不完整設定可同時保留本機輸入並把它注入遠端。

Profile 偵測會跳過找不到的螢幕（switcher.go:269），導致沒有任何可驗證螢幕時仍回傳第一個有 MonitorInputs 的 Profile；隔離測試已重現。這會把「偵測失敗」誤當成「可注入」。

修正優先序：USB 轉發預設 false；必須明確選定且成功驗證 AgentProfile 才可注入；任何 DDC/偵測錯誤或零個比對皆應關閉注入。

### P1-3：輸入可靠性與 Windows hook 生命週期存在風險

Windows Trap 在 Start 的呼叫 OS thread 建立視窗、註冊 hotkey、安裝 hook，之後才以另一個 Go goroutine 跑 messageLoop（internal/input/trap_windows.go:224、:250），沒有 runtime.LockOSThread。Win32 window queue 和 low-level hook callback 依 thread 工作，這是高信心的設計錯誤，但仍需 Windows 實機驗證實際症狀。

events channel 滿時會丟棄所有事件，包含 key-up/button-up（trap_windows.go:850、:913），可能讓遠端卡住按鍵或拖曳。Stop 關閉 channel（:284）時，正在執行的 hook callback 仍可能 send，存在 send-on-closed-channel panic 窗口。

初始 Host 啟動 inputTrap.Start 失敗時沒有 else log（cmd/main.go:432），使用者會看不到失敗原因。keyboardHookProc 的註解稱已保留 RegisterHotKey，但 capture mode 仍無條件 return 1（trap_windows.go:919），緊急熱鍵是否能穿透需實機驗證。

修正優先序：單一 LockOSThread 同步建立視窗、hooks 和 message loop；使用可停止且不關閉 producer channel 的 queue；鍵盤/按鈕事件必須可靠投遞或在斷線/停用時合成 release；加入 Windows integration test 與硬體 kill-switch test。

### P1-4：WebSocket 生命週期與 Hub 併發不安全

隔離測試確認 WSClient.Close 重複呼叫會 panic，且不會關閉活躍 conn，readPump 會卡住（network/ws_client.go:261）。

WSManager.broadcastMessage 在 RLock 持有期間 close client.send 並 delete map entry（api/websocket.go:88），違反 lock 的讀寫語意。readPump 同時可能在 sync response 路徑寫入 c.send（:253），使 close/send 競態可造成 panic。broadcast、register、unregister 又都是無緩衝 channel，server 尚未啟動或已停止時 callback 可能阻塞。

Agent 模式還會建立兩個 WebSocket client：Switcher.New 建一個（switcher.go:45），runService 再建一個（cmd/main.go:334）。這會產生重複連線、同步請求與難以推理的生命週期。

修正優先序：以 context/once 管理 Close；Close 時主動關閉 conn；只保留一個 agent session；由單一 Hub goroutine 擁有 clients map 和 channel 關閉權。

### P1-5：DDC 失敗仍可能宣告切換成功並廣播遠端狀態

Switcher 忽略 ListMonitors error（switcher.go:148）；找不到的 monitor 只 log 後跳過。接著仍保存 CurrentProfile、發通知、執行 onSwitch 廣播（:179、:190、:193），最後才回傳 lastErr。

因此本機顯示器未切換時，遠端 Agent 可能已切到新控制狀態，產生螢幕與鍵鼠控制分裂。ShowNotifications 設定也沒有被檢查，通知一律執行。

修正優先序：把 monitor discovery/critical DDC error 視為 transaction failure；只有成功條件完整滿足才提交 CurrentProfile、通知及廣播；尊重 ShowNotifications。

## P2：功能落差、可維護性與產品說明問題

### P2-1：設定即時套用與同步 UI 不一致

Role、CoordinatorAddr、APIPort、APIEnabled 等只在 runService 啟動時讀取。UI 可以儲存它們，但不會重啟 API、UDP、WebSocket 或 input role；閉包還保留啟動時的 cfg 指標（cmd/main.go:170、:192、:294）。

SyncProfiles 只是非同步送出請求後立刻回傳（switcher.go:200）；main 隨即 refreshShortcuts（cmd/main.go:619），早於 sync response。UpdateProfilesFromRemote 也不呼叫設定 callback（config.go:379），tray menu 則只依初始 cfg 建立一次（cmd/main.go:639）。因此同步後的 Profile/快捷鍵/托盤項目可能延遲或永久不一致。

### P2-2：多個設定欄位或宣稱的功能沒有實作

StartOnBoot、StartMinimized、ThisComputerIP 在程式中只有設定結構/UI，沒有服務行為。ShowNotifications 有設定但沒有被 switcher 使用。Windows autostart 明確是 stub（internal/autostart/autostart.go:123）；macOS 僅寫 plist，沒有 load/bootstrap 流程。

TODO.md:80 將「Mac 鎖定畫面可被控制、雙平台一鍵安裝」標為完成，但同一檔案 :59-78 仍列出 LaunchDaemon、daemon、IPC、Windows Service、TCC、封裝與整合測試皆未完成。Phase 4 的 BLE milestone 也在未完成任務旁標為完成（:121）。

plan.md 聲稱 mDNS 已實作，但實際只有 /24 HTTP 掃描，未找到 mDNS/Bonjour 實作或依賴。network/discovery.go:25 依賴到 8.8.8.8 的 UDP route 並假設 /24，離線 LAN、VPN、非 /24 和多網卡環境都可能失敗。

### P2-3：服務端防禦與供應鏈硬化不足

HTTP server 未設定 ReadHeaderTimeout、ReadTimeout、WriteTimeout 或 body size limit；/api/config 對任意大小 JSON Decode。UI 多個 state-changing handler 沒有限制 method，例如 /api/switch 和 /api/sync-to，可被 localhost CSRF 利用。

embedded 工具只依檔案大小判定快取是否可信（internal/embedded/embedded.go:75），而且會執行 user cache、PATH 或 D:\vkvm 路徑中的工具。應至少使用雜湊/版本戳與原子寫入。

本環境沒有 staticcheck、govulncheck 或 golangci-lint；因此沒有聲稱完成依賴漏洞掃描。release workflow 的 action 均以可變 tag 引用，未 pin 到 commit SHA，亦未建立 release checksum/signature。

## 建議修正順序

1. 修復 Windows notify 檔、對齊 CI Go 版本、加入雙平台 build gate。
2. 先停用不安全預設：API/USB forwarding 預設關閉，未驗證或偵測不確定時拒絕注入。
3. 設計單一安全配對協議並同時覆蓋 HTTP、WS、UDP；移除 token 回傳與明文控制面。
4. 修復 AppleScript/UI 注入與所有 localhost CSRF/SSRF 路徑。
5. 重構 ConfigManager、WS lifecycle 與 agent transport routing。
6. 修復 Windows input thread/queue，進行 Windows 實機 kill-switch、斷線、按鍵釋放、多 Agent 混合傳輸測試。
7. 讓設定變更具有明確的「立即套用」或「需重啟」語意，更新 TODO/README/plan 的實際狀態。

## 建議納入 CI 的最小驗收

- gofmt -d、go vet、go test -race、go test -cover；
- macOS arm64 與 Windows amd64 build；
- API 空 token 必拒絕、WS token 可連線、UDP 偽造封包被拒絕的測試；
- 無 monitor、DDC error、部分 DDC error 時不得啟用注入或提交 Profile；
- 多 Agent（UDP + WS fallback）端對端輸入測試；
- XSS/AppleScript escaping、CSRF method/CSRF token、sync target validation 測試；
- Windows 實機 hook、kill switch、停用輸入與 key-up/button-up 壓力測試。

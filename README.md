# SamWeb

Chrome-style web browser built with Go + WebView2 (via [wails](https://wails.io)),
with multi-profile cookie management and AICQ bridge integration.

## Architecture

- **main samweb.exe** — wails app, listens on `127.0.0.1:7777` (agent API)
  + WebView2 CDP on port `9222`
- **tab workers** — child `samweb.exe --tab --profile <id> --cdp-port <P>`
  processes, one per profile, each loads `https://chat.z.ai`
- **AICQ bridges** — `python scripts/aicq_bridge.py` child processes,
  one per profile with an AICQ identity, polling AICQ messages and
  forwarding them to z.ai Agent mode

Auto-spawn: on startup, main samweb lists all profiles and spawns a
tab worker (+ AICQ bridge if profile has AICQ identity) for each.

## Files (repo layout)

```
cmd/samweb/main.go           # main entrypoint
internal/browser/            # browser, tab_worker, wails_backend, profiles
internal/agent/              # agent HTTP API server (server.go, backend.go)
internal/cdp/client.go       # CDP WebSocket client (DOM/Runtime/Fetch)
scripts/aicq_bridge.py       # AICQ <-> z.ai bridge (Python, asyncio)
scripts/deploy.py            # dev deploy: upload + build + restart
start_samweb.bat             # one-click start (build if needed, then launch)
start_samweb.vbs             # hidden-window launcher (sets WEBVIEW2 CDP env)
build_samweb.bat             # compile-only (produces samweb.exe.new)
go-webview2-patch/           # patched go-webview2 (CDP port support)
samweb.exe.manifest          # Windows manifest (embedded by go build)
samweb.syso                  # Windows resource (icon, embedded by go build)
```

## Build

Prerequisites on the build machine (shan):
- Go 1.25+
- `go-webview2-patch/` directory present (patched go-webview2 with CDP)
- `go.mod` has `replace github.com/wailsapp/go-webview2 v1.0.22 => ./go-webview2-patch`

```bat
:: On shan (Windows):
C:\samweb\build_samweb.bat
:: -> produces C:\samweb\samweb.exe.new
```

## One-click start

```bat
:: On shan (Windows, in RDP session):
C:\samweb\start_samweb.bat
```

This will:
1. Build `samweb.exe` if missing or source is newer
2. Launch via `start_samweb.vbs` (hidden window, sets `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222`)
3. Output goes to `C:\samweb\run.log`

Auto-spawn takes ~30s after startup (3 profiles × tab worker + bridge).

## Dev deploy (from local Linux machine)

```bash
# 1. SSH forwarder (so local can reach shan's 127.0.0.1 ports)
python3 scripts/daemon_forward.py   # double-fork daemon, survives shell exit

# 2. Edit files in /home/z/my-project/samweb_code/

# 3. Deploy + build + restart
python3 scripts/deploy_to_shan.py
#   (equivalent to scripts/deploy.py on shan, but uploads from local)
```

## Scheduled task (auto-start on boot/login)

A `RestartSamweb` scheduled task is registered on shan:
- Trigger: ONLOGON (Administrator)
- Action: `wscript.exe "C:\samweb\start_samweb.vbs"`
- Runs in RDP session (session 2) so WebView2 has desktop access

Manage it:
```bat
schtasks /Run /TN RestartSamweb      :: start now
schtasks /Query /TN RestartSamweb    :: check status
```

## AICQ bridge flow

1. AICQ owner sends message to agent (via aicq.me web UI)
2. Bridge receives it via AICQ SDK WebSocket
3. Bridge ensures z.ai is in Agent mode, selects/creates a chat
4. Bridge types the message into z.ai's `#chat-input` and clicks send
5. Bridge polls z.ai's response via:
   - **Fetch domain** (primary — works when z.ai JS is blocked during long tasks)
   - **CDP DOM domain** (`/agent/cdp-dom-text-all`) — bypasses JS thread entirely
   - **Runtime.evaluate** (fallback — works for short tasks)
6. Bridge streams incremental response chunks back to AICQ via `core.send_stream_chunk`
7. When response is stable, bridge calls `core.send_stream_end`

### Long-task streaming (the hard case)

z.ai Agent mode can run multi-minute tasks (clone repo, playwright tests,
deploy). During these, z.ai's JS thread is blocked — `Runtime.evaluate`
times out. The bridge handles this by:

- Using CDP **Fetch domain** to intercept SSE chunks (`workspaces/up` endpoint)
- Using CDP **DOM domain** (`DOM.querySelectorAll` + `DOM.getOuterHTML`) to
  read the rendered assistant message without touching JS
- `pre_count` slicing: only consider assistant messages at index >= pre_count
  (so we don't re-read OLD messages from chat history)

## Debugging

- z.ai tab CDP: `http://127.0.0.1:<cdp_port>/json` (per-profile, see run.log)
- Agent API: `http://127.0.0.1:<agent_port>/agent/health`
- Bridge logs: `C:\Users\Administrator\.samweb\logs\<profile>_bridge.log`
- Main log: `C:\samweb\run.log`

## AICQ identities

Stored in `~/.samweb/profiles.json` per profile:
```json
{
  "id": "qq",
  "name": "qq",
  "aicq_identity": {
    "account_id": "ai_79ab6146",
    "owner_id": "1000008",
    "db_path": "C:\Users\Administrator\.aicq-sdk\data.db"
  }
}
```

AICQ SDK DBs at `~/.aicq-sdk/data*.db` hold agent keys + message history.

## License

MIT

---

# 工作交接文档 (2026-07-12)

## 项目背景

SamWeb 是一个基于 Go + WebView2 (wails) 的浏览器，配合 AICQ bridge 实现：
- 3 个 z.ai profile（qq / 139 / carterdong168）各自独立运行
- 每个 profile 有一个 AICQ agent identity，绑定到 owner（gasschina@gmail.com / AICQ ID 1000008）
- Owner 通过 aicq.me 发消息给 agent → bridge 转发到 z.ai Agent 模式 → z.ai 生成回复 → bridge 转发回 AICQ
- **核心目标**：AICQ 端和 z.ai 端的流式回复内容 + 样式一致

## 当前运行环境

- **shan 电脑**：Windows Server 2022，RDP session 2
  - samweb.exe 主进程 + 3 个 tab worker 子进程 + 3 个 AICQ bridge Python 子进程
  - 启动方式：`schtasks /Run /TN RestartSamweb`（触发 `start_samweb.vbs`）
  - WebView2 CDP 端口：9222（主窗口），每个 tab worker 有独立 CDP + agent API 端口（动态分配）
- **本地开发机**：Linux，通过 aitun ssh-proxy 连接 shan
  - SSH forwarder：`/home/z/my-project/scripts/ssh_forward.py`（double-fork daemon）
  - Playwright：用于操作 aicq.me 发消息 + 截图对比

## 代码仓

- GitHub：`samaidev/samweb`（main 分支）
- 最新 commit：`134b1b6 Fix: html_to_markdown converter + last message hash tracking + li newline`
- shan 上 `C:\samweb` = origin/main，working tree clean
- 本地编辑目录：`/home/z/my-project/samweb_code/`

## 已完成的修复（按 commit 顺序）

| Commit | 修复内容 | 状态 |
|---|---|---|
| `ff3639b` | 仓库清理 + 一键启动脚本 + README | ✅ 已验证 |
| `6853658` | CDP Enter 键发送 + DOM-only 文本 + anti-freeze 30s 点击 + 默认继续会话 | ✅ 已验证 |
| `ebebffc` | pre_count 用 CDP DOM 计算 + 60s input 等待 | ✅ 已验证 |
| `c1e5ce4` | 窗口最大化（main + tab workers，`options.Maximised`） | ✅ 已验证 |
| `fbbeb25` | pre_count 溢出修复（z.ai 删旧消息时取最后一条） | ⚠️ 后来被 revert |
| `134b1b6` | html_to_markdown 转换器 + last message hash tracking + li 换行 | ✅ 已验证 |

## 当前工作流（已验证可用）

```
Owner → aicq.me 发消息 → AICQ WebSocket → bridge on_message
  ↓
bridge 继续之前的 z.ai 会话（导航 + 10s 等待）
  ↓
bridge 用 CDP DOM 检测 #chat-input 就绪（60s 超时）
  ↓
bridge 用 JS setter 输入文本 + CDP Input.dispatchKeyEvent 发送 Enter
  ↓
bridge 用 CDP DOM 计算 pre_count + pre_last_hash
  ↓
bridge 每 30s 点击当前会话（anti-freeze，不 toggle Agent 模式）
  ↓
bridge 用 CDP cdp-dom-text-all + pre_count/pre_last_hash 切片读取新回复
  ↓
bridge 用 html_to_markdown() 把 z.ai HTML 转成 markdown 源码
  ↓
bridge 用 send_stream_chunk("text", markdown) 流式转发到 AICQ
```

## 剩余问题（下一个智能体需要解决）

### 问题 1：AICQ 端 markdown 没渲染（样式不一致）

**现象**：
- z.ai 端：标题"待办列表" + 3 个带复选框的列表项，markdown 正确渲染
- AICQ 端：纯文本 `# 待办列表- [ ] 完成项目周报- [ ] 回复客户邮件- [ ] 准备会议演示`，**没有 markdown 渲染**

**内容一致**（都是"待办列表 / 完成项目周报 / 回复客户邮件 / 准备会议演示"），但 **AICQ 端没渲染 markdown**。

**已排除**：
- bridge 已经发送了正确的 markdown 源码（`# 待办列表\n- [ ] 完成项目周报\n...`）
- 问题在 AICQ 客户端（chat.html）的渲染逻辑

**下一步**：
1. 查看 AICQ 的 chat.html 是怎么渲染 `stream_chunk` 的 `text` 类型——是否用 marked.js 渲染 markdown
2. 如果 chat.html 不渲染 markdown，可能需要：
   - 改 chunk type（如用 `markdown` 而不是 `text`）
   - 或在 AICQ 端加 markdown 渲染
3. AICQ chat.html 源码在 aicq.me 服务器上，可能需要从 aicq.me 的 `/static/` 目录下载

### 问题 2：z.ai 高峰时段弹窗频繁

**现象**：z.ai Agent 模式发送消息时频繁弹出"高峰时段"弹窗，bridge 需要重试 5-7 次才能成功发送。

**当前处理**：bridge 有 `zai_dismiss_high_traffic_popup` + 20 次重试逻辑，但每次重试等 20s，总共可能等 2-3 分钟。

**下一步**：可能需要加更长的初始等待，或者在弹窗后切换到其他 profile 轮询。

### 问题 3：pre_count 和 z.ai 消息数不一致

**现象**：z.ai 有时会**替换**最后一条消息（而不是新增），导致 `pre_count >= len(all_html)`。

**当前修复**（commit `134b1b6`）：用 `pre_last_hash`（MD5 of last message content）检测替换。如果 hash 变了，取 `all_html[-1]` 作为新回复。

**潜在问题**：如果 z.ai 在生成过程中多次更新最后一条消息（流式更新），hash 会频繁变化，bridge 可能读到中间状态。当前用 `stable_count >= 3`（15s 稳定）来判断完成，应该能处理。

## 关键文件索引

### Go 代码（需要 `go build -tags "desktop,production"`）
- `cmd/samweb/main.go` — 主入口
- `internal/browser/browser.go` — 主浏览器窗口（含 auto-spawn 逻辑 + `WindowStartState: options.Maximised`）
- `internal/browser/tab_worker.go` — tab worker 子进程（每 profile 一个，`WindowStartState: options.Maximised`）
- `internal/browser/wails_backend.go` — CDP 客户端管理 + `CDPDOMTextAll` / `CDPDispatchEnterKey`
- `internal/agent/server.go` — agent HTTP API（`/agent/cdp-dom-text-all`、`/agent/cdp-input-enter` 等端点）
- `internal/agent/backend.go` — Backend interface 定义
- `internal/cdp/client.go` — CDP WebSocket 客户端（`GetDOMTextAll`、`DispatchEnterKey`）
- `go-webview2-patch/` — patched go-webview2（CDP 端口支持，`go.mod` 有 replace 指令）

### Python 代码（bridge，不需要编译）
- `scripts/aicq_bridge.py` — **核心**（2400+ 行）。关键函数：
  - `html_to_markdown(html)` — z.ai HTML → markdown 转换器
  - `zai_read_response_via_dom(session, agent_base, pre_count, timeout, pre_last_hash)` — 用 CDP DOM 读新回复
  - `zai_type_and_send(session, agent_base, profile_id, message, ...)` — 输入文本 + CDP Enter 发送
  - `zai_new_chat(session, agent_base, profile_id)` — 新建会话（优先 BUTTON，等 URL 变化）
  - `zai_dom_text_all(session, agent_base, selector, timeout)` — CDP DOM 取所有匹配元素
  - 主流程在 `run_bridge()` 里，poll loop 每 5s 一次，每 6 polls（30s）anti-freeze 点击

### 部署脚本
- `start_samweb.bat` — 一键启动（编译检查 + vbs 启动）
- `start_samweb.vbs` — 隐藏窗口启动（设 `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222`）
- `build_samweb.bat` — 只编译（产出 `samweb.exe.new`）
- `scripts/deploy.py` — dev 部署（上传 + 编译 + 重启，从本地 Linux 跑）

### 本地测试脚本（在 `/home/z/my-project/scripts/`）
- `ssh_conn.py` — SSH 命令执行（paramiko + aitun ssh-proxy）
- `sftp_get.py` — SFTP 下载文件
- `ssh_forward.py` — SSH 端口转发（double-fork daemon，`daemon_forward.py` 启动）
- `deploy_to_shan.py` — 从本地上传 + 编译 + 重启（调用 `scripts/deploy.py` 逻辑）
- `compare_screenshots.py` — 发消息 + 同时截图 AICQ + z.ai 两端
- `check_zai_state.py` — 用 CDP 检查 z.ai 当前状态（assistant 消息数 + generating）
- `debug_selectors.py` — 调试 z.ai DOM 选择器
- `debug_send_btn.py` — 调试 z.ai 发送按钮

## 一键操作指南

### 在 shan 上启动 samweb
```bat
:: RDP 登录后，双击或命令行：
C:\samweb\start_samweb.bat
:: 或者用 schtask：
schtasks /Run /TN RestartSamweb
```

### 从本地部署代码改动
```bash
# 1. 确保 SSH forwarder 在跑
/home/z/.venv/bin/python3 /home/z/my-project/scripts/daemon_forward.py

# 2. 编辑代码（在 /home/z/my-project/samweb_code/ 下）

# 3. 一键部署（上传 + 编译 + 重启）
python3 /home/z/my-project/scripts/deploy_to_shan.py
```

### 测试两端一致性
```bash
# 1. 确保 forwarder + samweb 在跑
curl -s http://127.0.0.1:15046/agent/health  # qq agent API

# 2. 发消息 + 截图对比
python3 /home/z/my-project/scripts/compare_screenshots.py

# 3. 用 VLM 对比两张截图
z-ai vision -p "描述 AICQ 回复内容和样式" -i /home/z/my-project/download/compare_screenshots/aicq_side.png
z-ai vision -p "描述 z.ai 回复内容和样式" -i /home/z/my-project/download/compare_screenshots/zai_side.png
```

### 更新 forwarder 端口（samweb 重启后端口会变）
```bash
# 1. 查看新端口
python3 /home/z/my-project/scripts/ssh_conn.py 'type C:\samweb\run.log 2>&1 | findstr "profile=qq"'

# 2. 编辑 ssh_forward.py 里的 LOCAL_TO_REMOTE

# 3. 重启 forwarder
pkill -9 -f ssh_forward.py
/home/z/.venv/bin/python3 /home/z/my-project/scripts/daemon_forward.py
```

## AICQ 账号信息

- **Owner 账号**：gasschina@gmail.com / Dongshan@168（AICQ ID 1000008，display_name PHONE）
- **3 个 AICQ agent**：
  - `ai_79ab6146` — SamWeb Browser（qq profile），DB: `~/.aicq-sdk/data.db`
  - `ai_7e9a7d6f` — SamWeb-139（139 profile，当前会话），DB: `~/.aicq-sdk/data_139.db`
  - `ai_8653a541` — SamWeb-carterdong168（carterdong168 profile），DB: `~/.aicq-sdk/data_carterdong168.db`
- aicq.me 登录：email + password（`#loginEmail` + `#loginPassword` + `#loginForm button.btn-primary`）
- Playwright storage_state：`/home/z/my-project/aicq_state.json`

## shan 电脑访问

- 主机：`shan.aitun.cc:22`，用户 `Administrator`，密码 `dongshan168`
- SSH：通过 `aitun ssh-proxy shan.aitun.cc 22`（paramiko ProxyCommand）
- aitun 安装：`curl -fsSL https://aitun.cc/install.sh | bash`

## 注意事项

1. **窗口必须最大化**：z.ai Agent 模式长任务时，如果窗口不够大，输入框和停止按钮会被裁切。`browser.go` 和 `tab_worker.go` 都设了 `WindowStartState: options.Maximised`。
2. **Anti-freeze 不要点 Agent 模式按钮**：Agent 模式按钮是 toggle，点击会切回 Chat 模式丢失所有 Agent 输出。只点当前会话（sidebar 里的 chat button）。
3. **pre_count 必须用 CDP DOM 计算**：用 JS `eval` 计算的 count 和 CDP DOM 的 count 不一致（JS 会数到 markdown-prose 子元素），导致切片错位。
4. **发送消息用 CDP Enter 键**：JS `KeyboardEvent` 不被 React/Svelte 识别，必须用 CDP `Input.dispatchKeyEvent`（`/agent/cdp-input-enter` 端点）。
5. **go.mod 必须有 replace 指令**：`replace github.com/wailsapp/go-webview2 v1.0.22 => ./go-webview2-patch`，否则 CDP 端口 9222 开不了。
6. **编译用 `-tags "desktop,production"`**：`go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb`
7. **samweb 必须在 RDP session 里跑**：WebView2 需要桌面会话，session 0（Services）不行。`RestartSamweb` schtask 配置为 ONLOGON + `/IT`（interactive）。

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

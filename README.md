# SamWeb

A Chrome-style desktop web browser built with **Go** and the **WebKit/WebView2** engine via [`webview/webview_go`](https://github.com/webview/webview_go), with **built-in anti-bot bypass capabilities** including CDP (Chrome DevTools Protocol) integration, anti-detection JS injection, and a pluggable captcha breakthrough framework.

SamWeb packages a Chrome-lookalike UI (tab strip, omnibox, back/forward/reload, bookmarks, history, multiple search engines) into a tiny Go binary. The UI is plain HTML/CSS/JS that runs inside a WebKit webview, and pages from the real web are loaded through a built-in reverse proxy that strips `X-Frame-Options` / `Content-Security-Policy` frame-busting headers so they can be rendered inside the embedded iframe.

> **What makes SamWeb unique?** Unlike regular browsers, SamWeb is designed for **automated, hands-off browsing**. It exposes a full HTTP/JSON agent API, injects anti-detection JS at document_start, connects to WebView2's CDP endpoint for trusted input events, and includes a breakthrough framework that can automatically detect and bypass slider captchas (verified on Aliyun baxia / modelscope.cn).

---

## Features

### Browser Features
- **Chrome-style UI** — tab strip with favicons, pill-shaped omnibox, back/forward/reload/home buttons, bookmark star.
- **Multi-tab browsing** — open/close/switch tabs, per-tab history (Alt+Left / Alt+Right).
- **Smart omnibox** — type a URL and it navigates directly; type anything else and it searches.
- **Search engine picker** — Google, Bing, DuckDuckGo, Baidu.
- **Bookmarks & History** — click the star to bookmark; every navigation is recorded locally (last 500 entries).
- **Built-in proxy** — strips frame-busting headers so pages render inside the iframe.
- **Keyboard shortcuts** — `Ctrl+T` new tab, `Ctrl+W` close tab, `Ctrl+R` reload, `Ctrl+L` focus omnibox.

### Automation & Anti-Bot Features
- **Agent API** — full HTTP/JSON control plane: navigate, click, type, scroll, eval JS, screenshots, and more.
- **CDP Integration** — connects to WebView2's Chrome DevTools Protocol for trusted input events (`isTrusted=true`).
- **Anti-Detection JS** — injected at document_start: hides `navigator.webdriver`, fakes `window.chrome`, plugins, WebGL fingerprint, and more.
- **Breakthrough Framework** — pluggable system that auto-detects and bypasses slider captchas. Currently supports Aliyun baxia NoCaptcha.
- **Cookie Persistence** — saves both proxy cookies AND WebView2 browser cookies to disk, so login sessions survive process restarts.
- **CDP Screenshots** — captures actual rendered pixels via `Page.captureScreenshot` (not SVG foreignObject fallback).

---

## Project layout

```
samweb/
├── cmd/
│   ├── samweb/
│   │   └── main.go                # CLI entry point (full browser)
│   └── samweb-agent-test/
│       └── main.go                # Mock backend for API testing
├── internal/
│   ├── agent/                     # HTTP/JSON agent API (server + client SDK)
│   ├── breakthrough/              # Anti-bot bypass framework
│   │   ├── breakthrough.go        # Challenge interface + Manager
│   │   └── aliyun_baxia.go        # Aliyun baxia slider bypass (CDP + JS Teleport)
│   ├── browser/                   # WebView integration
│   │   ├── browser.go             # webview + UI + proxy + CDP + anti-detection
│   │   ├── webview_backend.go     # agent.Backend implementation
│   │   └── ui/                    # Chrome-style HTML/CSS/JS
│   ├── cdp/                       # Chrome DevTools Protocol client
│   │   ├── client.go              # WebSocket client + Network domain + Target.attach
│   │   ├── mouse.go               # Input.dispatchMouseEvent (raw + drag)
│   │   └── touch.go               # Input.dispatchTouchEvent
│   ├── proxy/                     # Reverse proxy with cookie persistence
│   └── search/                    # Omnibox URL-vs-search resolver
├── scripts/
│   ├── login_modelscope.py        # End-to-end modelscope.cn login automation
│   └── test_agent.py              # Agent API test suite
├── go.mod
└── README.md
```

---

## Build

### Prerequisites

You need **Go 1.25+** (or Go 1.24+ with `GOTOOLCHAIN=go1.25.0+auto`) and the platform webview development headers:

| OS | Install command |
|----|-----------------|
| Debian / Ubuntu | `sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev pkg-config` |
| Fedora | `sudo dnf install webkit2gtk4.1-devel gtk3-devel pkgconf-pkg-config` |
| Arch Linux | `sudo pacman -S webkit2gtk-4.1 gtk3 pkgconf` |
| macOS | (uses system WebKit; no extra packages) `brew install pkg-config` |
| Windows | (uses WebView2; runtime ships with Windows 11). **Also needs `WebView2Loader.dll`** next to `samweb.exe` — see below. |

### Build from source

```bash
git clone https://github.com/samaidev/samweb.git
cd samweb
go build -o samweb ./cmd/samweb
./samweb --agent-addr 127.0.0.1:7777 --agent-token my-secret
```

### Windows build notes

1. **WebView2Loader.dll** must be in the same directory as `samweb.exe`:
   ```powershell
   Invoke-WebRequest -Uri "https://www.nuget.org/api/v2/package/Microsoft.Web.WebView2/1.0.2903.40" -OutFile webview2.nupkg
   Expand-Archive webview2.nupkg -DestinationPath webview2pkg
   Copy-Item webview2pkg\build\native\x64\WebView2Loader.dll .
   ```

2. **SSH sessions**: use `schtasks /IT` or `PsExec -i <session>` to run in the interactive session:
   ```powershell
   PsExec64.exe -i 3 -d samweb.exe --agent-addr 127.0.0.1:7777 --agent-token secret --cdp-port 9222
   ```

### Run with options

```bash
./samweb --agent-addr 127.0.0.1:7777 --agent-token my-secret --cdp-port 9222 --width 230 --height 400
```

| Flag | Default | Description |
|------|---------|-------------|
| `--title` | `SamWeb` | OS window title |
| `--width` | `1280` | Window width in pixels |
| `--height` | `800` | Window height in pixels |
| `--engine` | `Google` | Default search engine |
| `--agent-addr` | `127.0.0.1:7777` | Agent HTTP API bind address |
| `--agent-token` | (empty) | Bearer token for agent API auth |
| `--cdp-port` | `9222` | CDP remote debugging port (0 disables; needed for breakthrough/cdp-mouse/screenshot-trusted) |

---

## How it works

```
┌──────────────────────────────────────────────────────────────┐
│                       SamWeb process                         │
│                                                              │
│  ┌────────────────────┐   ┌────────────────────────────────┐ │
│  │  webview (WebKit)  │   │  internal HTTP servers         │ │
│  │  ┌──────────────┐  │   │  ┌─────────────┐ ┌───────────┐ │ │
│  │  │  iframe      │◀─┼───┼──│ /api/*      │ │ /proxy    │ │ │
│  │  │  (page)      │  │   │  │ UI assets   │ │ (reverse  │ │ │
│  │  └──────────────┘  │   │  │             │ │  proxy)   │ │ │
│  │  Chrome-style UI   │   │  └─────────────┘ └─────┬─────┘ │ │
│  │  Anti-detect JS    │   │                        │       │ │
│  │  (document_start)  │   │  ┌─────────────────────┴─────┐ │ │
│  └────────────────────┘   │  │  CDP client (gorilla/ws)   │ │ │
│          ▲                │  │  - Input.dispatchMouseEvent │ │ │
│          │ webview.Eval   │  │  - Page.captureScreenshot   │ │ │
│          │ (via Dispatch) │  │  - Network.getAllCookies    │ │ │
│  ┌───────┴────────────┐   │  │  - Target.attachToTarget   │ │ │
│  │ WebviewBackend     │   │  └─────────────────────────────┘ │ │
│  │ + Breakthrough mgr │   │                                  │ │
│  └───────┬────────────┘   │  ┌─────────────────────────────┐ │ │
│          │                │  │  Breakthrough Framework      │ │ │
│  ┌───────┴────────────┐   │  │  - AliyunBaxiaSlider        │ │ │
│  │ agent.Server       │   │  │    (CDP + JS Teleport)      │ │ │
│  │ HTTP /agent/*      │   │  │  - extensible (Challenge)   │ │ │
│  └───────┬────────────┘   │  └─────────────────────────────┘ │ │
│          │                └──────────────────────────────────┘ │
└──────────┼─────────────────────────────────────────────────────┘
           │ HTTP / JSON
           ▼
    External agent (Python, Go, LLM, ...)
```

---

## Hands-off login (the "zero human participation" workflow)

SamWeb's reason for existing is to let an agent drive a browser
without a human in the loop. For login-protected sites this means:
log in once, then never again.

### How it works

1. `POST /agent/load-cookies` — restore cookies from previous session.
2. `POST /agent/navigate-direct` to a whoami API — if logged in, exit.
3. Otherwise: navigate to login page, auto-fill form, click login.
4. `POST /agent/breakthrough` — auto-detect + bypass slider captcha.
5. Complete login flow (e.g., fill SMS code).
6. `POST /agent/save-cookies` — persist session.
7. Every subsequent run hits step 2 and exits immediately.

### Baxia slider — SOLVED!

Aliyun baxia NoCaptcha slider has been bypassed using a novel
**CDP + JS Teleport** technique:

1. **CDP mousedown** (`Input.dispatchMouseEvent`, isTrusted=true) —
   establishes a real mouse-down state on the slider handle
2. **CDP mousemove × 25 steps** (isTrusted=true) — drags handle to
   ~50% of track (baxia limits CDP events to ~50%)
3. **JS Teleport** — directly sets `handle.style.left` and
   `bg.style.width` to the end position, jumping handle from 50% to 100%.
   Key insight: baxia tracks handle position via `handle.style.left`
   (inline style), and `bg.style.width = handle.style.left + 21`
   (handle is 42px wide, half = 21).
4. **CDP mouseup** (isTrusted=true) — triggers baxia's verification

**Verified on modelscope.cn**: SMS login flow completed end-to-end:
- Switch to SMS tab → select +86 → fill phone → check agreement
- Click 获取验证码 → baxia slider appears
- `POST /agent/breakthrough` → slider passed → SMS code sent
- Fill SMS code → click login → **logged in as ctz168!**
- 29 cookies persisted to `~/.samweb/cdp-cookies.json`

### Breakthrough framework

The `internal/breakthrough` package is a **pluggable framework** for
detecting and bypassing dynamic anti-bot challenges:

```go
// Implement the Challenge interface to support a new captcha type
type Challenge interface {
    Name() string
    Detect(ctx, env) (found bool, meta map, err error)
    Bypass(ctx, env, meta) (success bool, err error)
}
```

Built-in challenges:
- **AliyunBaxiaSlider** — CDP + JS Teleport technique (verified)

To add support for Geetest, Tencent, etc., implement `Challenge` and
register it in `NewManager()`. No changes to agent API or browser code needed.

**One-call bypass:**
```bash
curl -X POST http://127.0.0.1:7777/agent/breakthrough
# → {"challenge":"aliyun-baxia-slider","success":true}
```

---

## Agent API

SamWeb ships with a first-class **HTTP/JSON control plane** for LLM agents,
test harnesses, and automation scripts.

### Quickstart

```bash
# Start the agent server (mock backend, no GUI needed)
go build -o samweb-agent-test ./cmd/samweb-agent-test
./samweb-agent-test --addr 0.0.0.0:7777

# Drive the browser:
curl -s http://127.0.0.1:7777/agent/health
curl -s -X POST http://127.0.0.1:7777/agent/navigate \
     -H 'Content-Type: application/json' -d '{"url":"https://example.com"}'
curl -s http://127.0.0.1:7777/agent/screenshot -o shot.png
```

For real browsing with anti-bot capabilities:
```bash
./samweb --agent-addr 127.0.0.1:7777 --agent-token secret --cdp-port 9222
```

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET  | `/agent/health` | Liveness probe (always public) |
| GET  | `/agent/state` | Current URL, title, tabs, history flags |
| POST | `/agent/navigate` | Navigate via proxy |
| POST | `/agent/navigate-direct` | Load URL as top-level page (for SPAs) |
| POST | `/agent/back` `/agent/forward` `/agent/reload` `/agent/stop` | History controls |
| POST | `/agent/click` | Click by selector or coordinates |
| POST | `/agent/scroll` | Scroll by coords/selector/direction |
| POST | `/agent/type` | Type text (React-compatible via native setter) |
| POST | `/agent/key` | Press a key with optional modifiers |
| POST | `/agent/drag` | JS-level drag (cubic bezier trajectory) |
| POST | `/agent/eval` | Evaluate JavaScript |
| POST | `/agent/wait` | Wait for an element |
| GET  | `/agent/elements?selector=...` | Query elements with coordinates |
| GET  | `/agent/element?selector=...` | First matching element |
| GET  | `/agent/screenshot` | JS-level PNG screenshot |
| **CDP endpoints** | | |
| POST | `/agent/cdp-mouse` | Single CDP `Input.dispatchMouseEvent` (isTrusted=true) |
| POST | `/agent/drag-trusted` | Full CDP drag (mousedown+move+mouseup) |
| POST | `/agent/drag-touch` | CDP touch events (touchStart+move+End) |
| GET  | `/agent/screenshot-trusted` | CDP `Page.captureScreenshot` (real pixels) |
| POST | `/agent/network/enable` `/disable` `/requests` `/clear` | CDP Network capture |
| **Breakthrough** | | |
| POST | `/agent/breakthrough` | Auto-detect + bypass slider captcha |
| **Cookie management** | | |
| POST | `/agent/reset-cookies` | Clear cookie jar |
| POST | `/agent/save-cookies` | Persist cookies to disk |
| POST | `/agent/load-cookies` | Restore cookies from disk |

### Authentication

If `--agent-token <secret>` is set, every request (except `/agent/health`)
must carry `Authorization: Bearer <secret>`.

---

## Known limitations

- Some sites (Google, YouTube, GitHub) use JS-based frame-busting that the proxy doesn't handle.
- CDP integration is Chromium-only (WebView2 on Windows). On Linux (WebKitGTK), CDP endpoints return errors.
- Download manager is not implemented.
- True pixel-perfect screenshots use CDP `Page.captureScreenshot` (requires `--cdp-port`); fallback is SVG foreignObject.

---

## Roadmap

- [x] ~~CDP / native input injection to bypass `event.isTrusted`~~ — DONE
- [x] ~~Aliyun baxia slider bypass~~ — DONE (CDP + JS Teleport)
- [x] ~~Cookie persistence~~ — DONE (proxy jar + CDP browser cookies)
- [x] ~~Breakthrough framework~~ — DONE (pluggable Challenge interface)
- [ ] Geetest slider support
- [ ] Tencent captcha support
- [ ] Per-tab cookie jar in the proxy
- [ ] Download manager
- [ ] Cross-platform builds via GitHub Actions

---

## License

MIT — see [LICENSE](LICENSE).

---

## Acknowledgements

- [`webview/webview_go`](https://github.com/webview/webview_go) — Go bindings for the `webview` C library.
- [`gorilla/websocket`](https://github.com/gorilla/websocket) — WebSocket client for CDP integration.
- The Chrome UI was used as the visual reference; all assets reimplemented from scratch.
- The CDP + JS Teleport technique was developed through extensive reverse engineering of Aliyun baxia NoCaptcha during modelscope.cn login automation.

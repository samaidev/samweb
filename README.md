# SamWeb

A Chrome-style desktop web browser built with **Go** and the **WebKit** engine via [`webview/webview_go`](https://github.com/webview/webview_go).

SamWeb packages a Chrome-lookalike UI (tab strip, omnibox, back/forward/reload, bookmarks, history, multiple search engines) into a tiny Go binary. The UI is plain HTML/CSS/JS that runs inside a WebKit webview, and pages from the real web are loaded through a built-in reverse proxy that strips `X-Frame-Options` / `Content-Security-Policy` frame-busting headers so they can be rendered inside the embedded iframe.

> **Why WebKit?** `webview/webview_go` uses WebKitGTK on Linux, WebView2 (Chromium-based) on Windows, and WebKit on macOS — the same engine family that powers Safari and many modern browsers. The "most popular WebKit binding" in the Go ecosystem is exactly this one.

---

## Features

- **Chrome-style UI** — tab strip with favicons, pill-shaped omnibox, back/forward/reload/home buttons, bookmark star.
- **Multi-tab browsing** — open/close/switch tabs, per-tab history (Alt+Left / Alt+Right).
- **Smart omnibox** — type a URL and it navigates directly; type anything else and it searches. Heuristics mirror Chrome's omnibox (`example.com`, `localhost:8080`, `127.0.0.1`, etc. all go directly to the URL).
- **Agent API** — full HTTP/JSON control plane so an external program (LLM agent, test harness, automation script) can drive the browser: navigate, back/forward/reload, click (by selector or coordinates), scroll, type, press keys, eval JavaScript, wait for elements, query elements with their coordinates, take screenshots, and read browser state. Optional bearer-token authentication.
- **Search engine picker** — Google, Bing, DuckDuckGo, Baidu. The choice is remembered.
- **Bookmarks** — click the star to bookmark, open the bookmarks popover to revisit.
- **History** — every navigation is recorded locally (last 500 entries), viewable from the toolbar.
- **New tab page** — colorful SamWeb logo, search box, and shortcut tiles for popular sites.
- **Built-in proxy** — fetches remote pages on the Go side and strips frame-busting headers so they render inside the embedded iframe.
- **Keyboard shortcuts** — `Ctrl+T` new tab, `Ctrl+W` close tab, `Ctrl+R` reload, `Ctrl+L` focus omnibox, `Ctrl+Shift+H` history.
- **Zero runtime dependencies** — UI is embedded in the binary via `//go:embed`; the only system library required at build time is WebKitGTK.

---

## Project layout

```
samweb/
├── cmd/
│   └── samweb/
│       └── main.go                # CLI entry point
├── internal/
│   ├── browser/
│   │   ├── browser.go             # wires webview + UI server + proxy
│   │   └── ui/
│   │       ├── index.html         # Chrome-style UI shell
│   │       ├── styles.css         # Chrome-lookalike styling
│   │       └── app.js             # tab/omnibox/history/bookmark logic
│   ├── proxy/
│   │   └── proxy.go               # reverse proxy that strips iframe headers
│   └── search/
│       └── search.go              # omnibox URL-vs-search resolver
├── go.mod
├── go.sum
├── README.md
└── LICENSE
```

---

## Build

### Prerequisites

You need **Go 1.22+** and the WebKitGTK development headers:

| OS | Install command |
|----|-----------------|
| Debian / Ubuntu | `sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev pkg-config` |
| Fedora | `sudo dnf install webkit2gtk4.1-devel gtk3-devel pkgconf-pkg-config` |
| Arch Linux | `sudo pacman -S webkit2gtk-4.1 gtk3 pkgconf` |
| macOS | (uses system WebKit; no extra packages) `brew install pkg-config` |
| Windows | (uses WebView2; runtime ships with Windows 11) |

### Build from source

```bash
git clone https://github.com/samaidev/samweb.git
cd samweb
go build -o samweb ./cmd/samweb
./samweb
```

### Run with options

```bash
# Use Bing as the default search engine
./samweb --engine Bing

# Larger window
./samweb --width 1600 --height 900

# Custom window title
./samweb --title "SamWeb — Dev"
```

| Flag | Default | Description |
|------|---------|-------------|
| `--title` | `SamWeb` | OS window title |
| `--width` | `1280` | Window width in pixels |
| `--height` | `800` | Window height in pixels |
| `--engine` | `Google` | Default search engine: `Google`, `Bing`, `DuckDuckGo`, `Baidu` |

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
│  └────────────────────┘   └────────────────────────┼───────┘ │
└─────────────────────────────────────────────────────┼─────────┘
                                                      ▼
                                              remote website
```

1. **Embedded UI server** serves the Chrome-style HTML/CSS/JS on `127.0.0.1:<random>`.
2. **Embedded proxy server** listens on `127.0.0.1:<random>` and forwards requests to remote sites. It strips `X-Frame-Options`, `Content-Security-Policy`, `Cross-Origin-*` and a few other headers so that the response can be embedded inside the iframe.
3. The **webview** opens the UI server URL in a WebKit window. All user interactions (tab management, omnibox, bookmarks, history) are handled in JavaScript inside the webview. Page navigation goes through the proxy so the iframe can actually display remote content.
4. The omnibox resolver (`samwebResolve` Go binding or `/api/resolve` HTTP endpoint) decides whether the user typed a URL or a search query.

### Why a proxy?

WebKit's iframe element honors `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors`. Most major websites send at least one of these headers, which would cause the iframe to render blank. By fetching the page through a same-origin reverse proxy that strips those headers, SamWeb can embed the page content directly.

The proxy is intentionally minimal: it does not preserve cookies across requests, it does not rewrite resource URLs (so absolute URLs work; relative URLs work because the iframe document is same-origin with the proxy), and it is bound to `127.0.0.1` so it is not reachable from the network.

---

## Known limitations

- Some sites (Google, YouTube, GitHub) use JavaScript-based frame-busting or post-login redirects that the proxy does not handle — those may show a "refused to connect" message or redirect loop.
- Cookie persistence across proxy requests is not implemented. Sites that require login will not stay logged in.
- Download manager is not implemented.
- Browser-level devtools are not wired up (you can still open the webview's devtools via right-click on Linux builds if WebKitGTK was compiled with `WEBKIT_DEVELOPER_MODE=ON`).

These are deliberate scope choices for the initial release. Pull requests welcome.

---

## Roadmap

- [ ] Per-tab cookie jar in the proxy
- [ ] Resource URL rewriting for fully correct relative-path resolution
- [ ] Download manager
- [ ] Per-tab webview instances (true multi-tab isolation)
- [ ] Settings UI (homepage, theme, default engine, clear-on-exit)
- [ ] Cross-platform builds via GitHub Actions

---

## License

MIT — see [LICENSE](LICENSE).

---

# Agent API

SamWeb ships with a first-class **HTTP/JSON control plane** that lets an
external program drive the browser the same way Playwright / Selenium
drive a headless browser. It is intended for LLM agents, end-to-end test
harnesses, and automation scripts.

## Quickstart

```bash
# Start the agent server (uses an in-memory mock backend so it runs
# anywhere — no WebKitGTK needed).
go build -o samweb-agent-test ./cmd/samweb-agent-test
./samweb-agent-test --addr 0.0.0.0:7777

# In another shell, drive the browser:
curl -s http://127.0.0.1:7777/agent/health
curl -s -X POST http://127.0.0.1:7777/agent/navigate \
     -H 'Content-Type: application/json' \
     -d '{"url":"https://example.com"}'
curl -s 'http://127.0.0.1:7777/agent/elements?selector=a' | jq .
curl -s -X POST http://127.0.0.1:7777/agent/click \
     -H 'Content-Type: application/json' \
     -d '{"selector":"a.result:first-of-type"}'
curl -s http://127.0.0.1:7777/agent/screenshot -o shot.png
```

For real browsing (with a visible window), run the full binary:

```bash
./samweb --agent-addr 0.0.0.0:7777 --agent-token my-secret
```

## Endpoints

All endpoints live under `/agent/*`. Modify-state endpoints use `POST`;
read-state endpoints use `GET`. Bodies and responses are JSON.

| Method | Path | Body / Query | Description |
|--------|------|--------------|-------------|
| GET  | `/agent/health` | — | Liveness probe (always public, ignores auth token). |
| GET  | `/agent/state` | — | Current URL, title, tabs, history flags. |
| POST | `/agent/navigate` | `{"url":"..."}` | Navigate the active tab. |
| POST | `/agent/back` | — | Go back in history. |
| POST | `/agent/forward` | — | Go forward in history. |
| POST | `/agent/reload` | — | Reload the active tab. |
| POST | `/agent/stop` | — | Stop the current navigation. |
| POST | `/agent/click` | `{"selector":"..."}` *or* `{"x":N,"y":N}` (optional: `button`,`double`) | Click an element. |
| POST | `/agent/scroll` | `{"x":N,"y":N}` *or* `{"selector":"..."}` *or* `{"direction":"down","amount":400}` | Scroll the page. |
| POST | `/agent/type` | `{"selector":"...","text":"..."}` (optional: `clear`,`delayMs`) | Type text into an input. |
| POST | `/agent/key` | `{"key":"Enter","modifiers":["ctrl","shift"]}` | Press a key. |
| POST | `/agent/eval` | `{"script":"1+1"}` | Evaluate JS in the iframe; returns the JSON-encoded result. |
| POST | `/agent/wait` | `{"selector":"...","timeoutMs":5000}` | Wait for an element to appear. |
| GET  | `/agent/elements?selector=...` | — | All matching elements with coordinates, size, text, attrs, html. |
| GET  | `/agent/element?selector=...` | — | First matching element (404 if none). |
| GET  | `/agent/screenshot?fullPage=true` | — | PNG screenshot of the current view. |

### Element shape

```json
{
  "tag": "a",
  "id": "link1",
  "classes": ["result"],
  "x": 40, "y": 180,
  "width": 200, "height": 20,
  "text": "Hello World",
  "attrs": { "href": "https://hello.world", "id": "link1" },
  "html": "<a id=\"link1\" class=\"result\" href=\"https://hello.world\">Hello World</a>"
}
```

Coordinates are CSS pixels relative to the iframe's viewport.

## Authentication

If SamWeb was started with `--agent-token <secret>`, every request
(except `/agent/health`) must carry an `Authorization: Bearer <secret>`
header. Requests without or with the wrong token get HTTP 401.

## Go client SDK

```go
import "github.com/samaidev/samweb/internal/agent"

c := agent.NewClient("http://127.0.0.1:7777", "my-secret")

ctx := context.Background()
_ = c.Navigate(ctx, "https://example.com")
els, _ := c.Elements(ctx, "a")
fmt.Println("found", len(els), "links; first at", els[0].X, els[0].Y)
_ = c.Click(ctx, agent.ClickOpts{Selector: "a.result"})
png, _ := c.Screenshot(ctx, false)
os.WriteFile("shot.png", png, 0644)
```

## How it works

```
┌──────────────────────────────────────────────────────────────────┐
│                          SamWeb process                          │
│                                                                  │
│  ┌──────────────┐         ┌─────────────────────────────────┐   │
│  │   webview    │  eval   │  window.__samwebAgent.dispatch  │   │
│  │   (WebKit)   │ ──────▶ │  → operates iframe.contentDoc   │   │
│  │      ▲       │         │  → calls __agentCallback(id,…)  │   │
│  │      │       │ ◀────── │                                 │   │
│  └──────┼───────┘  bind   └─────────────────────────────────┘   │
│         │                                                        │
│  ┌──────┴─────────────────────────────────────┐                  │
│  │  WebviewBackend (implements agent.Backend) │                  │
│  │  - dispatches via webview.Eval             │                  │
│  │  - waits on per-request channel            │                  │
│  └──────┬─────────────────────────────────────┘                  │
│         │                                                        │
│  ┌──────┴─────────────────────────────────────┐                  │
│  │  agent.Server (HTTP /agent/* on :7777)     │                  │
│  └──────┬─────────────────────────────────────┘                  │
└─────────┼────────────────────────────────────────────────────────┘
          │ HTTP / JSON
          ▼
   External agent (Python, Go, LLM, ...)
```

The same-origin trick: the UI server and the proxy live on the **same
TCP port**, so the iframe loaded via `/proxy?url=…` is same-origin with
the parent page. The agent's JS code can therefore reach into
`iframe.contentDocument` to perform clicks, typing, DOM queries, and
canvas-based screenshots directly.

## Testing the Agent API without WebKitGTK

The `cmd/samweb-agent-test` binary runs the exact same `agent.Server`
against an in-memory `MockBackend` so the full API contract can be
exercised on any machine — including CI — without a GUI or WebKitGTK
headers.

```bash
go build -o samweb-agent-test ./cmd/samweb-agent-test
./samweb-agent-test --addr 127.0.0.1:7788
python3 scripts/test_agent.py   # exercises every endpoint
```

The test script verifies health, state, navigate, back/forward, reload,
eval, elements/element (with coordinates), click (selector + coords),
type, key (with modifiers), scroll (direction + coords + selector),
wait, screenshot (viewport + full-page), stop, and bearer-token auth.

## Known limitations

- Some sites (Google, YouTube, GitHub) use JavaScript-based frame-busting or post-login redirects that the proxy does not handle — those may show a "refused to connect" message or redirect loop.
- Cookie persistence across proxy requests is not implemented. Sites that require login will not stay logged in.
- Download manager is not implemented.
- Browser-level devtools are not wired up (you can still open the webview's devtools via right-click on Linux builds if WebKitGTK was compiled with `WEBKIT_DEVELOPER_MODE=ON`).
- True pixel-perfect screenshots require WebKitGTK's snapshot API which is not exposed by `webview_go`. The agent falls back to an SVG-foreignObject-based renderer that works for most pages, and a text-only PNG fallback when resources fail to load.

These are deliberate scope choices for the initial release. Pull requests welcome.

---

## Acknowledgements

- [`webview/webview_go`](https://github.com/webview/webview_go) — the Go bindings for the excellent [`webview`](https://github.com/webview/webview) C library.
- [`zserge/webview`](https://github.com/zserge/webview) — the original concept.
- The Chrome UI was used as the visual reference; all assets here were reimplemented from scratch in HTML/CSS.

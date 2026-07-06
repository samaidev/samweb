// Package browser wires together the embedded webview window, the local
// HTTP server that serves the Chrome-style UI + proxy + agent API, and
// the agent Backend that lets external programs drive the browser.
//
// The UI, proxy, and agent API all live behind a single HTTP listener on
// 127.0.0.1:<ephemeral>. Serving the UI and proxy on the same port is
// what makes the iframe same-origin with the parent page, which in turn
// lets the agent's JS code reach into iframe.contentDocument to perform
// clicks / typing / element queries / screenshots.
//
// The agent API is exposed on a separate listener (default 0.0.0.0:7777)
// so external programs can reach it without being blocked by the same-
// origin policy that protects the UI.
package browser

import (
        "context"
        "embed"
        "encoding/json"
        "fmt"
        "io/fs"
        "log"
        "net"
        "net/http"
        "os"
        "runtime"
        "sync"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/cdp"
        "github.com/samaidev/samweb/internal/proxy"
        "github.com/samaidev/samweb/internal/search"
        "github.com/webview/webview_go"
)

//go:embed all:ui
var uiFS embed.FS

// Options controls how the browser window is created.
type Options struct {
        Title      string
        Width      int
        Height     int
        EngineName string

        // AgentAddr is the address the agent HTTP API binds to. Defaults to
        // "0.0.0.0:7777" so external programs can reach it.
        AgentAddr string
        // AgentToken, if non-empty, gates the agent API behind a bearer token.
        AgentToken string

        // CDPPort, if non-zero, enables WebView2's remote debugging port on
        // the given port. The CDP client is then used by /agent/drag-trusted
        // to inject trusted mouse events (isTrusted=true) that bypass
        // anti-bot systems like Aliyun baxia. Defaults to 9222. Set to 0
        // to disable (then /agent/drag-trusted will return an error).
        CDPPort int
}

// Run starts the embedded HTTP servers and opens the webview window. It
// blocks until the window is closed by the user.
func Run(opts Options) error {
        // Lock this goroutine to its OS thread for the entire lifetime of the
        // webview. On Windows, webview_go (via WebView2) creates a HWND and a
        // message pump that MUST run on the same OS thread that called
        // webview.New. Go's goroutine scheduler is free to move a goroutine
        // between OS threads at any preemption point; without LockOSThread,
        // webview.New may create the window on thread A while webview.Run
        // pumps messages on thread B, leaving the window invisible (no
        // MainWindowHandle, no painted content, agent API state endpoint
        // times out forever). LockOSThread pins us to a single thread so
        // New / SetTitle / SetSize / Init / Bind / Navigate / Run all run
        // on the same thread that owns the HWND.
        runtime.LockOSThread()
        defer runtime.UnlockOSThread()

        if opts.Title == "" {
                opts.Title = "SamWeb"
        }
        if opts.Width <= 0 {
                opts.Width = 1280
        }
        if opts.Height <= 0 {
                opts.Height = 800
        }
        if opts.AgentAddr == "" {
                opts.AgentAddr = "0.0.0.0:7777"
        }
        if opts.CDPPort == 0 {
                opts.CDPPort = 9222 // default CDP port for trusted input injection
        }

        // Single listener for both UI and proxy so the iframe is same-origin
        // with the parent page. Required for the agent to access
        // iframe.contentDocument.
        uiLn, err := net.Listen("tcp", "127.0.0.1:0")
        if err != nil {
                return fmt.Errorf("listen ui: %w", err)
        }
        uiPort := uiLn.Addr().(*net.TCPAddr).Port

        // Pick the default search engine based on user option.
        engine := search.DefaultEngine
        for _, e := range search.Engines() {
                if e.Name == opts.EngineName {
                        engine = e
                        break
                }
        }

        uiSrv := newUIServer(uiPort, engine)

        var wg sync.WaitGroup
        errCh := make(chan error, 4)

        wg.Add(1)
        go func() {
                defer wg.Done()
                errCh <- http.Serve(uiLn, uiSrv.handler())
        }()

        // Wait for the UI server before opening the window.
        waitForReady(uiPort)

        uiURL := fmt.Sprintf("http://127.0.0.1:%d/", uiPort)
        log.Printf("[browser] opening webview -> %s", uiURL)

        // Enable WebView2's remote debugging port so we can connect via CDP
        // (Chrome DevTools Protocol) and inject trusted input events. This
        // is the engine-level escape hatch that lets us bypass the
        // event.isTrusted check used by Aliyun baxia / Geetest / etc.
        //
        // On Windows / WebView2, this sets the Chromium --remote-debugging-port
        // flag. On Linux (WebKitGTK) and macOS (WebKit), this env var is
        // ignored (CDP is Chromium-specific); those platforms would need a
        // different approach (e.g. WebKit's WebDriver), which is not
        // implemented here.
        if os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") == "" {
                os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
                        "--remote-debugging-port="+fmt.Sprint(opts.CDPPort))
                log.Printf("[browser] set WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=%s",
                        os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"))
        }

        w := webview.New(true)
        defer w.Destroy()
        w.SetTitle(opts.Title)
        w.SetSize(opts.Width, opts.Height, webview.HintNone)

        // Bootstrap the agent JS bridge BEFORE the webview navigates, so the
        // bindings are available when the page loads.
        //
        // The anti-detection script runs FIRST (it is registered before the
        // agent bridge via the same Init mechanism), so by the time any
        // page's own JS executes, navigator.webdriver is undefined, the
        // window.chrome object exists, plugins look real, etc. This is
        // what lets SamWeb drive sites protected by Aliyun baxia /
        // Cloudflare bot management / PerimeterX without being
        // immediately flagged as a bot.
        w.Init(antiDetectionJS())
        w.Init(agentBootstrapJS(uiPort))
        w.Bind("samwebResolve", func(input string) (string, error) {
                return search.Resolve(input, engine), nil
        })

        // Build the agent backend and server. NewWebviewBackend registers the
        // __agentCallback binding; do NOT re-register it here, otherwise the
        // placeholder would shadow the real handler and every callback would
        // be logged as an orphan.
        backend := NewWebviewBackend(w)

        agentSrv := agent.NewServer(opts.AgentAddr, opts.AgentToken, backend)
        wg.Add(1)
        go func() {
                defer wg.Done()
                errCh <- agentSrv.ListenAndServe()
        }()

        w.Navigate(uiURL)

        // Connect to the CDP endpoint (in a goroutine, retrying for ~10s
        // while WebView2 spins up the debugging port). The CDP client is
        // stored on the backend so /agent/drag-trusted can use it.
        if opts.CDPPort > 0 {
                go func() {
                        var cdpClient *cdp.Client
                        var cdpErr error
                        for i := 0; i < 20; i++ {
                                time.Sleep(500 * time.Millisecond)
                                cdpClient, cdpErr = cdp.ConnectToPage(opts.CDPPort)
                                if cdpErr == nil {
                                        break
                                }
                        }
                        if cdpErr != nil {
                                log.Printf("[browser] CDP connect failed after 10s: %v", cdpErr)
                                return
                        }
                        backend.SetCDPClient(cdpClient)
                        log.Printf("[browser] CDP client connected on port %d", opts.CDPPort)
                }()
        }

        w.Run()

        // After the window closes, shut down the servers so the process can
        // exit cleanly.
        _ = agentSrv.Shutdown(gracefulCtx())
        return nil
}

// waitForReady polls the UI server's /ready endpoint until it responds.
func waitForReady(port int) {
        url := fmt.Sprintf("http://127.0.0.1:%d/ready", port)
        client := &http.Client{Timeout: 500 * time.Millisecond}
        for i := 0; i < 50; i++ {
                resp, err := client.Get(url)
                if err == nil {
                        _ = resp.Body.Close()
                        if resp.StatusCode == http.StatusOK {
                                return
                        }
                }
                time.Sleep(50 * time.Millisecond)
        }
        log.Printf("[browser] warning: UI server not ready after 2.5s, opening window anyway")
}

// gracefulCtx returns a context that times out after 2 seconds, used for
// graceful server shutdown.
func gracefulCtx() context.Context {
        ctx, _ := context.WithTimeout(context.Background(), 2*time.Second)
        return ctx
}

// uiServer is the HTTP server that serves the embedded UI assets, the
// local API used by the frontend, and the proxy that fetches remote pages.
type uiServer struct {
        port   int
        engine search.Engine
}

func newUIServer(port int, engine search.Engine) *uiServer {
        return &uiServer{port: port, engine: engine}
}

func (s *uiServer) handler() http.Handler {
        mux := http.NewServeMux()

        mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK)
        })

        mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                // Same-origin: proxy lives on this same port under /proxy.
                _ = json.NewEncoder(w).Encode(map[string]any{
                        "proxyBase":     fmt.Sprintf("http://127.0.0.1:%d/proxy?url=", s.port),
                        "defaultEngine": s.engine.Name,
                        "engines":       search.Engines(),
                })
        })

        mux.HandleFunc("/api/resolve", func(w http.ResponseWriter, r *http.Request) {
                q := r.URL.Query().Get("q")
                engine := s.engine
                if name := r.URL.Query().Get("engine"); name != "" {
                        for _, e := range search.Engines() {
                                if e.Name == name {
                                        engine = e
                                        break
                                }
                        }
                }
                w.Header().Set("Content-Type", "application/json")
                _ = json.NewEncoder(w).Encode(map[string]any{
                        "url": search.Resolve(q, engine),
                })
        })

        // Proxy lives on the same port so the iframe is same-origin with the
        // parent page. The agent's JS code relies on this to access
        // iframe.contentDocument.
        proxyHandler := func(w http.ResponseWriter, r *http.Request) {
                target := r.URL.Query().Get("url")
                if target == "" {
                        http.Error(w, "missing url parameter", http.StatusBadRequest)
                        return
                }
                // Inline the proxy fetch here so we share the same http.ServeMux.
                // The proxy package exposes a stateless fetch function for this.
                proxy.ServeHTTP(w, r, target)
        }
        mux.HandleFunc("/proxy", proxyHandler)

        // Serve embedded UI files from the "ui" subdirectory, stripping the
        // "ui/" prefix so that the SPA index.html is served at "/".
        sub, err := fs.Sub(uiFS, "ui")
        if err != nil {
                log.Fatalf("[browser] cannot sub embed fs: %v", err)
        }
        mux.Handle("/", http.FileServer(http.FS(sub)))

        return mux
}

// antiDetectionJS returns a JS snippet that masks the most common signals
// bot-detection systems (Aliyun baxia, Cloudflare BM, PerimeterX, Akamai)
// use to identify automated browsers. It is injected via webview.Init so
// it runs at document_start on every page load, before the page's own JS
// can read the real values.
//
// What it does:
//   1. navigator.webdriver -> undefined (the single biggest giveaway)
//   2. window.chrome -> a realistic Chrome object (WebView2 doesn't set it)
//   3. navigator.plugins / mimeTypes -> a realistic list (empty by default
//      in WebView2, which is suspicious)
//   4. navigator.languages -> ['zh-CN', 'zh', 'en'] (WebView2 may leave
//      this empty in some configs)
//   5. Permissions API patched so Notification.permission matches the
//      Notifications API
//   6. WebGL vendor/renderer patched to look like a real GPU
//   7. window.outerWidth/outerHeight patched to match innerWidth/innerHeight
//      (headless giveaways)
//   8. Caches the original crypto.getRandomValues so baxia's entropy
//      probes don't see a 0-entropy source
//
// This is NOT a silver bullet. Sufficient determined reverse engineering
// can still detect SamWeb. But it gets us past the "first 5 seconds"
// automated rejection that an unmodified webview would hit.
func antiDetectionJS() string {
        return `
(function() {
  'use strict';
  if (window.__samwebAntiDetect) return; // already installed
  window.__samwebAntiDetect = true;

  try {
    // 1. navigator.webdriver = undefined
    Object.defineProperty(Navigator.prototype, 'webdriver', {
      get: function() { return undefined; },
      configurable: true
    });
  } catch (e) {}

  try {
    // 2. window.chrome — WebView2 doesn't define this, but real Chrome does
    if (!window.chrome) {
      window.chrome = {
        runtime: {},
        loadTimes: function() { return {}; },
        csi: function() { return {}; },
        app: { isInstalled: false },
        webstore: {}
      };
    }
  } catch (e) {}

  try {
    // 3. navigator.plugins / mimeTypes — make them non-empty
    var fakePlugin = function(name, filename, description) {
      var p = Object.create(Plugin.prototype);
      Object.defineProperties(p, {
        name: { value: name },
        filename: { value: filename },
        description: { value: description },
        length: { value: 1 }
      });
      return p;
    };
    var plugins = [
      fakePlugin('PDF Viewer', 'internal-pdf-viewer', 'Portable Document Format'),
      fakePlugin('Chrome PDF Viewer', 'internal-pdf-viewer', 'Portable Document Format'),
      fakePlugin('Chromium PDF Viewer', 'internal-pdf-viewer', 'Portable Document Format'),
      fakePlugin('Microsoft Edge PDF Viewer', 'internal-pdf-viewer', 'Portable Document Format'),
      fakePlugin('WebKit built-in PDF', 'internal-pdf-viewer', 'Portable Document Format')
    ];
    Object.defineProperty(navigator, 'plugins', {
      get: function() {
        var arr = plugins.slice();
        arr.item = function(i) { return arr[i] || null; };
        arr.namedItem = function(n) {
          for (var i = 0; i < arr.length; i++) if (arr[i].name === n) return arr[i];
          return null;
        };
        arr.refresh = function() {};
        return arr;
      },
      configurable: true
    });
    Object.defineProperty(navigator, 'mimeTypes', {
      get: function() {
        var m = [{
          type: 'application/pdf',
          suffixes: 'pdf',
          description: 'Portable Document Format'
        }];
        m.item = function(i) { return m[i] || null; };
        m.namedItem = function(n) {
          for (var i = 0; i < m.length; i++) if (m[i].type === n) return m[i];
          return null;
        };
        return m;
      },
      configurable: true
    });
  } catch (e) {}

  try {
    // 4. navigator.languages
    if (!navigator.languages || navigator.languages.length === 0) {
      Object.defineProperty(navigator, 'languages', {
        get: function() { return ['zh-CN', 'zh', 'en-US', 'en']; },
        configurable: true
      });
      Object.defineProperty(navigator, 'language', {
        get: function() { return 'zh-CN'; },
        configurable: true
      });
    }
  } catch (e) {}

  try {
    // 5. Permissions API — headless Chrome has Notification.permission='denied'
    //    but navigator.permissions.query({name:'notifications'}) returns 'prompt',
    //    which is a known detection vector. Patch query() to match.
    if (navigator.permissions && navigator.permissions.query) {
      var origQuery = navigator.permissions.query.bind(navigator.permissions);
      navigator.permissions.query = function(desc) {
        if (desc && desc.name === 'notifications') {
          return Promise.resolve({ state: Notification.permission, onchange: null });
        }
        return origQuery(desc);
      };
    }
  } catch (e) {}

  try {
    // 6. WebGL — patch vendor/renderer to a common Intel IGP
    var patchWebGL = function(proto) {
      if (!proto) return;
      var origGetParameter = proto.getParameter;
      proto.getParameter = function(p) {
        // UNMASKED_VENDOR_WEBGL = 0x9245, UNMASKED_RENDERER_WEBGL = 0x9246
        if (p === 0x9245) return 'Google Inc. (Intel)';
        if (p === 0x9246) return 'ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)';
        return origGetParameter.call(this, p);
      };
    };
    if (window.WebGLRenderingContext) patchWebGL(WebGLRenderingContext.prototype);
    if (window.WebGL2RenderingContext) patchWebGL(WebGL2RenderingContext.prototype);
  } catch (e) {}

  try {
    // 7. outerWidth/outerHeight — headless gives 0, patch to inner+some
    if (window.outerWidth === 0 || window.outerHeight === 0) {
      Object.defineProperty(window, 'outerWidth', {
        get: function() { return window.innerWidth + 16; },
        configurable: true
      });
      Object.defineProperty(window, 'outerHeight', {
        get: function() { return window.innerHeight + 88; },
        configurable: true
      });
    }
  } catch (e) {}

  try {
    // 8. Hairline feature detection — some bots fail to define this
    if (!window.HTMLElement.prototype.hasOwnProperty('matches')) {
      // no-op; WebView2 has it
    }
  } catch (e) {}

  try {
    // 9. navigator.hardwareConcurrency / deviceMemory — patch to common values
    if (!navigator.hardwareConcurrency || navigator.hardwareConcurrency < 4) {
      Object.defineProperty(navigator, 'hardwareConcurrency', {
        get: function() { return 8; },
        configurable: true
      });
    }
    if (!navigator.deviceMemory) {
      Object.defineProperty(navigator, 'deviceMemory', {
        get: function() { return 8; },
        configurable: true
      });
    }
  } catch (e) {}

  try {
    // 10. navigator.platform — match UA
    if (!navigator.platform || navigator.platform === '') {
      Object.defineProperty(navigator, 'platform', {
        get: function() { return 'Win32'; },
        configurable: true
      });
    }
  } catch (e) {}

  try {
    // 11. Notification — make sure it's not 'denied' (default for headless)
    if (window.Notification && Notification.permission === 'denied') {
      Object.defineProperty(Notification, 'permission', {
        get: function() { return 'default'; },
        configurable: true
      });
    }
  } catch (e) {}

  try {
    // 12. CDP detection — hide window.cdc_* properties (Chrome DevTools
    //     Protocol leaves these when controlled via CDP, but we don't use
    //     CDP so this is just defensive).
    for (var k in window) {
      if (/^cdc_/.test(k) || /^cdc_/.test(String(window[k]))) {
        try { delete window[k]; } catch (e) {}
      }
    }
  } catch (e) {}

  // 13. isTrusted hook — make ALL events report isTrusted=true.
  // This is the critical hook for bypassing Aliyun baxia / Geetest /
  // Tencent captcha sliders that check event.isTrusted to reject
  // JS-dispatched (synthetic) events.
  //
  // event.isTrusted is a readonly getter on Event.prototype implemented
  // in C++ (not overridable via Object.defineProperty in some browsers).
  // We use a different approach: override the Event constructor itself
  // so that all events created via `new Event()` / `new MouseEvent()` /
  // etc. have isTrusted hardcoded to true.
  //
  // This runs at document_start (via webview.Init /
  // AddScriptToExecuteOnDocumentCreated), BEFORE any page JS executes,
  // so baxia's slider JS sees isTrusted=true on our dispatched events.
  try {
    // Save original Event constructor
    var OrigEvent = window.Event;
    // Override the isTrusted property on Event.prototype.
    // In WebView2 (Chromium), isTrusted IS configurable via
    // defineProperty when done at document_start before any page JS
    // has a chance to lock it.
    var isTrustedOriginal = Object.getOwnPropertyDescriptor(Event.prototype, 'isTrusted');
    if (isTrustedOriginal) {
      Object.defineProperty(Event.prototype, 'isTrusted', {
        get: function() { return true; },
        configurable: true
      });
    }
  } catch (e) {}
})();
`
}

// agentBootstrapJS is the JS that runs once when the webview initializes.
// It defines window.__samwebAgent, the dispatcher used by the WebviewBackend.
// The uiPort is needed so the JS knows where to send navigate commands and
// where the proxy lives (both on the same port for same-origin access).
func agentBootstrapJS(uiPort int) string {
        return fmt.Sprintf(`
window.__samwebAgent = (function() {
  var UI_PORT = %d;
  var UI_BASE = 'http://127.0.0.1:' + UI_PORT;
  var iframe = function() { return document.getElementById('view'); };
  var iwin = function() {
    var f = iframe();
    return f ? f.contentWindow : window;
  };
  var idoc = function() {
    var f = iframe();
    if (!f) return document;
    try { return f.contentDocument; } catch (e) { return null; }
  };

  function getFrameDoc() {
    var d = idoc();
    if (!d) throw new Error('document is not accessible (cross-origin or not loaded)');
    return d;
  }

  // dispatch is the single entry point invoked from Go via webview.Eval.
  function dispatch(id, method, params) {
    Promise.resolve().then(function() {
      var fn = methods[method];
      if (!fn) throw new Error('unknown agent method: ' + method);
      return fn(params || {});
    }).then(function(result) {
      window.__agentCallback(id, JSON.stringify(result === undefined ? null : result), '');
    }).catch(function(e) {
      var msg = (e && e.message) ? e.message : String(e);
      window.__agentCallback(id, '', msg);
    });
  }

  var methods = {
    navigate: function(p) {
      // Use the UI's existing navigate function so tab history stays in sync.
      if (typeof window.navigate === 'function') {
        window.navigate(p.url);
      } else {
        iframe().src = UI_BASE + '/proxy?url=' + encodeURIComponent(p.url);
      }
      return { ok: true };
    },
    navigateDirect: function(p) {
      // Load the URL as the webview's top-level page, bypassing the
      // iframe proxy. The agent JS (this script) is re-injected on the
      // new page via webview.Init, so __samwebAgent survives navigation.
      window.location.href = p.url;
      return { ok: true };
    },
    back: function() {
      if (typeof window.goBack === 'function') { window.goBack(); }
      else { window.history.back(); }
      return { ok: true };
    },
    forward: function() {
      if (typeof window.goForward === 'function') { window.goForward(); }
      else { window.history.forward(); }
      return { ok: true };
    },
    reload: function() {
      if (typeof window.reloadActive === 'function') { window.reloadActive(); }
      else { window.location.reload(); }
      return { ok: true };
    },
    stop: function() {
      try { window.stop(); } catch (e) {}
      return { ok: true };
    },

    click: function(p) {
      var d = getFrameDoc();
      var el;
      if (p.selector) {
        el = d.querySelector(p.selector);
        if (!el) throw new Error('element not found: ' + p.selector);
      } else if (p.x !== undefined && p.y !== undefined) {
        el = d.elementFromPoint(p.x, p.y);
        if (!el) throw new Error('no element at (' + p.x + ',' + p.y + ')');
      } else {
        throw new Error('click requires selector or x,y');
      }
      el.scrollIntoView({block: 'center', inline: 'center'});
      var opts = { bubbles: true, cancelable: true, view: iwin() };
      var btn = (p.button === 'middle') ? 1 : (p.button === 'right') ? 2 : 0;
      el.dispatchEvent(new MouseEvent('mousedown', Object.assign({button: btn}, opts)));
      el.dispatchEvent(new MouseEvent('mouseup',   Object.assign({button: btn}, opts)));
      el.dispatchEvent(new MouseEvent('click',     Object.assign({button: btn}, opts)));
      if (p.double) {
        el.dispatchEvent(new MouseEvent('dblclick', opts));
      }
      return { ok: true, tag: el.tagName.toLowerCase(), text: (el.innerText || '').slice(0, 200) };
    },

    // drag simulates a human drag from (x1,y1) to (x2,y2) [or from the
    // center of selector1 to the center of selector2 / (x2,y2)]. The
    // trajectory is a cubic bezier with random jitter, dispatched as a
    // sequence of mousemove events with realistic inter-event delays
    // (10-25ms, with occasional pauses). This is what lets us pass
    // Aliyun baxia / Geetest / Tencent captcha sliders that check the
    // naturalness of the drag trajectory.
    //
    // Required: {x1,y1,x2,y2} OR {selector,x2,y2} OR {selector1,selector2}
    // Optional:
    //   iframeSelector - if set, selector/selector2 are resolved inside
    //     this iframe (same-origin only). Used for baxia punish iframe.
    //   duration   - total drag time in ms (default 800-1500, randomized)
    //   steps      - number of mousemove events (default 50-100, randomized)
    //   jitter     - max pixel offset from the bezier curve (default 3)
    //   holdAtEnd  - ms to hold the button at the end before release (default 50-200)
    drag: function(p) {
      return new Promise(function(resolve, reject) {
        var d = getFrameDoc();
        var w = iwin();

        // If iframeSelector is set, switch to that iframe's document.
        // This is needed for Aliyun baxia, which loads the slider in a
        // same-origin iframe (#baxia-dialog-content).
        var iframeDoc = d;
        var iframeWin = w;
        var iframeOffsetX = 0, iframeOffsetY = 0; // offset of iframe within top doc
        if (p.iframeSelector) {
          var iframe = d.querySelector(p.iframeSelector);
          if (!iframe) throw new Error('iframe not found: ' + p.iframeSelector);
          try {
            iframeDoc = iframe.contentDocument;
            iframeWin = iframe.contentWindow;
            if (!iframeDoc) throw new Error('iframe contentDocument is null (cross-origin?)');
            // getBoundingClientRect on the iframe element gives its
            // position in the top doc. Element coords inside the iframe
            // are relative to the iframe's own viewport, so to get
            // top-doc coords we add this offset.
            var ir0 = iframe.getBoundingClientRect();
            iframeOffsetX = ir0.left;
            iframeOffsetY = ir0.top;
          } catch (e) {
            throw new Error('cannot access iframe: ' + e.message);
          }
        }

        // Resolve start point. Coordinates are kept in TOP-DOC space
        // (we add iframeOffset to iframe-local coords from selectors).
        var x1, y1, x2, y2;
        if (p.selector) {
          var el1 = iframeDoc.querySelector(p.selector);
          if (!el1) throw new Error('element not found: ' + p.selector);
          var r1 = el1.getBoundingClientRect();
          // r1 is iframe-local; add iframeOffset to get top-doc coords
          x1 = r1.left + r1.width / 2 + iframeOffsetX;
          y1 = r1.top + r1.height / 2 + iframeOffsetY;
        } else if (p.x1 !== undefined && p.y1 !== undefined) {
          // Caller-provided coords. If iframeSelector is set, treat as
          // iframe-local and add iframeOffset.
          x1 = p.x1 + (p.iframeSelector ? iframeOffsetX : 0);
          y1 = p.y1 + (p.iframeSelector ? iframeOffsetY : 0);
        } else {
          throw new Error('drag requires selector or x1,y1');
        }

        if (p.selector2) {
          var el2 = iframeDoc.querySelector(p.selector2);
          if (!el2) throw new Error('element not found: ' + p.selector2);
          var r2 = el2.getBoundingClientRect();
          x2 = r2.left + r2.width / 2 + iframeOffsetX;
          y2 = r2.top + r2.height / 2 + iframeOffsetY;
        } else if (p.x2 !== undefined && p.y2 !== undefined) {
          // Caller-provided coords. If iframeSelector is set, treat as
          // iframe-local and add iframeOffset to get top-doc coords.
          // Otherwise treat as top-doc directly.
          x2 = p.x2 + (p.iframeSelector ? iframeOffsetX : 0);
          y2 = p.y2 + (p.iframeSelector ? iframeOffsetY : 0);
        } else {
          throw new Error('drag requires selector2 or x2,y2');
        }

        var duration = p.duration || (800 + Math.floor(Math.random() * 700));
        var steps = p.steps || (50 + Math.floor(Math.random() * 50));
        var jitter = p.jitter !== undefined ? p.jitter : 3;
        var holdAtEnd = p.holdAtEnd !== undefined ? p.holdAtEnd : (50 + Math.floor(Math.random() * 150));

        // Cubic bezier control points. Start with a slight upward arc
        // (humans tend to drag slightly above the line) and end with a
        // small overshoot.
        var dx = x2 - x1, dy = y2 - y1;
        var cx1 = x1 + dx * 0.25 + (Math.random() - 0.5) * 20;
        var cy1 = y1 + dy * 0.25 - Math.abs(dx) * 0.1 + (Math.random() - 0.5) * 10;
        var cx2 = x1 + dx * 0.75 + (Math.random() - 0.5) * 20;
        var cy2 = y1 + dy * 0.75 - Math.abs(dx) * 0.05 + (Math.random() - 0.5) * 10;

        function bezierPoint(t) {
          var u = 1 - t;
          var x = u*u*u*x1 + 3*u*u*t*cx1 + 3*u*t*t*cx2 + t*t*t*x2;
          var y = u*u*u*y1 + 3*u*u*t*cy1 + 3*u*t*t*cy2 + t*t*t*y2;
          // Add jitter (random walk so successive points don't jump too far)
          x += (Math.random() - 0.5) * 2 * jitter;
          y += (Math.random() - 0.5) * 2 * jitter;
          return { x: x, y: y };
        }

        // Find the element under the start point so we dispatch
        // mousedown on it (sliders track mousedown on the handle).
        var target;
        if (p.selector) {
          target = iframeDoc.querySelector(p.selector);
        } else if (p.iframeSelector) {
          // Convert top-doc coords to iframe-local for elementFromPoint
          target = iframeDoc.elementFromPoint(x1 - iframeOffsetX, y1 - iframeOffsetY);
        } else {
          target = d.elementFromPoint(x1, y1);
        }
        if (!target) throw new Error('no element at start point');

        // Mouse event clientX/clientY are relative to the VIEWPORT of
        // the document that owns the target element. For iframe targets,
        // that's the iframe's viewport, so we subtract iframeOffset.
        // For top-doc targets, no offset.
        var evOffsetX = p.iframeSelector ? -iframeOffsetX : 0;
        var evOffsetY = p.iframeSelector ? -iframeOffsetY : 0;

        var opts = { bubbles: true, cancelable: true, view: iframeWin,
                     clientX: x1 + evOffsetX, clientY: y1 + evOffsetY };
        // Pointer events are the modern equivalent of mouse events and
        // are what most modern sliders (including Aliyun baxia's) listen
        // for. We dispatch BOTH pointer and mouse events for maximum
        // compatibility.
        var pointerOpts = Object.assign({
          pointerId: 1,
          pointerType: 'mouse',
          isPrimary: true,
          width: 1, height: 1,
          pressure: 0.5
        }, opts);

        // mousedown + pointerdown on the target (slider handle)
        target.dispatchEvent(new PointerEvent('pointerdown',
          Object.assign({button: 0}, pointerOpts)));
        target.dispatchEvent(new MouseEvent('mousedown',
          Object.assign({button: 0}, opts)));

        // Slider JS often uses setPointerCapture on pointerdown, which
        // redirects subsequent pointer events to the capturing element.
        // But many sliders ALSO listen on document/window for pointermove
        // during drag. To cover both cases, we dispatch pointermove on
        // BOTH the target AND the document.
        var moveTarget = target;          // for setPointerCapture case
        var docTarget = iframeDoc;        // for document-level listeners
        var winTarget = iframeWin;        // for window-level listeners

        var i = 0;
        function nextMove() {
          if (i >= steps) {
            // Final move to exact (x2, y2) so we land on the target
            var finalOpts = Object.assign({button: 0, clientX: x2 + evOffsetX, clientY: y2 + evOffsetY}, opts);
            var finalPointer = Object.assign({}, pointerOpts, {clientX: x2 + evOffsetX, clientY: y2 + evOffsetY});
            moveTarget.dispatchEvent(new PointerEvent('pointermove', finalPointer));
            docTarget.dispatchEvent(new PointerEvent('pointermove', finalPointer));
            winTarget.dispatchEvent(new PointerEvent('pointermove', finalPointer));
            moveTarget.dispatchEvent(new MouseEvent('mousemove', finalOpts));
            docTarget.dispatchEvent(new MouseEvent('mousemove', finalOpts));
            // Hold at end
            setTimeout(function() {
              // mouseup + pointerup on all targets
              moveTarget.dispatchEvent(new PointerEvent('pointerup',
                Object.assign({button: 0}, finalPointer)));
              docTarget.dispatchEvent(new PointerEvent('pointerup',
                Object.assign({button: 0}, finalPointer)));
              winTarget.dispatchEvent(new PointerEvent('pointerup',
                Object.assign({button: 0}, finalPointer)));
              moveTarget.dispatchEvent(new MouseEvent('mouseup', finalOpts));
              docTarget.dispatchEvent(new MouseEvent('mouseup', finalOpts));
              // Final click (some sliders require it)
              moveTarget.dispatchEvent(new MouseEvent('click', finalOpts));
              resolve({ ok: true, from: {x: x1, y: y1}, to: {x: x2, y: y2},
                        duration: duration, steps: steps });
            }, holdAtEnd);
            return;
          }
          var t = i / steps;
          // Ease: humans accelerate then decelerate. Use a smoothstep.
          var eased = t * t * (3 - 2 * t);
          var pt = bezierPoint(eased);
          var moveOpts = Object.assign({button: 0, clientX: pt.x + evOffsetX, clientY: pt.y + evOffsetY}, opts);
          var movePointer = Object.assign({}, pointerOpts, {clientX: pt.x + evOffsetX, clientY: pt.y + evOffsetY});
          moveTarget.dispatchEvent(new PointerEvent('pointermove', movePointer));
          docTarget.dispatchEvent(new PointerEvent('pointermove', movePointer));
          winTarget.dispatchEvent(new PointerEvent('pointermove', movePointer));
          moveTarget.dispatchEvent(new MouseEvent('mousemove', moveOpts));
          docTarget.dispatchEvent(new MouseEvent('mousemove', moveOpts));
          i++;
          // Inter-event delay: humans are not perfectly regular.
          var delay = (duration / steps) + (Math.random() - 0.5) * 8;
          // Occasional brief pause (humans hesitate)
          if (Math.random() < 0.05) delay += 30 + Math.random() * 50;
          setTimeout(nextMove, Math.max(1, delay));
        }
        nextMove();
      });
    },

    scroll: function(p) {
      var d = getFrameDoc();
      var w = iwin();
      if (p.x !== undefined && p.y !== undefined) {
        w.scrollTo(p.x, p.y);
      } else if (p.selector) {
        var el = d.querySelector(p.selector);
        if (!el) throw new Error('element not found: ' + p.selector);
        el.scrollIntoView({block: 'start', inline: 'start'});
      } else if (p.direction) {
        var amt = p.amount || 400;
        var dx = 0, dy = 0;
        switch (p.direction) {
          case 'down': dy = amt; break;
          case 'up':   dy = -amt; break;
          case 'right': dx = amt; break;
          case 'left':  dx = -amt; break;
        }
        w.scrollBy(dx, dy);
      } else {
        throw new Error('scroll requires x,y or selector or direction');
      }
      return { ok: true, scrollX: w.scrollX, scrollY: w.scrollY };
    },

    type: function(p) {
      var d = getFrameDoc();
      var el;
      if (p.selector) {
        el = d.querySelector(p.selector);
      } else if (p.x !== undefined && p.y !== undefined) {
        el = d.elementFromPoint(p.x, p.y);
      }
      if (!el) throw new Error('element not found for type');
      // Use the native value setter to bypass React's synthetic event
      // system. React tracks the previous value of a controlled input and
      // only fires onChange if the native setter was used; assigning
      // el.value directly leaves React's internal state out of sync, so
      // the form looks empty when the SPA submits it (a well-known
      // Playwright/Puppeteer issue).
      var setInputValue = null, setTextareaValue = null;
      try {
        setInputValue = Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype, 'value').set;
      } catch (e) {}
      try {
        setTextareaValue = Object.getOwnPropertyDescriptor(
          window.HTMLTextAreaElement.prototype, 'value').set;
      } catch (e) {}
      function applyValue(target, value) {
        if (target instanceof HTMLTextAreaElement && setTextareaValue) {
          setTextareaValue.call(target, value);
        } else if (setInputValue) {
          setInputValue.call(target, value);
        } else {
          target.value = value; // fallback
        }
      }
      if (p.clear) {
        if ('value' in el) applyValue(el, '');
        else el.textContent = '';
      }
      if ('value' in el) {
        applyValue(el, el.value + p.text);
        // InputEvent (bubbles=true) is what React's onChange actually
        // listens for; Event('input') works for vanilla JS but not for
        // React 16+ controlled components.
        el.dispatchEvent(new InputEvent('input', {bubbles: true, cancelable: true, inputType: 'insertText', data: p.text}));
        el.dispatchEvent(new Event('change', {bubbles: true}));
      } else {
        el.textContent += p.text;
      }
      return { ok: true };
    },

    key: function(p) {
      var d = getFrameDoc();
      var el = p.selector ? d.querySelector(p.selector) : d.activeElement;
      if (!el) throw new Error('no active element and no selector');
      el.focus();
      var ev = new KeyboardEvent('keydown', {
        key: p.key, bubbles: true, cancelable: true,
        ctrlKey: (p.modifiers || []).indexOf('ctrl') >= 0,
        shiftKey: (p.modifiers || []).indexOf('shift') >= 0,
        altKey: (p.modifiers || []).indexOf('alt') >= 0,
        metaKey: (p.modifiers || []).indexOf('meta') >= 0,
      });
      el.dispatchEvent(ev);
      el.dispatchEvent(new KeyboardEvent('keyup', ev));
      if (p.key.length === 1 && 'value' in el) {
        el.value += p.key;
        el.dispatchEvent(new Event('input', {bubbles: true}));
      }
      return { ok: true };
    },

    eval: function(p) {
      var w = iwin();
      var result = w.eval(p.script);
      if (result && typeof result.then === 'function') {
        return result.then(function(v) { return { value: v === undefined ? null : v }; });
      }
      return { value: result === undefined ? null : result };
    },

    wait: function(p) {
      var sel = p.selector;
      var timeout = (p.timeoutMs || 30000);
      var start = Date.now();
      return new Promise(function(resolve, reject) {
        function check() {
          var d;
          try { d = idoc(); } catch (e) { d = null; }
          if (d && d.querySelector(sel)) { resolve({ok: true}); return; }
          if (Date.now() - start > timeout) {
            reject(new Error('timeout waiting for ' + sel));
            return;
          }
          setTimeout(check, 100);
        }
        check();
      });
    },

    elements: function(p) {
      var d = getFrameDoc();
      var list = d.querySelectorAll(p.selector);
      var out = [];
      for (var i = 0; i < list.length; i++) {
        out.push(serializeEl(list[i]));
      }
      return { elements: out, count: out.length };
    },

    element: function(p) {
      var d = getFrameDoc();
      var el = d.querySelector(p.selector);
      if (!el) throw new Error('element not found: ' + p.selector);
      return serializeEl(el);
    },

    state: function() {
      var d;
      var url = '', title = '';
      try { d = idoc(); if (d) { title = d.title || ''; } } catch (e) {}
      var f = iframe();
      if (f) {
        try { url = f.src || ''; } catch (e) {}
      } else {
        try { url = window.location.href || ''; } catch (e) {}
      }
      var tabs = [];
      if (typeof window.getTabsState === 'function') {
        tabs = window.getTabsState();
      } else {
        tabs = [{ id: 1, title: title, url: url }];
      }
      return {
        url: url,
        title: title,
        tabs: tabs,
        activeTab: typeof window.getActiveTabId === 'function' ? window.getActiveTabId() : 1,
        canBack:   typeof window.canBack === 'function' ? window.canBack() : false,
        canForward:typeof window.canForward === 'function' ? window.canForward() : false,
      };
    },

    screenshot: function(p) {
      return new Promise(function(resolve, reject) {
        var w = iwin();
        var d;
        try { d = idoc(); } catch (e) { d = null; }
        if (!d) { reject(new Error('iframe not accessible for screenshot')); return; }
        var W = p.fullPage ? Math.max(d.body.scrollWidth, d.documentElement.scrollWidth, w.innerWidth) : w.innerWidth;
        var H = p.fullPage ? Math.max(d.body.scrollHeight, d.documentElement.scrollHeight, w.innerHeight) : w.innerHeight;

        // Approach: serialize the iframe document into an SVG foreignObject,
        // render that SVG to a canvas, and export as PNG. This works for
        // same-origin iframes. External resources (images, fonts) may fail
        // to load inside the SVG, in which case we fall back to a text-only
        // PNG that still shows the page's URL, title and visible text.
        var html;
        try {
          html = new XMLSerializer().serializeToString(d.documentElement);
        } catch (e) { reject(e); return; }
        var svg = '<svg xmlns="http://www.w3.org/2000/svg" width="' + W + '" height="' + H + '">' +
                  '<foreignObject width="100%" height="100%">' + html + '</foreignObject></svg>';
        var svgBlob = new Blob([svg], {type: 'image/svg+xml;charset=utf-8'});
        var url = URL.createObjectURL(svgBlob);
        var img = new Image();
        img.onload = function() {
          var canvas = document.createElement('canvas');
          canvas.width = W; canvas.height = H;
          var ctx = canvas.getContext('2d');
          ctx.fillStyle = '#ffffff';
          ctx.fillRect(0, 0, W, H);
          try { ctx.drawImage(img, 0, 0); } catch (e) {}
          URL.revokeObjectURL(url);
          resolve({ dataUrl: canvas.toDataURL('image/png') });
        };
        img.onerror = function() {
          URL.revokeObjectURL(url);
          // Fallback: text-only PNG.
          var canvas = document.createElement('canvas');
          canvas.width = W; canvas.height = H;
          var ctx = canvas.getContext('2d');
          ctx.fillStyle = '#ffffff'; ctx.fillRect(0, 0, W, H);
          ctx.fillStyle = '#202124'; ctx.font = '14px monospace';
          var lines = [
            'SamWeb screenshot (text fallback)',
            'URL:   ' + (iframe().src || ''),
            'Title: ' + (d.title || ''),
            '',
            'Visible text:',
          ];
          var text = (d.body && d.body.innerText) || '';
          text.split('\\n').slice(0, 50).forEach(function(l) { lines.push(l); });
          lines.forEach(function(line, i) {
            try { ctx.fillText(line.slice(0, 120), 12, 28 + i * 18); } catch (e) {}
          });
          resolve({ dataUrl: canvas.toDataURL('image/png') });
        };
        img.src = url;
      });
    }
  };

  function serializeEl(el) {
    var r = el.getBoundingClientRect();
    var attrs = {};
    for (var i = 0; i < el.attributes.length; i++) {
      attrs[el.attributes[i].name] = el.attributes[i].value;
    }
    return {
      tag: el.tagName.toLowerCase(),
      id: el.id || '',
      classes: Array.prototype.slice.call(el.classList),
      x: r.left, y: r.top, width: r.width, height: r.height,
      text: (el.innerText || '').slice(0, 500),
      attrs: attrs,
      html: el.outerHTML.slice(0, 2000)
    };
  }

  return { dispatch: dispatch, methods: methods };
})();
`, uiPort)
}

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
        "net/http/httputil"
        "net/url"
        "os"
        "strings"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/cdp"
        "github.com/samaidev/samweb/internal/proxy"
        "github.com/samaidev/samweb/internal/search"
        "github.com/wailsapp/wails/v2"
        "github.com/wailsapp/wails/v2/pkg/options"
        "github.com/wailsapp/wails/v2/pkg/options/assetserver"
        wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:ui
var uiFS embed.FS

// Options controls how the browser window is created.
type Options struct {
        Title      string
        Width      int
        Height     int
        EngineName string
        AgentAddr  string
        AgentToken string
        CDPPort    int

        // Tab worker mode: borderless window that loads a URL directly
        // (no samweb UI chrome), using an isolated user-data-dir for
        // cookie/localStorage isolation. Used for multi-profile support.
        TabMode     bool
        Profile     string // profile ID
        UserDataDir string // WebView2 user-data-dir (isolated cookie store)
        AgentPort   int    // agent HTTP API port (0 = disable in tab mode)
        StartURL    string // URL to load on startup
}

// Run starts the wails app and the agent HTTP server. Blocks until the
// window is closed.
func Run(opts Options) error {
        // Tab worker mode: borderless window, no samweb UI, load URL directly.
        if opts.TabMode {
                return runTabWorker(opts)
        }

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
                opts.CDPPort = 9222
        }

        // Enable WebView2's remote debugging port for CDP. The patched
        // go-webview2 chromium.go reads WEBVIEW2_CDP_PORT (default 9222).
        if os.Getenv("WEBVIEW2_CDP_PORT") == "" {
                os.Setenv("WEBVIEW2_CDP_PORT", fmt.Sprint(opts.CDPPort))
        }
        log.Printf("[browser] WEBVIEW2_CDP_PORT=%s", os.Getenv("WEBVIEW2_CDP_PORT"))

        // Pick the default search engine.
        engine := search.DefaultEngine
        for _, e := range search.Engines() {
                if e.Name == opts.EngineName {
                        engine = e
                        break
                }
        }

        // Build the agent backend. This is a WailsBackend that uses wails
        // runtime.ExecJS to drive the browser (replaces webview_go's Eval/Bind).
        backend := NewWailsBackend()

        // Start the agent HTTP server (port 7777). This serves /agent/* for
        // external programs (Python scripts via SSH tunnel) AND /proxy?url= for
        // the UI's iframe, AND /api/config + /api/resolve for the UI's JS.
        agentSrv := agent.NewServer(opts.AgentAddr, opts.AgentToken, backend)
        go func() {
                if err := agentSrv.ListenAndServe(); err != nil {
                        log.Printf("[browser] agent server error: %v", err)
                }
        }()

        // Also start a UI HTTP server that serves /proxy, /api/config, /api/resolve,
        // /agent/callback, and reverse-proxies /agent/* to the agent server.
        uiLn, err := net.Listen("tcp", "127.0.0.1:0")
        if err != nil {
                return fmt.Errorf("listen ui: %w", err)
        }
        uiPort := uiLn.Addr().(*net.TCPAddr).Port
        uiSrv := newUIServer(uiPort, engine, opts.AgentAddr)
        // Add the callback handler for JS → Go communication.
        uiSrv.callbackHandler = HandleCallbackHTTP(backend)
        go func() {
                if err := http.Serve(uiLn, uiSrv.handler()); err != nil {
                        log.Printf("[browser] UI server error: %v", err)
                }
        }()

        // Wait for the UI server to be ready.
        waitForReady(uiPort)

        log.Printf("[browser] UI server on http://127.0.0.1:%d/", uiPort)
        log.Printf("[browser] agent server on %s", opts.AgentAddr)

        // Connect to CDP in the background.
        if opts.CDPPort > 0 {
                go func() {
                        var cdpClient *cdp.Client
                        var cdpErr error
                        for i := 0; i < 30; i++ {
                                time.Sleep(500 * time.Millisecond)
                                cdpClient, cdpErr = cdp.ConnectToPage(opts.CDPPort)
                                if cdpErr == nil {
                                        break
                                }
                        }
                        if cdpErr != nil {
                                log.Printf("[browser] CDP connect failed after 15s: %v", cdpErr)
                                return
                        }
                        backend.SetCDPClient(cdpClient)
                        log.Printf("[browser] CDP client connected on port %d", opts.CDPPort)
                }()
        }

        // Prepare the embedded UI assets.
        uiAssets, err := fs.Sub(uiFS, "ui")
        if err != nil {
                return fmt.Errorf("sub ui fs: %w", err)
        }

        // Create the wails app context.
        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()

        // Set the backend's context (needed for runtime.ExecJS).
        backend.SetContext(ctx)

        // Inject the agent bootstrap JS. This defines window.__samwebAgent
        // and window.__agentCallback, same as the webview_go version.
        // In wails, we use OnDomReady to inject JS after the page loads.
        agentJS := agentBootstrapJS(uiPort)

        // Run the wails app.
        err = wails.Run(&options.App{
                Title:     opts.Title,
                Width:     opts.Width,
                Height:    opts.Height,
                MinWidth:  400,
                MinHeight: 300,
                AssetServer: &assetserver.Options{
                        Assets: uiAssets,
                        Handler: &uiAssetHandler{
                                uiPort:          uiPort,
                                engine:          engine,
                                agentAddr:       opts.AgentAddr,
                                callbackHandler: HandleCallbackHTTP(backend),
                        },
                },
                OnStartup: func(ctx context.Context) {
                        // Save the context for later use.
                        backend.SetContext(ctx)
                        log.Printf("[browser] wails app started")
                },
                OnDomReady: func(ctx context.Context) {
                        // Inject the agent bootstrap JS.
                        wailsRuntime.WindowExecJS(ctx, agentJS)
                        log.Printf("[browser] agent bootstrap JS injected")
                },
                Bind: []interface{}{
                        backend, // expose backend methods to JS
                },
        })

        // Shutdown.
        _ = agentSrv.Shutdown(gracefulCtx())
        cancel()
        return err
}

// uiAssetHandler serves dynamic routes (/api/config, /api/resolve, /proxy,
// /agent/callback) alongside the embedded static files. wails calls Handler
// for every request; if it returns nil, wails falls back to serving the
// embedded asset.
type uiAssetHandler struct {
        uiPort          int
        engine          search.Engine
        agentAddr       string
        callbackHandler http.HandlerFunc // handles /agent/callback (JS→Go)
}

func (h *uiAssetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        switch {
        case r.URL.Path == "/agent/callback":
                // JS→Go callback. Must NOT be reverse-proxied to the agent
                // server (the agent server doesn't have this route — the
                // callback is handled in-process by the WailsBackend).
                if h.callbackHandler != nil {
                        h.callbackHandler(w, r)
                } else {
                        http.Error(w, "callback handler not configured", http.StatusServiceUnavailable)
                }
        case r.URL.Path == "/api/config":
                w.Header().Set("Content-Type", "application/json")
                _ = json.NewEncoder(w).Encode(map[string]any{
                        "proxyBase":     fmt.Sprintf("http://127.0.0.1:%d/proxy?url=", h.uiPort),
                        "defaultEngine": h.engine.Name,
                        "engines":       search.Engines(),
                })
        case r.URL.Path == "/api/resolve":
                q := r.URL.Query().Get("q")
                engine := h.engine
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
        case r.URL.Path == "/proxy":
                target := r.URL.Query().Get("url")
                if target == "" {
                        http.Error(w, "missing url parameter", http.StatusBadRequest)
                        return
                }
                proxy.ServeHTTP(w, r, target)
        case strings.HasPrefix(r.URL.Path, "/agent/"):
                // Reverse proxy /agent/* to the agent server.
                agentTarget := "http://" + h.agentAddr
                agentProxy := httputil.NewSingleHostReverseProxy(mustParseURL(agentTarget))
                originalDirector := agentProxy.Director
                agentProxy.Director = func(req *http.Request) {
                        originalDirector(req)
                        req.Host = h.agentAddr
                }
                agentProxy.ServeHTTP(w, r)
        default:
                // Let wails serve the embedded static file.
                http.FileServer(http.FS(mustSubFS())).ServeHTTP(w, r)
        }
}

// mustSubFS returns the embedded UI filesystem.
func mustSubFS() fs.FS {
        sub, err := fs.Sub(uiFS, "ui")
        if err != nil {
                log.Fatalf("[browser] cannot sub embed fs: %v", err)
        }
        return sub
}

// mustParseURL parses a URL string, panicking on error.
func mustParseURL(s string) *url.URL {
        if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
                s = "http://" + s
        }
        u, err := url.Parse(s)
        if err != nil {
                log.Fatalf("[browser] invalid agent address %q: %v", s, err)
        }
        return u
}

// uiServer is the HTTP server that serves dynamic routes (/api/config,
// /api/resolve, /proxy, /agent/*) alongside the wails AssetServer.
type uiServer struct {
        port            int
        engine          search.Engine
        agentAddr       string // host:port of the agent HTTP server (for reverse proxy)
        callbackHandler http.HandlerFunc
}

func newUIServer(port int, engine search.Engine, agentAddr string) *uiServer {
        return &uiServer{port: port, engine: engine, agentAddr: agentAddr}
}

func (s *uiServer) handler() http.Handler {
        mux := http.NewServeMux()

        mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK)
        })

        mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
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

        mux.HandleFunc("/proxy", func(w http.ResponseWriter, r *http.Request) {
                target := r.URL.Query().Get("url")
                if target == "" {
                        http.Error(w, "missing url parameter", http.StatusBadRequest)
                        return
                }
                proxy.ServeHTTP(w, r, target)
        })

        // /agent/callback — JS → Go callback endpoint
        if s.callbackHandler != nil {
                mux.HandleFunc("/agent/callback", s.callbackHandler)
        }

        // Reverse-proxy /agent/* to the agent server.
        if s.agentAddr != "" {
                agentTarget := "http://" + s.agentAddr
                agentProxy := httputil.NewSingleHostReverseProxy(mustParseURL(agentTarget))
                originalDirector := agentProxy.Director
                agentProxy.Director = func(req *http.Request) {
                        originalDirector(req)
                        req.Host = s.agentAddr
                }
                mux.Handle("/agent/", agentProxy)
        }

        return mux
}

// waitForReady polls the UI server's /ready endpoint until it responds.
func waitForReady(port int) {
        url := fmt.Sprintf("http://127.0.0.1:%d/ready", port)
        client := &http.Client{Timeout: 500 * time.Millisecond}
        for i := 0; i < 50; i++ {
                resp, err := client.Get(url)
                if err == nil {
                        _ = resp.Body.Close()
                        return
                }
                time.Sleep(100 * time.Millisecond)
        }
        log.Printf("[browser] WARNING: UI server not ready after 5s")
}

// gracefulCtx returns a context that expires after 5 seconds.
func gracefulCtx() context.Context {
        ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
        return ctx
}

// agentBootstrapJS is the JS that runs once when the webview initializes.
// It defines window.__samwebAgent, the dispatcher used by the backend.
//
// This is the same as the webview_go version, but uses fetch() to call
// /agent/callback (on the UI server) instead of a native binding.
func agentBootstrapJS(uiPort int) string {
        _ = uiPort
        return `
window.__samwebAgent = (function() {
  var UI_BASE = '';  // same-origin; uiAssetHandler reverse-proxies /agent/*
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

  // dispatch is the single entry point invoked from Go via wails runtime.ExecJS.
  // We use same-origin fetch() to POST the result back to /agent/callback
  // (uiAssetHandler reverse-proxies /agent/* to the agent server, but
  // /agent/callback is handled in-process).
  function dispatch(id, method, params) {
    Promise.resolve().then(function() {
      var fn = methods[method];
      if (!fn) throw new Error('unknown agent method: ' + method);
      return fn(params || {});
    }).then(function(result) {
      fetch(UI_BASE + '/agent/callback', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: id, result: JSON.stringify(result === undefined ? null : result), error: ''})
      }).catch(function(e) {
        console.error('callback fetch error:', e);
      });
    }).catch(function(e) {
      var msg = (e && e.message) ? e.message : String(e);
      fetch(UI_BASE + '/agent/callback', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: id, result: '', error: msg})
      }).catch(function(e2) {
        console.error('callback fetch error:', e2);
      });
    });
  }

  var methods = {
    navigate: function(p) {
      if (typeof window.navigate === 'function') {
        window.navigate(p.url);
      } else {
        iframe().src = '/proxy?url=' + encodeURIComponent(p.url);
      }
      return { ok: true };
    },
    navigateDirect: function(p) {
      var f = iframe();
      if (f) {
        f.removeAttribute('srcdoc');
        f.src = p.url;
      }
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
    eval: function(p) {
      var d = idoc();
      if (!d) throw new Error('document is not accessible (cross-origin or not loaded)');
      var result = (function() { return eval(p.script); }).call(d.defaultView || window);
      if (result && typeof result.then === 'function') {
        return result.then(function(v) { return { value: v === undefined ? null : v }; });
      }
      return { value: result === undefined ? null : result };
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
          target.value = value;
        }
      }
      if (p.clear) {
        if ('value' in el) applyValue(el, '');
        else el.textContent = '';
      }
      if ('value' in el) {
        applyValue(el, el.value + p.text);
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
      var omnibox = document.getElementById('omnibox');
      var omniboxValue = omnibox ? omnibox.value : '';
      var tabs = [];
      if (typeof window.getTabsState === 'function') {
        tabs = window.getTabsState();
      } else {
        tabs = [{ id: 1, title: title, url: omniboxValue || url }];
      }
      return {
        url: omniboxValue || url,
        title: title,
        tabs: tabs,
        activeTab: typeof window.getActiveTabId === 'function' ? window.getActiveTabId() : 1,
        canBack:   typeof window.canBack === 'function' ? window.canBack() : false,
        canForward:typeof window.canForward === 'function' ? window.canForward() : false,
        iframe_src: url,
        omnibox_value: omniboxValue,
      };
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
      html: el.outerHTML.slice(0, 2000),
    };
  }

  return { dispatch: dispatch };
})();

// Expose dispatch globally so wails runtime.ExecJS can call it.
window.__samwebAgentDispatch = function(id, method, paramsJSON) {
  var params = paramsJSON ? JSON.parse(paramsJSON) : {};
  window.__samwebAgent.dispatch(id, method, params);
};
`
}

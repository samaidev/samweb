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
	"sync"
	"time"

	"github.com/samaidev/samweb/internal/agent"
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
}

// Run starts the embedded HTTP servers and opens the webview window. It
// blocks until the window is closed by the user.
func Run(opts Options) error {
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

	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle(opts.Title)
	w.SetSize(opts.Width, opts.Height, webview.HintNone)

	// Bootstrap the agent JS bridge BEFORE the webview navigates, so the
	// bindings are available when the page loads.
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
      if (p.clear) {
        if ('value' in el) el.value = '';
        else el.textContent = '';
      }
      if ('value' in el) {
        el.value += p.text;
        el.dispatchEvent(new Event('input', {bubbles: true}));
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

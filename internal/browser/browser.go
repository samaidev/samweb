// Package browser wires together the embedded webview window, the local
// HTTP server that serves the Chrome-style UI, and the proxy server that
// fetches remote pages so they can be rendered inside an iframe.
//
// The UI itself is plain HTML/CSS/JS embedded in the binary via Go's
// embed package (see the ./ui subdirectory of this package). The Go side
// only exposes a tiny HTTP API used by the UI to resolve omnibox input
// into a target URL and to learn where the local proxy is listening.
package browser

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/samaidev/samweb/internal/proxy"
	"github.com/samaidev/samweb/internal/search"
	"github.com/webview/webview_go"
)

//go:embed all:ui
var uiFS embed.FS

// Options controls how the browser window is created.
type Options struct {
	// Title is the OS window title. Defaults to "SamWeb".
	Title string
	// Width / Height of the window in pixels. Defaults to 1280x800.
	Width  int
	Height int
	// EngineName picks the default search engine ("Google", "Bing",
	// "DuckDuckGo", "Baidu"). Empty defaults to Google.
	EngineName string
}

// Run starts the embedded HTTP server and opens the webview window. It
// blocks until the window is closed by the user.
//
// Internally it spins up two HTTP listeners on ephemeral localhost ports:
//   - a UI server that serves the embedded Chrome-style UI
//   - a proxy server that fetches remote pages for the iframe so they are
//     not blocked by X-Frame-Options / CSP frame-ancestors
//
// Both ports are discovered at runtime so the app can coexist with other
// services on the machine.
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

	uiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen ui: %w", err)
	}
	uiPort := uiLn.(*net.TCPAddr).Port

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen proxy: %w", err)
	}
	proxyPort := proxyLn.(*net.TCPAddr).Port

	// Pick the default search engine based on user option.
	engine := search.DefaultEngine
	for _, e := range search.Engines() {
		if e.Name == opts.EngineName {
			engine = e
			break
		}
	}

	uiSrv := newUIServer(uiPort, proxyPort, engine)

	// Start both servers. They share a WaitGroup so we can cleanly shut down
	// if any of them fails to start.
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- http.Serve(uiLn, uiSrv.handler())
	}()

	proxySrv := proxy.New(fmt.Sprintf("127.0.0.1:%d", proxyPort))
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- proxySrv.ListenAndServe()
	}()

	// Wait for the UI server to actually be accepting connections before
	// opening the window, otherwise webview can race the listener.
	waitForReady(uiPort)

	uiURL := fmt.Sprintf("http://127.0.0.1:%d/", uiPort)
	log.Printf("[browser] opening webview -> %s", uiURL)

	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle(opts.Title)
	w.SetSize(opts.Width, opts.Height, webview.HintNone)

	// Expose a small JS <-> Go bridge so the UI can ask the host to resolve
	// omnibox input. The UI also has a pure-JS fallback so it works even if
	// the binding is unavailable.
	w.Bind("samwebResolve", func(input string) (string, error) {
		return search.Resolve(input, engine), nil
	})

	w.Navigate(uiURL)
	w.Run()

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

// uiServer is the HTTP server that serves the embedded UI assets plus the
// small JSON API used by the frontend.
type uiServer struct {
	port      int
	proxyPort int
	engine    search.Engine
}

func newUIServer(port, proxyPort int, engine search.Engine) *uiServer {
	return &uiServer{port: port, proxyPort: proxyPort, engine: engine}
}

func (s *uiServer) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxyBase":     fmt.Sprintf("http://127.0.0.1:%d/proxy?url=", s.proxyPort),
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

	// Serve embedded UI files from the "ui" subdirectory, stripping the
	// "ui/" prefix so that the SPA index.html is served at "/".
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		log.Fatalf("[browser] cannot sub embed fs: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	return mux
}

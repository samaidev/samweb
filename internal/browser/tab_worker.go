package browser

import (
        "context"
        "fmt"
        "io"
        "io/fs"
        "log"
        "net"
        "net/http"
        "os"
        "path/filepath"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/cdp"
        wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

        "github.com/wailsapp/wails/v2/pkg/options"
        "github.com/wailsapp/wails/v2/pkg/options/assetserver"
        wails "github.com/wailsapp/wails/v2"
)

// runTabWorker starts a borderless wails window that loads opts.StartURL
// directly (no samweb UI chrome), using an isolated user-data-dir for
// cookie/localStorage isolation.
//
// This is the "tab worker" process — spawned by the main samweb (the
// coordinator) once per profile. Each tab worker:
//   - has its own WebView2 user-data-dir → isolated cookies + localStorage
//   - has its own CDP port → independent DOM automation
//   - has its own agent HTTP API port → AICQ bridge connects here
//   - loads the StartURL directly (no iframe, no proxy)
//
// On first launch for a profile, the user-data-dir is empty, so the
// tab worker injects the profile's saved cookies + localStorage (from
// ~/.samweb/profiles.json) so the user is logged in. On subsequent
// launches, the user-data-dir already has the login state — no
// injection needed.
func runTabWorker(opts Options) error {
        if opts.StartURL == "" {
                opts.StartURL = "about:blank"
        }
        if opts.Width <= 0 {
                opts.Width = 1000
        }
        if opts.Height <= 0 {
                opts.Height = 700
        }
        if opts.CDPPort == 0 {
                opts.CDPPort = 0 // let OS assign
        }

        // Set WebView2 user-data-dir for cookie/localStorage isolation.
        // We set it via env var AND patch the go-webview2 chromium.go to
        // read it (the default DataPath uses AppData/<exe_name> which is
        // shared across all tab workers).
        if opts.UserDataDir != "" {
                abs, err := filepath.Abs(opts.UserDataDir)
                if err == nil {
                        os.MkdirAll(abs, 0755)
                        os.Setenv("WEBVIEW2_USER_DATA_FOLDER", abs)
                        log.Printf("[tab] user-data-dir: %s", abs)
                }
        }

        // CDP port — set via WEBVIEW2_CDP_PORT env var (read by the
        // patched go-webview2 chromium.go). Each tab worker needs its
        // own CDP port for independent DOM automation.
        cdpPort := opts.CDPPort
        if cdpPort == 0 {
                ln, err := net.Listen("tcp", "127.0.0.1:0")
                if err == nil {
                        cdpPort = ln.Addr().(*net.TCPAddr).Port
                        ln.Close()
                }
        }
        os.Setenv("WEBVIEW2_CDP_PORT", fmt.Sprintf("%d", cdpPort))
        log.Printf("[tab] CDP port: %d (via WEBVIEW2_CDP_PORT)", cdpPort)

        // Build the agent backend
        backend := NewWailsBackend()

        // Start agent HTTP server. In tab mode, we let the OS choose a free
        // port (opts.AgentPort == 0), then write the chosen port to a file
        // so the parent process can discover it.
        agentLn, err := net.Listen("tcp", "127.0.0.1:0")
        if err != nil {
                return fmt.Errorf("listen agent port: %w", err)
        }
        agentPort := agentLn.Addr().(*net.TCPAddr).Port
        agentAddr := fmt.Sprintf("127.0.0.1:%d", agentPort)
        agentLn.Close() // release; the agent server will re-bind immediately

        // Write the agent port to a file so the parent can read it
        if opts.UserDataDir != "" {
                portFile := filepath.Join(opts.UserDataDir, "agent_port")
                os.WriteFile(portFile, []byte(fmt.Sprintf("%d", agentPort)), 0644)
        }

        srv := agent.NewServer(agentAddr, opts.AgentToken, backend)
        go func() {
                if err := srv.ListenAndServe(); err != nil {
                        log.Printf("[tab] agent server error: %v", err)
                }
        }()
        log.Printf("[tab] agent API on %s", agentAddr)

        // Minimal UI assets for tab mode — a blank page. We navigate to the
        // StartURL via CDP after the window starts (more reliable than an
        // HTML redirect, which wails' asset handler may block).
        tabHTML := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Tab</title>
<style>html,body{margin:0;padding:0;width:100%;height:100%;background:#fff}</style>
</head><body></body></html>`

        // Create a custom asset handler that serves the tab HTML
        tabAssets := &tabAssetHandler{html: tabHTML}

        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()
        backend.SetContext(ctx)

        // Connect to CDP in the background after the window starts, then
        // navigate to StartURL + inject profile storage.
        go func() {
                var cdpClient *cdp.Client
                var cdpErr error
                for i := 0; i < 40; i++ {
                        time.Sleep(500 * time.Millisecond)
                        cdpClient, cdpErr = cdp.ConnectToPage(cdpPort)
                        if cdpErr == nil {
                                break
                        }
                }
                if cdpErr != nil {
                        log.Printf("[tab] CDP connect failed after 20s: %v", cdpErr)
                        return
                }
                backend.SetCDPClient(cdpClient)
                log.Printf("[tab] CDP client connected on port %d", cdpPort)

                // Enable Page domain (needed for Page.navigate to work
                // reliably in WebView2)
                _ = cdpClient.EnablePage()

                // Navigate to StartURL. Use Page.navigate, then verify
                // the navigation took effect by checking location.href.
                if opts.StartURL != "" && opts.StartURL != "about:blank" {
                        // Try Page.navigate first
                        if err := cdpClient.Navigate(opts.StartURL); err != nil {
                                log.Printf("[tab] Page.navigate error: %v, trying location.href", err)
                        }
                        time.Sleep(3 * time.Second)
                        // Verify + fallback to location.href
                        origin, _ := cdpClient.CurrentOrigin()
                        if origin == "" || origin == "http://wails.localhost" {
                                log.Printf("[tab] navigate didn't take effect, setting location.href via eval")
                                _, _ = cdpClient.Send("Runtime.evaluate", map[string]interface{}{
                                        "expression": fmt.Sprintf("window.location.href = %q;", opts.StartURL),
                                })
                        }
                        time.Sleep(3 * time.Second)
                        origin2, _ := cdpClient.CurrentOrigin()
                        log.Printf("[tab] navigated to %s (origin now %s)", opts.StartURL, origin2)
                }

                // If this is a fresh user-data-dir, inject the profile's cookies
                // + localStorage so the user is logged in.
                if opts.Profile != "" {
                        injectProfileStorage(cdpClient, opts.Profile)
                }
        }()

        err = wails.Run(&options.App{
                Title:            opts.Title,
                Width:            opts.Width,
                Height:           opts.Height,
                MinWidth:         200,
                MinHeight:        150,
                AssetServer:      &assetserver.Options{
                        Assets:  tabAssets,
                        Handler: tabAssets,
                },
                BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 1},
                OnStartup: func(ctx context.Context) {
                        backend.SetContext(ctx)
                        log.Printf("[tab] wails app started (profile=%s)", opts.Profile)
                },
                Bind: []interface{}{backend},
        })

        _ = srv.Shutdown(context.Background())
        cancel()
        return err
}

// injectProfileStorage loads a profile's cookies + localStorage from
// ~/.samweb/profiles.json and injects them into the current CDP page.
// This is used on first launch of a tab worker for a profile — after
// that, the user-data-dir persists the login state.
func injectProfileStorage(c *cdp.Client, profileID string) {
        prof, ok, err := Profiles().Get(profileID)
        if err != nil || !ok {
                log.Printf("[tab] profile %s not found, skipping storage injection", profileID)
                return
        }

        // Inject cookies
        for _, ck := range prof.Cookies {
                _ = c.SetCookie(ck)
        }
        log.Printf("[tab] injected %d cookies from profile %s", len(prof.Cookies), profileID)

        // Inject localStorage (navigate to each origin, write entries)
        if len(prof.LocalStorage) > 0 {
                for origin, entries := range prof.LocalStorage {
                        if err := c.Navigate(origin + "/"); err != nil {
                                log.Printf("[tab] warning: navigate to %s for localStorage: %v", origin, err)
                                continue
                        }
                        time.Sleep(3 * time.Second)
                        if err := c.RestoreLocalStorage(entries); err != nil {
                                log.Printf("[tab] warning: restore localStorage for %s: %v", origin, err)
                        } else {
                                log.Printf("[tab] restored %d localStorage entries for origin %s", len(entries), origin)
                        }
                }
                // Navigate back to the start URL
                _ = c.Navigate("about:blank")
        }
}

// tabAssetHandler serves a single HTML page (the tab redirect page).
// It implements both http.Handler (for wails Handler) and fs.FS (for
// wails Assets).
type tabAssetHandler struct {
        html string
}

func (h *tabAssetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        // Serve the tab HTML for all requests (including "/" with trailing
        // slash, which wails' default handler rejects).
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        w.Write([]byte(h.html))
}

// Open implements fs.FS. Returns the HTML for any path (the tab page
// is a single page app that redirects to the StartURL).
func (h *tabAssetHandler) Open(name string) (fs.File, error) {
        return &tabFile{content: []byte(h.html)}, nil
}

type tabFile struct {
        content  []byte
        readOnce bool
}

func (f *tabFile) Stat() (fs.FileInfo, error) {
        return &tabFileInfo{size: int64(len(f.content))}, nil
}
func (f *tabFile) Read(p []byte) (int, error) {
        if f.readOnce {
                return 0, io.EOF
        }
        f.readOnce = true
        n := copy(p, f.content)
        return n, io.EOF
}
func (f *tabFile) Close() error { return nil }
func (f *tabFile) Seek(offset int64, whence int) (int64, error) {
        return 0, nil
}
func (f *tabFile) Readdir(count int) ([]fs.FileInfo, error) {
        return nil, nil
}

type tabFileInfo struct {
        size int64
}

func (fi *tabFileInfo) Name() string       { return "index.html" }
func (fi *tabFileInfo) Size() int64        { return fi.size }
func (fi *tabFileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *tabFileInfo) ModTime() time.Time { return time.Now() }
func (fi *tabFileInfo) IsDir() bool        { return false }
func (fi *tabFileInfo) Sys() interface{}   { return nil }

// Suppress unused import warning for wailsRuntime (used in main browser.go)
var _ = wailsRuntime.WindowExecJS

package browser

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "os"
        "path/filepath"
        "sync"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/cdp"
        "github.com/samaidev/samweb/internal/proxy"
        "github.com/webview/webview_go"
)

// cdpCookieFile is the on-disk JSON file for CDP browser cookies.
// Defaults to ~/.samweb/cdp-cookies.json (alongside the proxy cookie jar).
func cdpCookieFile() string {
        home, err := os.UserHomeDir()
        if err != nil || home == "" {
                home = "."
        }
        return filepath.Join(home, ".samweb", "cdp-cookies.json")
}

// saveCDPCookies writes CDP browser cookies to disk (atomic temp+rename).
func saveCDPCookies(cookies []cdp.CDPCookie) error {
        if len(cookies) == 0 {
                return nil
        }
        path := cdpCookieFile()
        if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
                return err
        }
        data, err := json.MarshalIndent(cookies, "", "  ")
        if err != nil {
                return err
        }
        tmp := path + ".tmp"
        if err := os.WriteFile(tmp, data, 0o600); err != nil {
                return err
        }
        return os.Rename(tmp, path)
}

// loadCDPCookies reads CDP browser cookies from disk.
func loadCDPCookies() ([]cdp.CDPCookie, error) {
        data, err := os.ReadFile(cdpCookieFile())
        if err != nil {
                if os.IsNotExist(err) {
                        return nil, nil
                }
                return nil, err
        }
        var cookies []cdp.CDPCookie
        if err := json.Unmarshal(data, &cookies); err != nil {
                return nil, err
        }
        return cookies, nil
}

// WebviewBackend is the production agent.Backend implementation that drives
// a real webview instance. Every method:
//
//  1. Builds a small JS snippet that performs the action inside the
//     embedded iframe (which is same-origin with the UI because both are
//     served from the same port).
//  2. Dispatches the snippet via webview.Eval.
//  3. The JS writes its result back to Go via the __agentCallback binding.
//  4. Go returns the result to the agent HTTP handler.
//
// Because webview.Eval is fire-and-forget (no return value), we use a
// pending-request map keyed by a unique ID. Each request waits on a
// channel that is fulfilled by the callback handler.
type WebviewBackend struct {
        w    webview.WebView
        mu   sync.Mutex
        pend map[string]chan callbackResult

        // cdpMu protects cdpClient. The CDP client is connected lazily after
        // the webview starts (see browser.Run); DragTrusted checks for nil
        // and returns a clear error if CDP is not connected.
        cdpMu     sync.RWMutex
        cdpClient *cdp.Client
}

type callbackResult struct {
        result string
        err    string
}

// NewWebviewBackend constructs a backend for the given webview and
// registers the __agentCallback binding. The caller must have already
// initialized the webview with Init() that defines window.__samwebAgent.
func NewWebviewBackend(w webview.WebView) *WebviewBackend {
        b := &WebviewBackend{
                w:    w,
                pend: map[string]chan callbackResult{},
        }
        w.Bind("__agentCallback", b.handleCallback)
        return b
}

// handleCallback is the Go-side receiver for JS results. It is invoked by
// the webview binding whenever the JS side calls window.__agentCallback(id, result, err).
func (b *WebviewBackend) handleCallback(id, result, err string) {
        b.mu.Lock()
        ch, ok := b.pend[id]
        if ok {
                delete(b.pend, id)
        }
        b.mu.Unlock()
        if !ok {
                // Orphan callback (e.g. the diagnostic ping, or a duplicate). Drop.
                return
        }
        ch <- callbackResult{result: result, err: err}
}

// dispatch sends a method invocation to the JS side and waits for the
// callback. It is the core of every Backend method.
func (b *WebviewBackend) dispatch(ctx context.Context, method string, params interface{}) (string, error) {
        id := newRequestID()
        ch := make(chan callbackResult, 1)
        b.mu.Lock()
        b.pend[id] = ch
        b.mu.Unlock()

        paramsJSON := "null"
        if params != nil {
                bb, err := json.Marshal(params)
                if err != nil {
                        return "", fmt.Errorf("marshal params: %w", err)
                }
                paramsJSON = string(bb)
        }
        // Wrap the dispatch call in try/catch so that if __samwebAgent is
        // undefined (Init JS didn't execute, e.g. on Windows when
        // AddScriptToExecuteOnDocumentCreated fails silently), we get an
        // immediate error back via __agentCallback instead of hanging until
        // the request timeout. This makes Windows-specific Init failures
        // diagnosable.
        js := fmt.Sprintf(`(function(){
  try {
    if (typeof window.__samwebAgent === 'undefined') {
      window.__agentCallback(%q, '', 'window.__samwebAgent is not defined (Init JS did not execute)');
      return;
    }
    window.__samwebAgent.dispatch(%q, %q, %s);
  } catch (e) {
    window.__agentCallback(%q, '', 'dispatch error: ' + (e && e.message ? e.message : String(e)));
  }
})();`, id, id, method, paramsJSON, id)
        // Use Dispatch to ensure the Eval runs on the webview's main thread.
        // Without this, on Windows the Eval may be called from a different
        // goroutine / OS thread than the one that owns the HWND, and
        // ExecuteScript may silently fail to execute the JS.
        b.w.Dispatch(func() {
                b.w.Eval(js)
        })

        select {
        case r := <-ch:
                if r.err != "" {
                        return "", errors.New(r.err)
                }
                return r.result, nil
        case <-ctx.Done():
                b.mu.Lock()
                delete(b.pend, id)
                b.mu.Unlock()
                return "", ctx.Err()
        case <-time.After(60 * time.Second):
                b.mu.Lock()
                delete(b.pend, id)
                b.mu.Unlock()
                return "", errors.New("agent: timeout waiting for webview callback")
        }
}

// dispatchVoid is dispatch for methods that return only {ok:true}.
func (b *WebviewBackend) dispatchVoid(ctx context.Context, method string, params interface{}) error {
        _, err := b.dispatch(ctx, method, params)
        return err
}

// ----------------------------- Backend impl -----------------------------

func (b *WebviewBackend) Navigate(ctx context.Context, url string) error {
        return b.dispatchVoid(ctx, "navigate", map[string]string{"url": url})
}
func (b *WebviewBackend) NavigateDirect(ctx context.Context, url string) error {
        return b.dispatchVoid(ctx, "navigateDirect", map[string]string{"url": url})
}
func (b *WebviewBackend) Back(ctx context.Context) error {
        return b.dispatchVoid(ctx, "back", nil)
}
func (b *WebviewBackend) Forward(ctx context.Context) error {
        return b.dispatchVoid(ctx, "forward", nil)
}
func (b *WebviewBackend) Reload(ctx context.Context) error {
        return b.dispatchVoid(ctx, "reload", nil)
}
func (b *WebviewBackend) Stop(ctx context.Context) error {
        return b.dispatchVoid(ctx, "stop", nil)
}

func (b *WebviewBackend) Click(ctx context.Context, opts agent.ClickOpts) error {
        return b.dispatchVoid(ctx, "click", opts)
}
func (b *WebviewBackend) Scroll(ctx context.Context, opts agent.ScrollOpts) error {
        return b.dispatchVoid(ctx, "scroll", opts)
}
func (b *WebviewBackend) Type(ctx context.Context, opts agent.TypeOpts) error {
        return b.dispatchVoid(ctx, "type", opts)
}
func (b *WebviewBackend) PressKey(ctx context.Context, opts agent.KeyOpts) error {
        return b.dispatchVoid(ctx, "key", opts)
}
func (b *WebviewBackend) Drag(ctx context.Context, opts agent.DragOpts) error {
        return b.dispatchVoid(ctx, "drag", opts)
}

func (b *WebviewBackend) Eval(ctx context.Context, script string) (json.RawMessage, error) {
        out, err := b.dispatch(ctx, "eval", map[string]string{"script": script})
        if err != nil {
                return nil, err
        }
        // out is a JSON string: the JSON-encoded return value of the script.
        return json.RawMessage(out), nil
}

func (b *WebviewBackend) Wait(ctx context.Context, selector string, timeoutMs int) error {
        return b.dispatchVoid(ctx, "wait", map[string]interface{}{
                "selector":  selector,
                "timeoutMs": timeoutMs,
        })
}

func (b *WebviewBackend) Elements(ctx context.Context, selector string) ([]agent.Element, error) {
        out, err := b.dispatch(ctx, "elements", map[string]string{"selector": selector})
        if err != nil {
                return nil, err
        }
        var res agent.ElementsResult
        if err := json.Unmarshal([]byte(out), &res); err != nil {
                return nil, fmt.Errorf("decode elements: %w", err)
        }
        return res.Elements, nil
}

func (b *WebviewBackend) Element(ctx context.Context, selector string) (*agent.Element, error) {
        out, err := b.dispatch(ctx, "element", map[string]string{"selector": selector})
        if err != nil {
                return nil, err
        }
        var el agent.Element
        if err := json.Unmarshal([]byte(out), &el); err != nil {
                return nil, fmt.Errorf("decode element: %w", err)
        }
        return &el, nil
}

func (b *WebviewBackend) State(ctx context.Context) (*agent.State, error) {
        out, err := b.dispatch(ctx, "state", nil)
        if err != nil {
                return nil, err
        }
        var st agent.State
        if err := json.Unmarshal([]byte(out), &st); err != nil {
                return nil, fmt.Errorf("decode state: %w", err)
        }
        return &st, nil
}

func (b *WebviewBackend) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
        out, err := b.dispatch(ctx, "screenshot", map[string]interface{}{"fullPage": fullPage})
        if err != nil {
                return nil, err
        }
        // out is a JSON string wrapping a data URL: "data:image/png;base64,...."
        var dataURL string
        if err := json.Unmarshal([]byte(out), &dataURL); err != nil {
                return nil, fmt.Errorf("decode screenshot data url: %w", err)
        }
        b64, err := parseDataURL(dataURL)
        if err != nil {
                return nil, err
        }
        return b64, nil
}

// ScreenshotTrusted captures the page via CDP Page.captureScreenshot.
// This captures the actual rendered pixels from the WebView2 compositor
// (what the user sees), unlike Screenshot which uses JS SVG foreignObject
// and often fails on complex pages with cross-origin iframes or large DOM.
func (b *WebviewBackend) ScreenshotTrusted(ctx context.Context, fullPage bool) ([]byte, error) {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil, fmt.Errorf("CDP client not connected — start samweb with a non-zero --cdp-port (default 9222)")
        }
        done := make(chan struct {
                data []byte
                err  error
        }, 1)
        go func() {
                data, err := c.Screenshot(fullPage)
                done <- struct {
                        data []byte
                        err  error
                }{data, err}
        }()
        select {
        case r := <-done:
                return r.data, r.err
        case <-ctx.Done():
                return nil, ctx.Err()
        }
}

func (b *WebviewBackend) Close() error { return nil }

// ResetCookies clears both the proxy's cookie jar AND the CDP browser
// cookie store (where navigate-direct cookies live). Call before a
// fresh login attempt.
func (b *WebviewBackend) ResetCookies(ctx context.Context) error {
        proxy.ResetCookies()
        // Also clear the CDP browser cookie store (WebView2's own cookies
        // from navigate-direct navigations).
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c != nil {
                if err := c.ClearCookies(); err != nil {
                        // non-fatal — log and continue
                        _ = err
                }
        }
        return nil
}

// SaveCookies persists BOTH the proxy cookie jar AND the CDP browser
// cookie store to disk. The CDP cookies are saved to a separate file
// (~/.samweb/cdp-cookies.json) and restored on next process start via
// LoadCookies. This is what makes navigate-direct logins persist —
// the proxy jar only captures cookies from /proxy?url= requests, but
// navigate-direct loads pages via WebView2's own network stack, so
// cookies end up in WebView2's cookie store, not the proxy jar.
func (b *WebviewBackend) SaveCookies(ctx context.Context) error {
        // 1. Save proxy jar (for /proxy?url= cookies)
        if err := proxy.SaveCookies(); err != nil {
                return err
        }
        // 2. Save CDP cookies (for navigate-direct cookies)
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil // no CDP — proxy-only mode
        }
        cookies, err := c.GetAllCookies()
        if err != nil {
                return fmt.Errorf("get CDP cookies: %w", err)
        }
        if err := saveCDPCookies(cookies); err != nil {
                return fmt.Errorf("save CDP cookies: %w", err)
        }
        return nil
}

// LoadCookies re-reads both cookie stores from disk: the proxy jar
// (proxy.LoadCookies) and the CDP browser cookie store (via
// Network.setCookie for each saved cookie).
func (b *WebviewBackend) LoadCookies(ctx context.Context) error {
        // 1. Load proxy jar
        if err := proxy.LoadCookies(); err != nil {
                return err
        }
        // 2. Load CDP cookies into the browser store
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil
        }
        cookies, err := loadCDPCookies()
        if err != nil {
                return err
        }
        for _, ck := range cookies {
                if err := c.SetCookie(ck); err != nil {
                        // non-fatal — one bad cookie shouldn't fail the whole load
                        _ = err
                }
        }
        return nil
}

// SetCDPClient stores the CDP client (connected to WebView2's remote
// debugging port). Called by browser.Run after the webview starts.
// Once set, DragTrusted uses it to inject trusted mouse events, and
// SaveCookies/LoadCookies/ResetCookies also operate on the CDP cookie
// store (which is where navigate-direct cookies live, since they bypass
// samweb's proxy).
func (b *WebviewBackend) SetCDPClient(c *cdp.Client) {
        b.cdpMu.Lock()
        defer b.cdpMu.Unlock()
        b.cdpClient = c
}

// DragTrusted injects a human-like drag via CDP's Input.dispatchMouseEvent.
func (b *WebviewBackend) DragTrusted(ctx context.Context, opts agent.TrustedDragOpts) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return fmt.Errorf("CDP client not connected — start samweb with a non-zero --cdp-port (default 9222)")
        }
        done := make(chan error, 1)
        go func() {
                done <- c.Drag(opts.X1, opts.Y1, opts.X2, opts.Y2,
                        opts.Duration, opts.Steps, opts.Jitter, opts.HoldAtEnd)
        }()
        select {
        case err := <-done:
                return err
        case <-ctx.Done():
                return ctx.Err()
        }
}

// DragTouch injects a human-like drag via CDP's Input.dispatchTouchEvent
// (touchStart/touchMove/touchEnd). Used for captcha sliders that listen
// for touch events rather than mouse events.
func (b *WebviewBackend) DragTouch(ctx context.Context, opts agent.TrustedDragOpts) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return fmt.Errorf("CDP client not connected")
        }
        done := make(chan error, 1)
        go func() {
                done <- c.DragTouch(opts.X1, opts.Y1, opts.X2, opts.Y2,
                        opts.Duration, opts.Steps, opts.Jitter, opts.HoldAtEnd)
        }()
        select {
        case err := <-done:
                return err
        case <-ctx.Done():
                return ctx.Err()
        }
}

// EnableNetworkCapture starts CDP Network domain capturing.
func (b *WebviewBackend) EnableNetworkCapture(ctx context.Context) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return fmt.Errorf("CDP client not connected")
        }
        return c.EnableNetwork()
}

// DisableNetworkCapture stops CDP Network domain capturing.
func (b *WebviewBackend) DisableNetworkCapture(ctx context.Context) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil
        }
        c.DisableNetwork()
        return nil
}

// GetCapturedRequests returns all captured network requests.
func (b *WebviewBackend) GetCapturedRequests(ctx context.Context) ([]agent.CapturedRequest, error) {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil, fmt.Errorf("CDP client not connected")
        }
        reqs := c.GetCapturedRequests()
        out := make([]agent.CapturedRequest, len(reqs))
        for i, r := range reqs {
                out[i] = agent.CapturedRequest{
                        URL: r.URL, Method: r.Method, PostData: r.PostData,
                        Status: r.Status, ResponseBody: r.ResponseBody,
                        ResourceType: r.ResourceType,
                }
        }
        return out, nil
}

// ClearCapturedRequests clears the captured requests buffer.
func (b *WebviewBackend) ClearCapturedRequests(ctx context.Context) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil
        }
        c.ClearCapturedRequests()
        return nil
}

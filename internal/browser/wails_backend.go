package browser

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "log"
        "strings"
        "sync"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/cdp"
        "github.com/samaidev/samweb/internal/proxy"
        wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// WailsBackend is the production agent.Backend implementation that drives
// a wails WebView2 instance. It replaces WebviewBackend (which used
// webview_go). The key difference is:
//
//   - Go → JS: uses wails runtime.ExecJS instead of webview.Eval
//   - JS → Go: uses HTTP POST to /agent/callback instead of webview.Bind
//   - Thread safety: uses wails runtime.ExecJS which is safe to call
//     from any goroutine (wails handles marshalling to the UI thread)
type WailsBackend struct {
        ctx  context.Context
        mu   sync.Mutex
        pend map[string]chan callbackResult

        cdpMu     sync.RWMutex
        cdpClient *cdp.Client

        // skipNextClearOnNav is set by SwitchToProfile so that the
        // next CDPNavigateTop to an external site does NOT clear storage
        // again (SwitchToProfile already cleared + injected the profile's
        // cookies + localStorage; clearing again would wipe the injection).
        skipClearMu        sync.Mutex
        skipNextClearOnNav int
}

type callbackResult struct {
        result string
        err    string
}

// NewWailsBackend constructs a new backend.
func NewWailsBackend() *WailsBackend {
        return &WailsBackend{
                pend: map[string]chan callbackResult{},
        }
}

// SetContext sets the wails context (needed for runtime.ExecJS).
func (b *WailsBackend) SetContext(ctx context.Context) {
        b.ctx = ctx
}

// SetCDPClient stores the CDP client.
func (b *WailsBackend) SetCDPClient(c *cdp.Client) {
        b.cdpMu.Lock()
        defer b.cdpMu.Unlock()
        b.cdpClient = c
}

// HandleCallback is called by the HTTP handler when JS POSTs a result
// to /agent/callback.
func (b *WailsBackend) HandleCallback(id, result, err string) {
        b.mu.Lock()
        ch, ok := b.pend[id]
        if ok {
                delete(b.pend, id)
        }
        b.mu.Unlock()
        if !ok {
                return
        }
        ch <- callbackResult{result: result, err: err}
}

// dispatch sends a method invocation to the JS side and waits for the
// callback via HTTP POST.
func (b *WailsBackend) dispatch(ctx context.Context, method string, params interface{}) (string, error) {
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

        // Use wails runtime.ExecJS to call __samwebAgentDispatch.
        // This is safe to call from any goroutine.
        js := fmt.Sprintf(`window.__samwebAgentDispatch(%q, %q, %s);`, id, method, paramsJSON)

        if b.ctx == nil {
                return "", errors.New("wails context not set")
        }
        wailsRuntime.WindowExecJS(b.ctx, js)

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
                return "", errors.New("agent: timeout waiting for wails callback")
        }
}

// dispatchVoid is dispatch for methods that return only {ok:true}.
func (b *WailsBackend) dispatchVoid(ctx context.Context, method string, params interface{}) error {
        _, err := b.dispatch(ctx, method, params)
        return err
}

// newRequestID is defined in util.go — no need to redeclare here.
// (kept as a comment to avoid confusion)

// ----------------------------- Backend implementation -----------------------------

func (b *WailsBackend) Navigate(ctx context.Context, url string) error {
        return b.dispatchVoid(ctx, "navigate", map[string]string{"url": url})
}

func (b *WailsBackend) NavigateDirect(ctx context.Context, url string) error {
        return b.dispatchVoid(ctx, "navigateDirect", map[string]string{"url": url})
}

func (b *WailsBackend) Back(ctx context.Context) error       { return b.dispatchVoid(ctx, "back", nil) }
func (b *WailsBackend) Forward(ctx context.Context) error    { return b.dispatchVoid(ctx, "forward", nil) }
func (b *WailsBackend) Reload(ctx context.Context) error     { return b.dispatchVoid(ctx, "reload", nil) }
func (b *WailsBackend) Stop(ctx context.Context) error       { return b.dispatchVoid(ctx, "stop", nil) }
func (b *WailsBackend) Click(ctx context.Context, opts agent.ClickOpts) error {
        return b.dispatchVoid(ctx, "click", opts)
}
func (b *WailsBackend) Scroll(ctx context.Context, opts agent.ScrollOpts) error {
        return b.dispatchVoid(ctx, "scroll", opts)
}
func (b *WailsBackend) Type(ctx context.Context, opts agent.TypeOpts) error {
        return b.dispatchVoid(ctx, "type", opts)
}
func (b *WailsBackend) PressKey(ctx context.Context, opts agent.KeyOpts) error {
        return b.dispatchVoid(ctx, "key", opts)
}
func (b *WailsBackend) Drag(ctx context.Context, opts agent.DragOpts) error {
        return b.dispatchVoid(ctx, "drag", opts)
}
func (b *WailsBackend) DragTrusted(ctx context.Context, opts agent.TrustedDragOpts) error {
        return b.dispatchVoid(ctx, "dragTrusted", opts)
}
func (b *WailsBackend) DragTouch(ctx context.Context, opts agent.TrustedDragOpts) error {
        return b.dispatchVoid(ctx, "dragTouch", opts)
}

func (b *WailsBackend) EnableNetworkCapture(ctx context.Context) error {
        return b.dispatchVoid(ctx, "networkEnable", nil)
}
func (b *WailsBackend) DisableNetworkCapture(ctx context.Context) error {
        return b.dispatchVoid(ctx, "networkDisable", nil)
}
func (b *WailsBackend) GetCapturedRequests(ctx context.Context) ([]agent.CapturedRequest, error) {
        r, err := b.dispatch(ctx, "getRequests", nil)
        if err != nil {
                return nil, err
        }
        var resp struct {
                Requests []agent.CapturedRequest `json:"requests"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return nil, fmt.Errorf("unmarshal requests: %w", err)
        }
        return resp.Requests, nil
}
func (b *WailsBackend) ClearCapturedRequests(ctx context.Context) error {
        return b.dispatchVoid(ctx, "clearRequests", nil)
}

func (b *WailsBackend) GetAllCookies(ctx context.Context) ([]agent.BrowserCookie, error) {
        r, err := b.dispatch(ctx, "getCookies", nil)
        if err != nil {
                return nil, err
        }
        var resp struct {
                Cookies []agent.BrowserCookie `json:"cookies"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return nil, fmt.Errorf("unmarshal cookies: %w", err)
        }
        return resp.Cookies, nil
}

func (b *WailsBackend) CDPRawMouse(ctx context.Context, opts agent.RawMouseOpts) error {
        return b.dispatchVoid(ctx, "cdpMouse", opts)
}

// CDPNavigateTop drives the WebView2 top-level page to url via CDP
// Page.navigate. This bypasses the samweb iframe entirely, allowing
// sites that block iframe embedding (z.ai, Google) to be loaded
// directly. The samweb UI is gone after this call — bring it back by
// calling CDPNavigateTop("http://wails.localhost/").
//
// Before navigating away, we inject a "← Back to SamWeb" floating
// button via Page.addScriptToEvaluateOnNewDocument so the user can
// return without external tooling.
//
// If navigating AWAY from samweb UI (to an external site), we clear
// ALL browser storage (cookies, localStorage, sessionStorage, IndexedDB,
// cache) first so the user starts with a fresh session — unless
// skipNextClearOnNav was set by SwitchToProfile (in which case we
// preserve the just-injected profile storage).
func (b *WailsBackend) CDPNavigateTop(ctx context.Context, url string) error {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return fmt.Errorf("CDP client not connected")
        }

        // If navigating AWAY from samweb UI, clear ALL storage so the
        // user starts fresh (unless skip flag is set by SwitchToProfile).
        if !strings.Contains(url, "wails.localhost") {
                b.skipClearMu.Lock()
                skip := b.skipNextClearOnNav
                if skip > 0 {
                        b.skipNextClearOnNav = skip - 1
                }
                b.skipClearMu.Unlock()
                if skip > 0 {
                        log.Printf("[browser] skip ClearDataForOrigin (profile just switched) before direct-navigate to %s", url)
                } else {
                        if err := c.ClearDataForOrigin("*", "all"); err != nil {
                                log.Printf("[browser] warning: ClearDataForOrigin before navigate: %v", err)
                        } else {
                                log.Printf("[browser] cleared all storage before direct-navigate to %s", url)
                        }
                }
        }

        // Inject the back-button script (runs on every new document load).
        backButtonScript := `
(function(){
  if (window.top !== window) return;
  if (location.href.indexOf('wails.localhost') >= 0) return;
  if (window.__samwebBackButton) return;
  window.__samwebBackButton = true;
  var btn = document.createElement('div');
  btn.id = '__samweb_back_btn';
  btn.textContent = '\u2190 \u8fd4\u56de SamWeb';
  btn.style.cssText = [
    'position:fixed','top:12px','right:12px','z-index:2147483647',
    'padding:8px 14px','background:#1a73e8','color:#fff',
    'font:600 13px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif',
    'border-radius:20px','cursor:pointer','box-shadow:0 2px 8px rgba(0,0,0,0.3)',
    'user-select:none','-webkit-user-select:none','border:none'
  ].join(';');
  btn.onmouseenter = function(){ btn.style.background = '#1557b0'; };
  btn.onmouseleave = function(){ btn.style.background = '#1a73e8'; };
  btn.onclick = function(){
    btn.textContent = '\u8fd4\u56de\u4e2d\u2026';
    window.location.href = 'http://wails.localhost/';
  };
  function mount(){
    if (document.body) {
      document.body.appendChild(btn);
    } else {
      setTimeout(mount, 50);
    }
  }
  mount();
})();
`
        if _, err := c.AddScriptToEvaluateOnNewDocument(backButtonScript); err != nil {
                return fmt.Errorf("inject back-button script: %w", err)
        }
        if err := c.EnablePage(); err != nil {
                log.Printf("[browser] Page.enable warning: %v", err)
        }
        return c.Navigate(url)
}

func (b *WailsBackend) BreakthroughSlider(ctx context.Context) (string, bool, error) {
        r, err := b.dispatch(ctx, "breakthrough", nil)
        if err != nil {
                return "", false, err
        }
        var resp struct {
                Challenge string `json:"challenge"`
                Success   bool   `json:"success"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return "", false, fmt.Errorf("unmarshal breakthrough: %w", err)
        }
        return resp.Challenge, resp.Success, nil
}

func (b *WailsBackend) Eval(ctx context.Context, script string) (json.RawMessage, error) {
        r, err := b.dispatch(ctx, "eval", map[string]string{"script": script})
        if err != nil {
                return nil, err
        }
        // The JS side returns {value: <result>}, where <result> is already
        // a JSON string. We need to extract it.
        var resp struct {
                Value json.RawMessage `json:"value"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                // If unmarshal fails, return the raw string.
                return json.RawMessage(r), nil
        }
        return resp.Value, nil
}

func (b *WailsBackend) Wait(ctx context.Context, selector string, timeoutMs int) error {
        return b.dispatchVoid(ctx, "wait", map[string]interface{}{
                "selector":  selector,
                "timeoutMs": timeoutMs,
        })
}

func (b *WailsBackend) Elements(ctx context.Context, selector string) ([]agent.Element, error) {
        r, err := b.dispatch(ctx, "elements", map[string]string{"selector": selector})
        if err != nil {
                return nil, err
        }
        var resp struct {
                Elements []agent.Element `json:"elements"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return nil, fmt.Errorf("unmarshal elements: %w", err)
        }
        return resp.Elements, nil
}

func (b *WailsBackend) Element(ctx context.Context, selector string) (*agent.Element, error) {
        r, err := b.dispatch(ctx, "element", map[string]string{"selector": selector})
        if err != nil {
                return nil, err
        }
        var resp struct {
                Element *agent.Element `json:"element"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return nil, fmt.Errorf("unmarshal element: %w", err)
        }
        return resp.Element, nil
}

func (b *WailsBackend) State(ctx context.Context) (*agent.State, error) {
        r, err := b.dispatch(ctx, "state", nil)
        if err != nil {
                return nil, err
        }
        var state agent.State
        if err := json.Unmarshal([]byte(r), &state); err != nil {
                return nil, fmt.Errorf("unmarshal state: %w", err)
        }
        return &state, nil
}

func (b *WailsBackend) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
        r, err := b.dispatch(ctx, "screenshot", map[string]bool{"fullPage": fullPage})
        if err != nil {
                return nil, err
        }
        // The JS side returns a base64-encoded PNG.
        var resp struct {
                Data string `json:"data"`
        }
        if err := json.Unmarshal([]byte(r), &resp); err != nil {
                return nil, fmt.Errorf("unmarshal screenshot: %w", err)
        }
        // Decode base64.
        return decodeBase64(resp.Data)
}

func (b *WailsBackend) ScreenshotTrusted(ctx context.Context, fullPage bool) ([]byte, error) {
        // Use CDP for trusted screenshot.
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil, fmt.Errorf("CDP client not connected")
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

func (b *WailsBackend) ResetCookies(ctx context.Context) error {
        proxy.ResetCookies()
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c != nil {
                _ = c.ClearCookies()
        }
        return nil
}

func (b *WailsBackend) SaveCookies(ctx context.Context) error {
        if err := proxy.SaveCookies(); err != nil {
                return err
        }
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil
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

func (b *WailsBackend) LoadCookies(ctx context.Context) error {
        if err := proxy.LoadCookies(); err != nil {
                return err
        }
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
                _ = c.SetCookie(ck)
        }
        return nil
}

// Profile methods (same as WebviewBackend).
func (b *WailsBackend) SaveCurrentCookiesToProfile(ctx context.Context, name string) (agent.ProfileInfo, error) {
        cookies, err := b.snapshotCDPCookies()
        if err != nil {
                return agent.ProfileInfo{}, err
        }
        prof, err := Profiles().Create(name, cookies)
        if err != nil {
                return agent.ProfileInfo{}, err
        }
        // Also dump localStorage from the current page (z.ai stores its
        // login JWT in localStorage, not cookies). Group by origin.
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c != nil {
                ls, lsErr := c.DumpLocalStorage()
                if lsErr == nil && len(ls) > 0 {
                        origin, originErr := c.CurrentOrigin()
                        if originErr == nil && origin != "" && origin != "http://wails.localhost" {
                                lsMap := map[string]map[string]string{origin: ls}
                                _ = Profiles().UpdateLocalStorage(prof.ID, lsMap)
                                log.Printf("[browser] saved %d localStorage entries for origin %s to profile %s", len(ls), origin, prof.ID)
                        }
                } else if lsErr != nil {
                        log.Printf("[browser] warning: dump localStorage: %v", lsErr)
                }
        }
        return toProfileInfo(prof), nil
}

func (b *WailsBackend) ListProfiles(ctx context.Context) ([]agent.ProfileInfo, string, error) {
        profs, activeID, err := Profiles().List()
        if err != nil {
                return nil, "", err
        }
        out := make([]agent.ProfileInfo, len(profs))
        for i, p := range profs {
                out[i] = toProfileInfo(p)
        }
        return out, activeID, nil
}

func (b *WailsBackend) RenameProfile(ctx context.Context, id, newName string) error {
        return Profiles().Rename(id, newName)
}

func (b *WailsBackend) DeleteProfile(ctx context.Context, id string) error {
        return Profiles().Delete(id)
}

func (b *WailsBackend) SwitchToProfile(ctx context.Context, id string) error {
        if err := Profiles().Activate(id); err != nil {
                return err
        }
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return fmt.Errorf("CDP client not connected")
        }

        // Clear ALL storage (cookies + localStorage + IndexedDB + cache)
        // for all origins — gives the new profile a clean slate.
        if err := c.ClearDataForOrigin("*", "all"); err != nil {
                log.Printf("[browser] warning: ClearDataForOrigin on switch: %v", err)
        }

        if id == "" {
                log.Printf("[browser] switched to empty profile (cleared all storage)")
                return nil
        }

        prof, ok, err := Profiles().Get(id)
        if err != nil {
                return err
        }
        if !ok {
                return fmt.Errorf("profile not found: %s", id)
        }

        // Inject cookies.
        for _, ck := range prof.Cookies {
                _ = c.SetCookie(ck)
        }
        log.Printf("[browser] injected %d cookies from profile %s", len(prof.Cookies), id)

        // Set skip flag so the next CDPNavigateTop to an external site
        // doesn't clear storage again (which would wipe the cookies we
        // just injected + any localStorage). 1 skip is enough for
        // profiles without localStorage (only the user's "直接打开"
        // needs to be skipped). For profiles WITH localStorage, we need
        // 2 skips: one for our internal navigate to inject localStorage,
        // one for the user's subsequent "直接打开".
        b.skipClearMu.Lock()
        if len(prof.LocalStorage) > 0 {
                b.skipNextClearOnNav = 2
        } else {
                b.skipNextClearOnNav = 1
        }
        b.skipClearMu.Unlock()

        if len(prof.LocalStorage) > 0 {
                // localStorage must be injected on the target origin.
                // Navigate there, write localStorage, then navigate back.
                go func() {
                        for origin, entries := range prof.LocalStorage {
                                if err := c.Navigate(origin + "/"); err != nil {
                                        log.Printf("[browser] warning: navigate to %s for localStorage: %v", origin, err)
                                        continue
                                }
                                time.Sleep(3 * time.Second)
                                if err := c.RestoreLocalStorage(entries); err != nil {
                                        log.Printf("[browser] warning: restore localStorage for %s: %v", origin, err)
                                } else {
                                        log.Printf("[browser] restored %d localStorage entries for origin %s", len(entries), origin)
                                }
                        }
                        _ = c.Navigate("http://wails.localhost/")
                }()
        }

        return nil
}

func (b *WailsBackend) snapshotCDPCookies() ([]cdp.CDPCookie, error) {
        b.cdpMu.RLock()
        c := b.cdpClient
        b.cdpMu.RUnlock()
        if c == nil {
                return nil, fmt.Errorf("CDP client not connected")
        }
        return c.GetAllCookies()
}

// toProfileInfo converts the internal Profile type to the agent-layer
// ProfileInfo DTO.
func toProfileInfo(p Profile) agent.ProfileInfo {
        return agent.ProfileInfo{
                ID:          p.ID,
                Name:        p.Name,
                CookieCount: len(p.Cookies),
                Created:     p.Created,
                Updated:     p.Updated,
        }
}

func (b *WailsBackend) Close() error { return nil }

// Wails-exposed methods (for frontend binding). These are called from JS
// via wails' auto-generated bindings. We don't use them directly — the
// agent API goes through the HTTP server. But wails requires at least
// one bound struct.
func (b *WailsBackend) Ping() string { return "pong" }

// decodeBase64 decodes a base64 string to bytes.
func decodeBase64(s string) ([]byte, error) {
        return base64Decode(s)
}

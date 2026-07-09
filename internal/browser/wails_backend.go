package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/samaidev/samweb/internal/agent"
	"github.com/samaidev/samweb/internal/cdp"
	"github.com/wailsapp/wails/v2/pkg/runtime"
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
	runtime.ExecJS(b.ctx, js)

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

// newRequestID generates a unique request ID.
var requestIDCounter struct {
	sync.Mutex
	n uint64
}

func newRequestID() string {
	requestIDCounter.Lock()
	defer requestIDCounter.Unlock()
	requestIDCounter.n++
	return fmt.Sprintf("req-%d", requestIDCounter.n)
}

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
	import_encoding_base64_DecodeString := func(s string) ([]byte, error) {
		return decodeBase64(s)
	}
	return import_encoding_base64_DecodeString(resp.Data)
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
	if id == "" {
		return nil
	}
	prof, ok, err := Profiles().Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("profile not found: %s", id)
	}
	if err := b.ResetCookies(ctx); err != nil {
		return fmt.Errorf("reset cookies: %w", err)
	}
	b.cdpMu.RLock()
	c := b.cdpClient
	b.cdpMu.RUnlock()
	if c == nil {
		return fmt.Errorf("CDP client not connected")
	}
	for _, ck := range prof.Cookies {
		_ = c.SetCookie(ck)
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

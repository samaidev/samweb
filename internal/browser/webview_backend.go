package browser

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "sync"
        "time"

        "github.com/samaidev/samweb/internal/agent"
        "github.com/samaidev/samweb/internal/proxy"
        "github.com/webview/webview_go"
)

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

func (b *WebviewBackend) Close() error { return nil }

// ResetCookies clears the proxy's shared cookie jar so the next navigation
// starts a fresh session. This is useful for switching accounts or retrying
// a failed login without restarting the whole browser.
func (b *WebviewBackend) ResetCookies(ctx context.Context) error {
        proxy.ResetCookies()
        return nil
}

// SaveCookies persists the proxy's cookie jar to disk so the session
// survives process restarts. This is the mechanism that makes SamWeb
// "log in once, stay logged in forever" — after a successful manual
// login, the agent calls SaveCookies, and on the next process start
// the proxy's init() calls LoadCookies automatically.
func (b *WebviewBackend) SaveCookies(ctx context.Context) error {
        return proxy.SaveCookies()
}

// LoadCookies re-reads the cookie jar from disk, replacing any in-memory
// cookies. Useful when the cookie file was edited externally or written
// by a previous process.
func (b *WebviewBackend) LoadCookies(ctx context.Context) error {
        return proxy.LoadCookies()
}

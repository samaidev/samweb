// Package cdp provides a minimal Chrome DevTools Protocol client that
// connects to a WebView2 instance's remote debugging port and dispatches
// trusted input events (mouse / touch) that bypass the `event.isTrusted`
// check used by anti-bot systems like Aliyun baxia.
//
// This is the engine-level injection that JS dispatchEvent cannot do:
// CDP's Input.dispatchMouseEvent creates events with isTrusted=true,
// exactly as if a real user moved the mouse.
package cdp

import (
        "encoding/base64"
        "encoding/json"
        "fmt"
        "log"
        "net/http"
        "strings"
        "sync"
        "sync/atomic"
        "time"

        "github.com/gorilla/websocket"
)

// Client is a CDP-over-WebSocket client connected to a running WebView2
// instance. It is goroutine-safe (a single underlying WebSocket guarded
// by a mutex, since the WS protocol does not allow concurrent writes).
type Client struct {
        mu       sync.Mutex
        conn     *websocket.Conn
        nextID   atomic.Int64
        pending  map[int64]chan cdpResponse
        endpoint string // ws://127.0.0.1:9222/devtools/page/<id>

        // Network capture state
        captureMu        sync.RWMutex
        capturing        bool
        capturedRequests []CapturedRequest
        // requestMap maps CDP requestId to the index in capturedRequests,
        // so we can update the entry when responseReceived / loadingFinished fires.
        requestMap       map[string]int

        // SSE stream capture: z.ai Agent mode streams output via SSE.
        // These events arrive via CDP WebSocket (not JS thread), so they
        // work even when z.ai's JS is blocked during task execution.
        sseMu       sync.Mutex
        sseMessages []SSEMessage
        sseEnabled  bool

        // Fetch domain intercept: captures response bodies of z.ai's
        // streaming API calls in real-time. Unlike Runtime.evaluate
        // (which needs JS thread), Fetch.requestPaused events arrive
        // via CDP WebSocket and work even when JS is blocked.
        //
        // When a streaming response is paused, we call Fetch.getResponseBody
        // (which returns whatever has accumulated so far) AND let the request
        // continue. As long as the request is still streaming, successive
        // requestPaused events / takeResponseBodyAsStream calls let us read
        // incremental chunks. We also rely on Network.loadingFinished to
        // grab the final body when streaming completes.
        fetchMu        sync.Mutex
        fetchEnabled   bool
        fetchChunks    []FetchChunk // accumulated response chunks
        // fetchPaused tracks each paused request: requestId -> last byte offset read.
        fetchPaused    map[string]int64
        // fetchURLs maps requestId to the request URL (for logging / chunk metadata).
        fetchURLs      map[string]string
        fetchFilter    string // URL substring filter (only intercept matching requests)
}

// FetchChunk is a single incremental chunk of a streaming response body
// captured via the Fetch domain. Chunks arrive via CDP WebSocket — they
// do not require the page's JS thread, so they work even when z.ai's JS
// is fully blocked during long Agent tasks.
type FetchChunk struct {
        RequestID string `json:"requestId"`
        URL       string `json:"url"`
        Chunk     string `json:"chunk"`       // text content (decoded)
        Offset    int64  `json:"offset"`      // byte offset of this chunk
        Timestamp float64 `json:"timestamp"`
        Final     bool   `json:"final"`       // true if this is the last chunk
}

// SSEMessage is a single Server-Sent Event message captured from the
// network. z.ai uses SSE to stream Agent mode responses.
type SSEMessage struct {
        URL       string `json:"url"`
        Data      string `json:"data"`
        Timestamp float64 `json:"timestamp"`
}

type cdpResponse struct {
        result json.RawMessage
        err    error
}

// PageTarget describes a single CDP page target as returned by
// /json on the remote debugging port.
type PageTarget struct {
        ID                   string `json:"id"`
        Title                string `json:"title"`
        URL                  string `json:"url"`
        Type                 string `json:"type"`
        WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// FindPageTarget queries http://127.0.0.1:<port>/json and returns the
// first "page" target. Returns nil if none found.
func FindPageTarget(port int) (*PageTarget, error) {
        resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port))
        if err != nil {
                return nil, fmt.Errorf("cdp: cannot reach /json on port %d: %w", port, err)
        }
        defer resp.Body.Close()
        var targets []PageTarget
        if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
                return nil, fmt.Errorf("cdp: decode /json response: %w", err)
        }
        for i := range targets {
                if targets[i].Type == "page" && targets[i].WebSocketDebuggerURL != "" {
                        return &targets[i], nil
                }
        }
        return nil, fmt.Errorf("cdp: no page target found on port %d", port)
}

// Connect dials the CDP WebSocket endpoint and starts a reader goroutine
// that dispatches responses to the waiting callers.
func Connect(wsURL string) (*Client, error) {
        dialer := websocket.Dialer{
                HandshakeTimeout: 10 * time.Second,
        }
        conn, _, err := dialer.Dial(wsURL, nil)
        if err != nil {
                return nil, fmt.Errorf("cdp: dial %s: %w", wsURL, err)
        }
        c := &Client{
                conn:       conn,
                pending:    map[int64]chan cdpResponse{},
                endpoint:   wsURL,
                requestMap: map[string]int{},
        }
        go c.readLoop()
        log.Printf("[cdp] connected to %s", wsURL)
        return c, nil
}

// ConnectToPage is a convenience that finds the first page target on
// the given port and connects to it.
func ConnectToPage(port int) (*Client, error) {
        t, err := FindPageTarget(port)
        if err != nil {
                return nil, err
        }
        return Connect(t.WebSocketDebuggerURL)
}

// FindIframeTarget finds a target of type "iframe" on the given port.
// Returns the first iframe target, or nil if none found.
func FindIframeTarget(port int) (*PageTarget, error) {
        resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json", port))
        if err != nil {
                return nil, fmt.Errorf("cdp: cannot reach /json on port %d: %w", port, err)
        }
        defer resp.Body.Close()
        var targets []PageTarget
        if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
                return nil, fmt.Errorf("cdp: decode /json response: %w", err)
        }
        for i := range targets {
                // iframe targets have type "iframe" in newer Chromium, or
                // type "other" with a URL that looks like a punish/captcha page
                t := targets[i]
                if t.Type == "iframe" && t.WebSocketDebuggerURL != "" {
                        return &t, nil
                }
        }
        // Fallback: look for any target whose URL contains "punish" or
        // "baxia" (the baxia captcha iframe)
        for i := range targets {
                t := targets[i]
                if t.WebSocketDebuggerURL != "" && (strings.Contains(t.URL, "punish") || strings.Contains(t.URL, "baxia")) {
                        return &t, nil
                }
        }
        return nil, fmt.Errorf("cdp: no iframe target found on port %d", port)
}

// ConnectToIframe finds and connects to the baxia iframe's CDP target.
// This is separate from the page-level CDP connection. Events dispatched
// from this connection go directly to the iframe's document, bypassing
// the top-level page's event routing.
func ConnectToIframe(port int) (*Client, error) {
        t, err := FindIframeTarget(port)
        if err != nil {
                return nil, err
        }
        return Connect(t.WebSocketDebuggerURL)
}

// AttachToTarget uses CDP Target.attachToTarget to attach to a specific
// target (e.g. an iframe) by target ID. Returns the session ID needed
// for subsequent commands on that target.
//
// This is the "flat session" approach: we send commands with a
// sessionId field, and CDP routes them to the iframe's context.
func (c *Client) AttachToTarget(targetID string) (string, error) {
        resp, err := c.send("Target.attachToTarget", map[string]interface{}{
                "targetId":  targetID,
                "flatten":   true,
        })
        if err != nil {
                return "", err
        }
        var result struct {
                SessionID string `json:"sessionId"`
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return "", fmt.Errorf("cdp: decode attachToTarget response: %w", err)
        }
        return result.SessionID, nil
}

// GetTargets returns all targets via CDP Target.getTargets.
func (c *Client) GetTargets() ([]PageTarget, error) {
        resp, err := c.send("Target.getTargets", nil)
        if err != nil {
                return nil, err
        }
        var result struct {
                TargetInfos []struct {
                        TargetID         string `json:"targetId"`
                        Type             string `json:"type"`
                        Title            string `json:"title"`
                        URL              string `json:"url"`
                        Attached         bool   `json:"attached"`
                        OpenerID         string `json:"openerId"`
                        CanAccessOpener  bool   `json:"canAccessOpener"`
                        OpenerFrameID    string `json:"openerFrameId"`
                } `json:"targetInfos"`
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return nil, fmt.Errorf("cdp: decode getTargets response: %w", err)
        }
        out := make([]PageTarget, len(result.TargetInfos))
        for i, t := range result.TargetInfos {
                out[i] = PageTarget{
                        ID:               t.TargetID,
                        Type:             t.Type,
                        Title:            t.Title,
                        URL:              t.URL,
                        WebSocketDebuggerURL: "", // Target.getTargets doesn't return ws URL
                }
        }
        return out, nil
}

// GetAllCookies returns all cookies from the browser's cookie store via
// CDP Network.getAllCookies. This includes cookies set by navigate-direct
// (which bypass samweb's proxy cookie jar, since the webview loads the
// page directly via WebView2's network stack). Use this to export the
// full cookie state for persistence.
func (c *Client) GetAllCookies() ([]CDPCookie, error) {
        resp, err := c.send("Network.getAllCookies", nil)
        if err != nil {
                return nil, err
        }
        var result struct {
                Cookies []CDPCookie `json:"cookies"`
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return nil, fmt.Errorf("cdp: decode cookies response: %w", err)
        }
        return result.Cookies, nil
}

// CDPCookie is the CDP representation of a browser cookie (from
// Network.getAllCookies). Field names match the CDP spec.
type CDPCookie struct {
        Name     string  `json:"name"`
        Value    string  `json:"value"`
        Domain   string  `json:"domain"`
        Path     string  `json:"path"`
        Expires  float64 `json:"expires"`   // Unix timestamp in seconds (-1 = session)
        Size     int     `json:"size"`
        HTTPOnly bool    `json:"httpOnly"`
        Secure   bool    `json:"secure"`
        Session  bool    `json:"session"`
        SameSite string  `json:"sameSite"`
        Priority string  `json:"priority"`
}

// SetCookie injects a cookie into the browser's cookie store via CDP
// Network.setCookie. Used by LoadCookiesTrusted to restore a previously
// saved cookie jar.
func (c *Client) SetCookie(cookie CDPCookie) error {
        params := map[string]interface{}{
                "name":     cookie.Name,
                "value":    cookie.Value,
                "domain":   cookie.Domain,
                "path":     cookie.Path,
                "httpOnly": cookie.HTTPOnly,
                "secure":   cookie.Secure,
                "sameSite": cookie.SameSite,
        }
        if cookie.Expires > 0 && !cookie.Session {
                params["expires"] = cookie.Expires
        }
        _, err := c.send("Network.setCookie", params)
        return err
}

// ClearCookies deletes all cookies from the browser's cookie store via
// CDP Network.clearBrowserCookies. Used by ResetCookiesTrusted.
func (c *Client) ClearCookies() error {
        _, err := c.send("Network.clearBrowserCookies", nil)
        return err
}

// Navigate tells the WebView2 top-level page to navigate to url.
// Use this to bypass the samweb iframe (and the X-Frame-Options / CSP
// restrictions that block iframe embedding of sites like z.ai).
// The samweb UI is gone after this call — bring it back by navigating
// to http://wails.localhost/.
func (c *Client) Navigate(url string) error {
        _, err := c.send("Page.navigate", map[string]interface{}{"url": url})
        return err
}

// AddScriptToEvaluateOnNewDocument injects a script that will run on
// every new document load (including cross-origin navigations). Used
// to inject the "← Back to SamWeb" floating button when the user
// "直接打开"s a site that can't be iframed.
func (c *Client) AddScriptToEvaluateOnNewDocument(script string) (string, error) {
        resp, err := c.send("Page.addScriptToEvaluateOnNewDocument",
                map[string]interface{}{"source": script})
        if err != nil {
                return "", err
        }
        var out struct {
                Identifier string `json:"identifier"`
        }
        _ = json.Unmarshal(resp, &out)
        return out.Identifier, nil
}

// RemoveScriptToEvaluateOnNewDocument removes a previously injected
// script by its identifier.
func (c *Client) RemoveScriptToEvaluateOnNewDocument(identifier string) error {
        _, err := c.send("Page.removeScriptToEvaluateOnNewDocument",
                map[string]interface{}{"identifier": identifier})
        return err
}

// EnablePage turns on Page domain events (needed before Page.navigate
// in some WebView2 versions).
func (c *Client) EnablePage() error {
        _, err := c.send("Page.enable", nil)
        return err
}

// ClearDataForOrigin wipes ALL storage (cookies, localStorage,
// sessionStorage, IndexedDB, cache, service workers) for the given
// origin. Pass origin="*" to clear for all origins. This is the
// nuclear option — needed because sites like z.ai store their login
// token in localStorage, not just cookies, so Network.clearBrowserCookies
// alone is not enough to log out.
//
// storageTypes: "all" (default), or a comma-separated subset:
// "cookies,local_storage,session_storage,indexeddb,cache_storage".
func (c *Client) ClearDataForOrigin(origin string, storageTypes string) error {
        if storageTypes == "" {
                storageTypes = "all"
        }
        _, err := c.send("Storage.clearDataForOrigin", map[string]interface{}{
                "origin":       origin,
                "storageTypes": storageTypes,
        })
        return err
}

// DumpLocalStorage reads all localStorage entries for the current page
// via Runtime.evaluate. Returns a map of key→value. Must be called
// when the page is currently loaded on the origin whose localStorage
// you want to dump.
func (c *Client) DumpLocalStorage() (map[string]string, error) {
        script := `(function(){
          var out = {};
          try {
            for (var i = 0; i < localStorage.length; i++) {
              var k = localStorage.key(i);
              out[k] = localStorage.getItem(k) || '';
            }
          } catch(e) {}
          return out;
        })()`
        resp, err := c.send("Runtime.evaluate", map[string]interface{}{
                "expression":    script,
                "returnByValue": true,
        })
        if err != nil {
                return nil, err
        }
        var wrap struct {
                Result struct {
                        Value map[string]string `json:"value"`
                } `json:"result"`
        }
        if err := json.Unmarshal(resp, &wrap); err != nil {
                return nil, fmt.Errorf("decode dump localStorage: %w", err)
        }
        return wrap.Result.Value, nil
}

// RestoreLocalStorage writes the given key→value entries into the
// current page's localStorage via Runtime.evaluate. Must be called
// when the page is currently loaded on the target origin (e.g.
// navigate to https://chat.z.ai first, then call this).
func (c *Client) RestoreLocalStorage(entries map[string]string) error {
        entriesJSON, err := json.Marshal(entries)
        if err != nil {
                return fmt.Errorf("marshal localStorage entries: %w", err)
        }
        script := fmt.Sprintf(`(function(){
          var entries = %s;
          try {
            for (var k in entries) {
              if (entries.hasOwnProperty(k)) {
                localStorage.setItem(k, entries[k]);
              }
            }
          } catch(e) { return false; }
          return true;
        })()`, string(entriesJSON))
        _, err = c.send("Runtime.evaluate", map[string]interface{}{
                "expression":    script,
                "returnByValue": true,
        })
        return err
}

// CurrentOrigin returns the origin (scheme://host[:port]) of the
// currently-loaded page. Used to key localStorage snapshots by origin.
func (c *Client) CurrentOrigin() (string, error) {
        script := `(function(){
          try {
            return location.origin;
          } catch(e) { return ''; }
        })()`
        resp, err := c.send("Runtime.evaluate", map[string]interface{}{
                "expression":    script,
                "returnByValue": true,
        })
        if err != nil {
                return "", err
        }
        var wrap struct {
                Result struct {
                        Value string `json:"value"`
                } `json:"result"`
        }
        if err := json.Unmarshal(resp, &wrap); err != nil {
                return "", fmt.Errorf("decode current origin: %w", err)
        }
        return wrap.Result.Value, nil
}
// Returns the base64-encoded PNG data (CDP returns base64, not raw
// bytes). The caller should base64-decode it.
//
// Unlike the JS-level screenshot (which uses SVG foreignObject and
// often fails on complex pages), this captures the actual rendered
// pixels from the WebView2 compositor — what the user sees.
func (c *Client) Screenshot(fullPage bool) ([]byte, error) {
        params := map[string]interface{}{
                "format":      "png",
                "fromSurface": true,
        }
        if fullPage {
                // Capture the full scrollable page, not just the viewport.
                params["clip"] = map[string]interface{}{
                        "x":      0,
                        "y":      0,
                        "width":  1280,
                        "height": 8000,
                        "scale":  1,
                }
        }
        resp, err := c.send("Page.captureScreenshot", params)
        if err != nil {
                return nil, err
        }
        var result struct {
                Data string `json:"data"` // base64-encoded PNG
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return nil, fmt.Errorf("cdp: decode screenshot response: %w", err)
        }
        if result.Data == "" {
                return nil, fmt.Errorf("cdp: screenshot returned empty data")
        }
        // base64-decode
        decoded, err := base64.StdEncoding.DecodeString(result.Data)
        if err != nil {
                return nil, fmt.Errorf("cdp: base64-decode screenshot: %w", err)
        }
        return decoded, nil
}

// EnableSSECapture enables CDP Network domain + listens for SSE events.
// z.ai Agent mode streams output via Server-Sent Events. These events
// arrive via CDP WebSocket (not JS thread), so they work even when
// z.ai's JS is blocked during task execution.
func (c *Client) EnableSSECapture() error {
        c.sseMu.Lock()
        c.sseMessages = nil
        c.sseEnabled = true
        c.sseMu.Unlock()
        // Enable Network domain (needed for eventSource events)
        _, err := c.send("Network.enable", map[string]interface{}{
                "maxTotalBufferSize":    50 * 1024 * 1024,
                "maxResourceBufferSize": 10 * 1024 * 1024,
        })
        return err
}

// DisableSSECapture stops capturing SSE events.
func (c *Client) DisableSSECapture() {
        c.sseMu.Lock()
        c.sseEnabled = false
        c.sseMu.Unlock()
        _, _ = c.send("Network.disable", nil)
}

// GetSSEMessages returns all captured SSE messages since the last call
// and clears the buffer. Each call returns only NEW messages.
func (c *Client) GetSSEMessages() []SSEMessage {
        c.sseMu.Lock()
        defer c.sseMu.Unlock()
        msgs := c.sseMessages
        c.sseMessages = nil
        return msgs
}

// EnableNetwork enables the CDP Network domain and captures all network
// requests with full detail (headers, cookies, response body, timing).
// After calling this, GetCapturedRequests returns a list of all requests
// made by the page.
func (c *Client) EnableNetwork() error {
        // Start capturing before enabling
        c.captureMu.Lock()
        c.capturing = true
        c.capturedRequests = nil
        c.requestMap = map[string]int{}
        c.captureMu.Unlock()
        // Enable Network domain with response bodies
        _, err := c.send("Network.enable", map[string]interface{}{
                "maxTotalBufferSize":    50 * 1024 * 1024,
                "maxResourceBufferSize": 10 * 1024 * 1024,
        })
        return err
}

// DisableNetwork stops capturing network requests.
func (c *Client) DisableNetwork() {
        c.captureMu.Lock()
        c.capturing = false
        c.captureMu.Unlock()
        _, _ = c.send("Network.disable", nil)
}

// GetCapturedRequests returns all network requests captured since
// EnableNetwork was called. Each request includes the URL, method,
// postData, and response body (if available).
func (c *Client) GetCapturedRequests() []CapturedRequest {
        c.captureMu.RLock()
        defer c.captureMu.RUnlock()
        out := make([]CapturedRequest, len(c.capturedRequests))
        copy(out, c.capturedRequests)
        return out
}

// NetworkHeader is a single HTTP header key-value pair.
type NetworkHeader struct {
        Name  string `json:"name"`
        Value string `json:"value"`
}

// NetworkCookie is a cookie attached to a request (from request.headers.Cookie).
type NetworkCookie struct {
        Name  string `json:"name"`
        Value string `json:"value"`
}

// CapturedRequest is a network request captured by EnableNetwork.
type CapturedRequest struct {
        // Request info
        RequestID      string           `json:"requestId"`
        URL            string           `json:"url"`
        Method         string           `json:"method"`
        ResourceType   string           `json:"resourceType,omitempty"`
        PostData       string           `json:"postData,omitempty"`
        RequestHeaders []NetworkHeader  `json:"requestHeaders,omitempty"`
        Cookies        []NetworkCookie  `json:"cookies,omitempty"`
        // Response info
        Status         int              `json:"status"`
        StatusText     string           `json:"statusText,omitempty"`
        ResponseHeaders []NetworkHeader `json:"responseHeaders,omitempty"`
        ResponseBody   string           `json:"responseBody,omitempty"`
        ResponseContentType string     `json:"responseContentType,omitempty"`
        ResponseSize   int64            `json:"responseSize,omitempty"`
        // Timing
        Timestamp      float64          `json:"timestamp,omitempty"`
        WallTime      float64          `json:"wallTime,omitempty"`
        Duration      float64          `json:"duration,omitempty"`
}

// ClearCapturedRequests clears the captured requests buffer.
func (c *Client) ClearCapturedRequests() {
        c.captureMu.Lock()
        c.capturedRequests = nil
        c.requestMap = map[string]int{}
        c.captureMu.Unlock()
}

// parseHeaders converts a CDP headers map (map[string]string or
// map[string][]string) into a sorted slice of NetworkHeader.
func parseHeaders(raw json.RawMessage) []NetworkHeader {
        // CDP sometimes sends headers as map[string]interface{} where values
        // can be string or []string. Try the common map[string]string first.
        var m map[string]string
        if err := json.Unmarshal(raw, &m); err == nil {
                out := make([]NetworkHeader, 0, len(m))
                for k, v := range m {
                        out = append(out, NetworkHeader{Name: k, Value: v})
                }
                return out
        }
        // Fallback: map[string]interface{}
        var mi map[string]interface{}
        if err := json.Unmarshal(raw, &mi); err != nil {
                return nil
        }
        out := make([]NetworkHeader, 0, len(mi))
        for k, v := range mi {
                var vs string
                switch val := v.(type) {
                case string:
                        vs = val
                case []interface{}:
                        // join multiple values with ", " like browsers do
                        parts := make([]string, len(val))
                        for i, p := range val {
                                parts[i], _ = p.(string)
                        }
                        vs = strings.Join(parts, ", ")
                }
                out = append(out, NetworkHeader{Name: k, Value: vs})
        }
        return out
}

// parseCookies extracts cookies from a Cookie header value.
func parseCookies(cookieHeader string) []NetworkCookie {
        if cookieHeader == "" {
                return nil
        }
        parts := strings.Split(cookieHeader, "; ")
        cookies := make([]NetworkCookie, 0, len(parts))
        for _, p := range parts {
                p = strings.TrimSpace(p)
                if p == "" {
                        continue
                }
                idx := strings.Index(p, "=")
                if idx < 0 {
                        continue
                }
                cookies = append(cookies, NetworkCookie{
                        Name:  p[:idx],
                        Value: p[idx+1:],
                })
        }
        return cookies
}

// handleEvent processes CDP events (Network.requestWillBeSent,
// Network.responseReceived, Network.loadingFinished) to capture
// full network traffic including headers, cookies, and response bodies.
func (c *Client) handleEvent(method string, params json.RawMessage) {
        // Check the appropriate capture flag based on the event type.
        // Network.* events need `capturing` (set by EnableNetwork).
        // Fetch.requestPaused needs `fetchEnabled` (set by EnableFetchCapture).
        // Network.eventSourceMessageReceived / Network.webSocketFrameReceived
        // need `sseEnabled` (set by EnableSSECapture).
        c.captureMu.RLock()
        capturing := c.capturing
        c.captureMu.RUnlock()
        c.sseMu.Lock()
        sseEnabled := c.sseEnabled
        c.sseMu.Unlock()
        c.fetchMu.Lock()
        fetchEnabled := c.fetchEnabled
        c.fetchMu.Unlock()

        // Early return ONLY if no capture is enabled at all.
        if !capturing && !sseEnabled && !fetchEnabled {
                return
        }

        // For Fetch events, check fetchEnabled (not `capturing`).
        // For SSE events, check sseEnabled.
        // For other Network events, check `capturing`.
        isFetchEvent := strings.HasPrefix(method, "Fetch.")
        isSSEEvent := method == "Network.eventSourceMessageReceived" ||
                method == "Network.webSocketFrameReceived"
        if isFetchEvent && !fetchEnabled {
                return
        }
        if isSSEEvent && !sseEnabled {
                return
        }
        if !isFetchEvent && !isSSEEvent && !capturing {
                return
        }

        switch method {
        case "Network.requestWillBeSent":
                var ev struct {
                        Request struct {
                                URL      string          `json:"url"`
                                Method   string          `json:"method"`
                                PostData string          `json:"postData"`
                                Headers  json.RawMessage `json:"headers"`
                        } `json:"request"`
                        Type      string  `json:"type"`
                        RequestID string  `json:"requestId"`
                        Timestamp float64 `json:"timestamp"`
                        WallTime float64 `json:"wallTime"`
                }
                if err := json.Unmarshal(params, &ev); err != nil {
                        return
                }

                req := CapturedRequest{
                        RequestID:    ev.RequestID,
                        URL:          ev.Request.URL,
                        Method:       ev.Request.Method,
                        PostData:     ev.Request.PostData,
                        ResourceType: ev.Type,
                        Timestamp:    ev.Timestamp,
                        WallTime:     ev.WallTime,
                }
                // Parse request headers
                if len(ev.Request.Headers) > 0 {
                        req.RequestHeaders = parseHeaders(ev.Request.Headers)
                        // Extract cookies from the Cookie header
                        for _, h := range req.RequestHeaders {
                                if strings.EqualFold(h.Name, "cookie") {
                                        req.Cookies = parseCookies(h.Value)
                                        break
                                }
                        }
                }

                c.captureMu.Lock()
                idx := len(c.capturedRequests)
                c.capturedRequests = append(c.capturedRequests, req)
                c.requestMap[ev.RequestID] = idx
                c.captureMu.Unlock()

        case "Network.responseReceived":
                var ev struct {
                        RequestID  string          `json:"requestId"`
                        Type       string          `json:"type"`
                        Response   struct {
                                Status         int             `json:"status"`
                                StatusText     string          `json:"statusText"`
                                Headers        json.RawMessage `json:"headers"`
                                ContentType    string          `json:"mimeType"`
                                FromServiceWorker bool           `json:"fromServiceWorker"`
                        } `json:"response"`
                        Timestamp  float64 `json:"timestamp"`
                }
                if err := json.Unmarshal(params, &ev); err != nil {
                        return
                }
                c.captureMu.Lock()
                idx, ok := c.requestMap[ev.RequestID]
                if ok && idx < len(c.capturedRequests) {
                        c.capturedRequests[idx].Status = ev.Response.Status
                        c.capturedRequests[idx].StatusText = ev.Response.StatusText
                        c.capturedRequests[idx].ResponseContentType = ev.Response.ContentType
                        if ev.Timestamp > 0 {
                                c.capturedRequests[idx].Duration = ev.Timestamp - c.capturedRequests[idx].Timestamp
                        }
                        if len(ev.Response.Headers) > 0 {
                                c.capturedRequests[idx].ResponseHeaders = parseHeaders(ev.Response.Headers)
                        }
                }
                c.captureMu.Unlock()

        case "Network.loadingFinished":
                var ev struct {
                        RequestID    string  `json:"requestId"`
                        Timestamp    float64 `json:"timestamp"`
                        EncodedDataLength float64 `json:"encodedDataLength"`
                }
                if err := json.Unmarshal(params, &ev); err != nil {
                        return
                }
                // Asynchronously fetch the response body for interesting resources.
                // We do this in a goroutine to avoid blocking the readLoop.
                c.captureMu.RLock()
                idx, ok := c.requestMap[ev.RequestID]
                if !ok || idx >= len(c.capturedRequests) {
                        c.captureMu.RUnlock()
                        return
                }
                req := &c.capturedRequests[idx]
                // Only fetch body for XHR/Fetch/Document types to avoid
                // downloading large images/scripts.
                shouldFetch := req.ResourceType == "XHR" ||
                        req.ResourceType == "Fetch" ||
                        req.ResourceType == "Document" ||
                        req.ResourceType == "WebSocket" ||
                        req.Status == 0 // retry if status not yet set
                c.captureMu.RUnlock()

                if ev.EncodedDataLength > 0 {
                        c.captureMu.Lock()
                        if idx < len(c.capturedRequests) {
                                c.capturedRequests[idx].ResponseSize = int64(ev.EncodedDataLength)
                        }
                        c.captureMu.Unlock()
                }

                // Update duration
                if ev.Timestamp > 0 {
                        c.captureMu.Lock()
                        if idx < len(c.capturedRequests) {
                                c.capturedRequests[idx].Duration = ev.Timestamp - c.capturedRequests[idx].Timestamp
                        }
                        c.captureMu.Unlock()
                }

                if shouldFetch {
                        go c.fetchResponseBody(ev.RequestID)
                }

        case "Network.eventSourceMessageReceived":
                // SSE message — z.ai Agent mode streams output via SSE.
                // This event arrives via CDP WebSocket, NOT JS thread,
                // so it works even when z.ai's JS is blocked.
                c.sseMu.Lock()
                if c.sseEnabled {
                        var ev struct {
                                RequestID string  `json:"requestId"`
                                Timestamp float64 `json:"timestamp"`
                                EventName string  `json:"eventName"`
                                EventID   string  `json:"eventId"`
                                Data      string  `json:"data"`
                        }
                        if err := json.Unmarshal(params, &ev); err == nil {
                                c.sseMessages = append(c.sseMessages, SSEMessage{
                                        Data:      ev.Data,
                                        Timestamp: ev.Timestamp,
                                })
                        }
                }
                c.sseMu.Unlock()

        case "Network.webSocketFrameReceived":
                // WebSocket frame — some z.ai endpoints may use WS instead of SSE.
                c.sseMu.Lock()
                if c.sseEnabled {
                        var ev struct {
                                RequestID struct {
                                        URL string `json:"url"`
                                } `json:"requestId"`
                                Timestamp float64 `json:"timestamp"`
                                Response  struct {
                                        PayloadData string `json:"payloadData"`
                                } `json:"response"`
                        }
                        if err := json.Unmarshal(params, &ev); err == nil {
                                c.sseMessages = append(c.sseMessages, SSEMessage{
                                        Data:      ev.Response.PayloadData,
                                        Timestamp: ev.Timestamp,
                                })
                        }
                }
                c.sseMu.Unlock()

        case "Fetch.requestPaused":
                // Fetch domain event: a network request was paused and is
                // waiting for us to either continue or abort. This event
                // arrives via CDP WebSocket, NOT the page's JS thread, so
                // it works even when z.ai's JS is fully blocked during a
                // long Agent task.
                c.handleFetchPaused(params)
        }
}

// handleFetchPaused processes a Fetch.requestPaused event. We:
//  1. Check the response Content-Type header.
//  2. If it's a streaming response (text/event-stream, application/x-ndjson,
//     application/stream+json, etc.), keep it paused and record it for
//     incremental body chunk polling.
//  3. If it's a non-streaming response (regular JSON, HTML, etc.),
//     snapshot the body immediately and call continueResponse so the
//     page isn't blocked.
//
// This ensures we capture streaming responses (z.ai Agent mode output)
// without blocking the page on non-streaming requests.
func (c *Client) handleFetchPaused(params json.RawMessage) {
        c.fetchMu.Lock()
        if !c.fetchEnabled {
                c.fetchMu.Unlock()
                return
        }
        c.fetchMu.Unlock()

        var ev struct {
                RequestID     string  `json:"requestId"`
                Request       struct {
                        URL    string `json:"url"`
                        Method string `json:"method"`
                } `json:"request"`
                ResourceType string  `json:"resourceType"`
                ResponseStatusCode int `json:"responseStatusCode"`
                ResponseHeaders    []struct {
                        Name  string `json:"name"`
                        Value string `json:"value"`
                } `json:"responseHeaders"`
                Timestamp float64 `json:"timestamp"`
        }
        if err := json.Unmarshal(params, &ev); err != nil {
                return
        }

        // Check Content-Type to decide if this is a streaming response.
        // ONLY use content-type — don't use URL heuristics because they
        // cause false positives (e.g. chats/new is a regular JSON POST
        // but matches "chat" in the URL).
        contentType := ""
        for _, h := range ev.ResponseHeaders {
                if strings.EqualFold(h.Name, "content-type") {
                        contentType = strings.ToLower(h.Value)
                        break
                }
        }
        isStreaming := strings.Contains(contentType, "event-stream") ||
                strings.Contains(contentType, "ndjson") ||
                strings.Contains(contentType, "stream+json") ||
                strings.Contains(contentType, "stream/json") ||
                strings.Contains(contentType, "octet-stream")

        if isStreaming {
                // Streaming response (text/event-stream). Snapshot whatever
                // body has accumulated so far, then CONTINUE the request
                // immediately. We do NOT keep it paused — keeping it paused
                // would prevent z.ai's JS from receiving the response body,
                // which means the UI would never render the response.
                //
                // By continuing immediately, z.ai's fetch() promise resolves
                // and the UI renders the response. Our eval-based polling
                // (with DOM domain fallback) then reads the rendered DOM.
                //
                // We still snapshot the body first (in a goroutine) so the
                // bridge can use the raw stream data as a fallback if the
                // eval-based polling can't read the DOM.
                c.fetchMu.Lock()
                if c.fetchPaused == nil {
                        c.fetchPaused = map[string]int64{}
                }
                c.fetchPaused[ev.RequestID] = 0
                if c.fetchURLs == nil {
                        c.fetchURLs = map[string]string{}
                }
                c.fetchURLs[ev.RequestID] = ev.Request.URL
                c.fetchMu.Unlock()
                log.Printf("[cdp] Fetch.requestPaused (STREAMING, snapshot+continue): %s %s [content-type=%s]",
                        ev.Request.Method, ev.Request.URL, contentType)
                go func() {
                        c.snapshotFetchBody(ev.RequestID, ev.Request.URL, ev.Timestamp)
                        c.continueFetchRequest(ev.RequestID)
                }()
        } else {
                // Non-streaming — snapshot body and continue immediately.
                log.Printf("[cdp] Fetch.requestPaused (non-streaming, continuing): %s %s [content-type=%s]",
                        ev.Request.Method, ev.Request.URL, contentType)
                // Snapshot the body in a goroutine, then continue.
                // We do this in a goroutine so the readLoop isn't blocked.
                go func() {
                        c.snapshotFetchBody(ev.RequestID, ev.Request.URL, ev.Timestamp)
                        c.continueFetchRequest(ev.RequestID)
                }()
        }
}

// snapshotFetchBody calls Fetch.getResponseBody on a paused request and
// records the result as a FetchChunk. This is the ONLY way to read a
// streaming response body while the JS thread is blocked — Fetch domain
// events arrive via the CDP WebSocket, not the page's JS event loop.
//
// Works on PAUSED requests only (between Fetch.requestPaused and
// Fetch.continueResponse). Returns the body accumulated so far; we
// compute the delta vs. the last snapshot and store only the new bytes.
func (c *Client) snapshotFetchBody(requestID, url string, ts float64) {
        resp, err := c.send("Fetch.getResponseBody", map[string]interface{}{
                "requestId": requestID,
        })
        if err != nil {
                return
        }
        var result struct {
                Body          string `json:"body"`
                Base64Encoded bool   `json:"base64Encoded"`
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return
        }
        body := result.Body
        if result.Base64Encoded {
                dec, err := base64.StdEncoding.DecodeString(body)
                if err == nil {
                        body = string(dec)
                }
        }
        if body == "" {
                return
        }
        c.fetchMu.Lock()
        defer c.fetchMu.Unlock()
        if !c.fetchEnabled {
                return
        }
        // Look up URL from map if not provided.
        if url == "" {
                url = c.fetchURLs[requestID]
        }
        prev := c.fetchPaused[requestID]
        if int64(len(body)) <= prev {
                // No new bytes since last snapshot.
                return
        }
        // Only store the new bytes.
        chunk := body[prev:]
        c.fetchPaused[requestID] = int64(len(body))
        c.fetchChunks = append(c.fetchChunks, FetchChunk{
                RequestID: requestID,
                URL:       url,
                Chunk:     chunk,
                Offset:    prev,
                Timestamp: ts,
        })
}

// continueFetchRequest resumes a paused request so the page receives the
// response. Called by FinishFetchRequest (bridge-driven) when the stream
// is done or after a max timeout.
func (c *Client) continueFetchRequest(requestID string) {
        _, _ = c.send("Fetch.continueResponse", map[string]interface{}{
                "requestId": requestID,
        })
        c.fetchMu.Lock()
        delete(c.fetchPaused, requestID)
        delete(c.fetchURLs, requestID)
        c.fetchMu.Unlock()
}

// FinishFetchRequest resumes a paused request (calls Fetch.continueResponse)
// and removes it from the polling set. The bridge calls this when it
// detects the stream is done or after a max timeout.
func (c *Client) FinishFetchRequest(requestID string) {
        c.continueFetchRequest(requestID)
}

// FinishAllFetchRequests resumes ALL currently-paused requests. The bridge
// calls this on shutdown or when cancelling a task.
func (c *Client) FinishAllFetchRequests() {
        c.fetchMu.Lock()
        ids := make([]string, 0, len(c.fetchPaused))
        for id := range c.fetchPaused {
                ids = append(ids, id)
        }
        c.fetchMu.Unlock()
        for _, id := range ids {
                c.continueFetchRequest(id)
        }
}

// EnableFetchCapture enables the Fetch domain and starts intercepting
// network requests whose URL contains the filter substring. Captured
// streaming response chunks are stored and returned by GetFetchChunks.
//
// Key property: Fetch.requestPaused events arrive via the CDP WebSocket,
// NOT via the page's JS event loop. This means we can capture streaming
// response bodies (like z.ai's Agent mode output) even when z.ai's JS
// thread is fully blocked during a long task execution.
//
// The filter should match z.ai's chat/agent API endpoint, e.g.
// "chat.z.ai/api/" or "/api/chat" — this avoids intercepting images,
// scripts, fonts, etc.
//
// IMPORTANT: paused requests are NOT auto-continued. The bridge MUST
// call FinishFetchRequest(requestId) or FinishAllFetchRequests() when
// done, otherwise the page's fetch promise stays pending forever.
// EnableFetchCapture also auto-resumes any leftover paused requests
// from a previous capture session, so it's safe to call repeatedly.
func (c *Client) EnableFetchCapture(urlFilter string) error {
        // First, resume any leftover paused requests from a previous
        // capture session. This prevents resource leaks (paused requests
        // would otherwise stay paused forever at the CDP level).
        c.FinishAllFetchRequests()

        c.fetchMu.Lock()
        c.fetchEnabled = true
        c.fetchChunks = nil
        c.fetchPaused = map[string]int64{}
        c.fetchURLs = map[string]string{}
        c.fetchFilter = urlFilter
        c.fetchMu.Unlock()

        // Build a URL-pattern filter. CDP Fetch.enable accepts a list of
        // URL patterns to intercept. We use urlMatch with the substring
        // (matches any URL containing it) at the Response stage.
        params := map[string]interface{}{
                "patterns": []map[string]interface{}{
                        {
                                "requestStage": "Response",
                        },
                },
        }
        if urlFilter != "" {
                params["patterns"] = []map[string]interface{}{
                        {
                                "urlMatch":     urlFilter,
                                "requestStage": "Response",
                        },
                }
        }
        _, err := c.send("Fetch.enable", params)
        if err != nil {
                return fmt.Errorf("Fetch.enable: %w", err)
        }
        log.Printf("[cdp] Fetch.enable: filter=%q", urlFilter)
        return nil
}

// DisableFetchCapture stops intercepting network requests and resumes
// any currently-paused requests.
func (c *Client) DisableFetchCapture() {
        c.fetchMu.Lock()
        c.fetchEnabled = false
        ids := make([]string, 0, len(c.fetchPaused))
        for id := range c.fetchPaused {
                ids = append(ids, id)
        }
        c.fetchMu.Unlock()
        for _, id := range ids {
                _, _ = c.send("Fetch.continueResponse", map[string]interface{}{
                        "requestId": id,
                })
        }
        _, _ = c.send("Fetch.disable", nil)
        log.Printf("[cdp] Fetch.disable: resumed %d paused requests", len(ids))
}

// GetFetchChunks returns all captured Fetch chunks since the last call
// and clears the buffer. Each call returns only NEW chunks.
func (c *Client) GetFetchChunks() []FetchChunk {
        c.fetchMu.Lock()
        defer c.fetchMu.Unlock()
        out := c.fetchChunks
        c.fetchChunks = nil
        return out
}

// GetPausedRequestIDs returns the request IDs of all currently-paused
// Fetch requests. The bridge uses this to know which requests to finish
// when the task completes.
func (c *Client) GetPausedRequestIDs() []string {
        c.fetchMu.Lock()
        defer c.fetchMu.Unlock()
        out := make([]string, 0, len(c.fetchPaused))
        for id := range c.fetchPaused {
                out = append(out, id)
        }
        return out
}

// PollFetchBodies re-snapshots all in-flight paused/streaming requests
// and records any new chunks collected. This is the key method to call
// during long z.ai Agent tasks — it works even when z.ai's JS thread
// is fully blocked, because all I/O is via CDP WebSocket.
//
// Each call issues Fetch.getResponseBody for every currently-paused
// request, computes the delta vs. the last snapshot, and appends new
// bytes to c.fetchChunks. The bridge then calls GetFetchChunks to
// retrieve and clear the buffer.
func (c *Client) PollFetchBodies() {
        c.fetchMu.Lock()
        if !c.fetchEnabled {
                c.fetchMu.Unlock()
                return
        }
        ids := make([]string, 0, len(c.fetchPaused))
        for id := range c.fetchPaused {
                ids = append(ids, id)
        }
        c.fetchMu.Unlock()
        ts := float64(time.Now().UnixNano()) / 1e9
        for _, id := range ids {
                // Synchronous (not goroutine) so we don't spam Fetch.getResponseBody
                // and so the chunks are ordered. Each call is fast (just reads
                // the buffered body, no network I/O).
                c.snapshotFetchBody(id, "", ts)
        }
}
func (c *Client) fetchResponseBody(requestID string) {
        resp, err := c.send("Network.getResponseBody", map[string]interface{}{
                "requestId": requestID,
        })
        if err != nil {
                return // body not available (e.g. cached, redirected)
        }
        var result struct {
                Body          string `json:"body"`
                Base64Encoded bool   `json:"base64Encoded"`
        }
        if err := json.Unmarshal(resp, &result); err != nil {
                return
        }
        c.captureMu.Lock()
        defer c.captureMu.Unlock()
        idx, ok := c.requestMap[requestID]
        if !ok || idx >= len(c.capturedRequests) {
                return
        }
        if result.Base64Encoded {
                // Store raw base64, let the consumer decode
                c.capturedRequests[idx].ResponseBody = "[base64]" + result.Body
        } else {
                // Truncate very large bodies to avoid memory issues
                body := result.Body
                if len(body) > 512*1024 {
                        body = body[:512*1024] + "... [truncated]"
                }
                c.capturedRequests[idx].ResponseBody = body
        }
}
func (c *Client) readLoop() {
        for {
                c.mu.Lock()
                conn := c.conn
                c.mu.Unlock()
                if conn == nil {
                        return
                }
                _, data, err := conn.ReadMessage()
                if err != nil {
                        // Connection closed or errored — fail all pending.
                        c.mu.Lock()
                        for id, ch := range c.pending {
                                ch <- cdpResponse{err: err}
                                delete(c.pending, id)
                        }
                        c.mu.Unlock()
                        return
                }
                var msg struct {
                        ID        int64           `json:"id"`
                        Result    json.RawMessage `json:"result"`
                        Error     *struct {
                                Code    int    `json:"code"`
                                Message string `json:"message"`
                        } `json:"error,omitempty"`
                        Method   string          `json:"method,omitempty"` // for events
                        Params   json.RawMessage `json:"params,omitempty"`
                        SessionID string         `json:"sessionId,omitempty"` // flat session
                }
                if err := json.Unmarshal(data, &msg); err != nil {
                        continue
                }
                if msg.ID == 0 {
                        // Event (not a response). Handle Network events if capturing.
                        if msg.Method != "" {
                                c.handleEvent(msg.Method, msg.Params)
                        }
                        continue
                }
                c.mu.Lock()
                ch, ok := c.pending[msg.ID]
                if ok {
                        delete(c.pending, msg.ID)
                }
                c.mu.Unlock()
                if !ok {
                        continue
                }
                if msg.Error != nil {
                        ch <- cdpResponse{err: fmt.Errorf("cdp error %d: %s", msg.Error.Code, msg.Error.Message)}
                } else {
                        ch <- cdpResponse{result: msg.Result}
                }
        }
}

// Send is the public version of send, for use by other packages
// (e.g. tab_worker.go needs to call Runtime.evaluate directly).
func (c *Client) Send(method string, params interface{}) (json.RawMessage, error) {
        return c.send(method, params)
}

// GetDOMText reads the text content of elements matching a CSS selector
// via CDP DOM domain (NOT Runtime.evaluate). This bypasses the JS thread
// entirely, so it works even when z.ai's JS is blocked during task
// execution (e.g. running bash commands for minutes).
//
// Returns the innerHTML of the last matching element, or "" if none.
func (c *Client) GetDOMText(selector string) (string, error) {
        // 1. Get the root document
        resp, err := c.send("DOM.getDocument", map[string]interface{}{
                "depth": 0,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.getDocument: %w", err)
        }
        var doc struct {
                Root struct {
                        NodeID int `json:"nodeId"`
                } `json:"root"`
        }
        if err := json.Unmarshal(resp, &doc); err != nil {
                return "", fmt.Errorf("decode document: %w", err)
        }

        // 2. Query selector for the target element
        resp, err = c.send("DOM.querySelector", map[string]interface{}{
                "nodeId":  doc.Root.NodeID,
                "selector": selector,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.querySelector: %w", err)
        }
        var qs struct {
                NodeID int `json:"nodeId"`
        }
        if err := json.Unmarshal(resp, &qs); err != nil {
                return "", fmt.Errorf("decode querySelector: %w", err)
        }
        if qs.NodeID == 0 {
                return "", nil // no match
        }

        // 3. Get outer HTML
        resp, err = c.send("DOM.getOuterHTML", map[string]interface{}{
                "nodeId": qs.NodeID,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.getOuterHTML: %w", err)
        }
        var oh struct {
                OuterHTML string `json:"outerHTML"`
        }
        if err := json.Unmarshal(resp, &oh); err != nil {
                return "", fmt.Errorf("decode outerHTML: %w", err)
        }
        return oh.OuterHTML, nil
}

// GetDOMTextLast reads the innerHTML of the LAST element matching a CSS
// selector via CDP DOM domain. This is like GetDOMText but returns the
// last match instead of the first — needed for z.ai chat pages where
// multiple assistant messages exist and we want the newest one.
//
// Bypasses the JS thread entirely, so it works even when z.ai's JS is
// blocked during task execution.
func (c *Client) GetDOMTextLast(selector string) (string, error) {
        // 1. Get the root document
        resp, err := c.send("DOM.getDocument", map[string]interface{}{
                "depth": 0,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.getDocument: %w", err)
        }
        var doc struct {
                Root struct {
                        NodeID int `json:"nodeId"`
                } `json:"root"`
        }
        if err := json.Unmarshal(resp, &doc); err != nil {
                return "", fmt.Errorf("decode document: %w", err)
        }

        // 2. Query selector ALL for all matching elements
        resp, err = c.send("DOM.querySelectorAll", map[string]interface{}{
                "nodeId":   doc.Root.NodeID,
                "selector": selector,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.querySelectorAll: %w", err)
        }
        var qs struct {
                NodeIDs []int `json:"nodeIds"`
        }
        if err := json.Unmarshal(resp, &qs); err != nil {
                return "", fmt.Errorf("decode querySelectorAll: %w", err)
        }
        if len(qs.NodeIDs) == 0 {
                return "", nil // no match
        }

        // 3. Get the LAST matching element's outer HTML
        lastNodeID := qs.NodeIDs[len(qs.NodeIDs)-1]
        resp, err = c.send("DOM.getOuterHTML", map[string]interface{}{
                "nodeId": lastNodeID,
        })
        if err != nil {
                return "", fmt.Errorf("DOM.getOuterHTML: %w", err)
        }
        var oh struct {
                OuterHTML string `json:"outerHTML"`
        }
        if err := json.Unmarshal(resp, &oh); err != nil {
                return "", fmt.Errorf("decode outerHTML: %w", err)
        }
        return oh.OuterHTML, nil
}

// GetDOMTextAll reads the outerHTML of ALL elements matching a CSS selector
// via CDP DOM domain. Returns a slice of HTML strings (one per match).
//
// This is like GetDOMTextLast but returns ALL matches instead of just the
// last one — needed by the bridge to filter out OLD assistant messages
// (only consider messages at index >= pre_count, i.e. NEW responses).
//
// Bypasses the JS thread entirely, so it works even when z.ai's JS is
// blocked during task execution.
func (c *Client) GetDOMTextAll(selector string) ([]string, error) {
        // 1. Get the root document
        resp, err := c.send("DOM.getDocument", map[string]interface{}{
                "depth": 0,
        })
        if err != nil {
                return nil, fmt.Errorf("DOM.getDocument: %w", err)
        }
        var doc struct {
                Root struct {
                        NodeID int `json:"nodeId"`
                } `json:"root"`
        }
        if err := json.Unmarshal(resp, &doc); err != nil {
                return nil, fmt.Errorf("decode document: %w", err)
        }

        // 2. Query selector ALL for all matching elements
        resp, err = c.send("DOM.querySelectorAll", map[string]interface{}{
                "nodeId":   doc.Root.NodeID,
                "selector": selector,
        })
        if err != nil {
                return nil, fmt.Errorf("DOM.querySelectorAll: %w", err)
        }
        var qs struct {
                NodeIDs []int `json:"nodeIds"`
        }
        if err := json.Unmarshal(resp, &qs); err != nil {
                return nil, fmt.Errorf("decode querySelectorAll: %w", err)
        }
        if len(qs.NodeIDs) == 0 {
                return nil, nil // no match
        }

        // 3. Get OUTER HTML of ALL matching elements
        out := make([]string, 0, len(qs.NodeIDs))
        for _, nodeID := range qs.NodeIDs {
                resp, err = c.send("DOM.getOuterHTML", map[string]interface{}{
                        "nodeId": nodeID,
                })
                if err != nil {
                        continue // skip on error, return what we have
                }
                var oh struct {
                        OuterHTML string `json:"outerHTML"`
                }
                if err := json.Unmarshal(resp, &oh); err != nil {
                        continue
                }
                out = append(out, oh.OuterHTML)
        }
        return out, nil
}

// DispatchEnterKey sends a real Enter keydown+keyup via CDP Input domain.
// This is more reliable than JS KeyboardEvent for React/Svelte apps that
// use synthetic events. Focuses the element with the given selector first.
func (c *Client) DispatchEnterKey(selector string) error {
        // Focus the element via Runtime.evaluate
        focusJS := fmt.Sprintf(`(function(){
                var el = document.querySelector(%q);
                if (el) { el.focus(); return 'ok'; }
                return 'not_found';
        })()`, selector)
        _, err := c.send("Runtime.evaluate", map[string]interface{}{
                "expression":  focusJS,
                "returnByValue": true,
        })
        if err != nil {
                return fmt.Errorf("focus: %w", err)
        }
        // Send keydown + keyup via Input.dispatchKeyEvent
        for _, evtType := range []string{"keyDown", "keyUp"} {
                _, err := c.send("Input.dispatchKeyEvent", map[string]interface{}{
                        "type":                  evtType,
                        "key":                   "Enter",
                        "code":                  "Enter",
                        "windowsVirtualKeyCode": 13,
                })
                if err != nil {
                        return fmt.Errorf("%s: %w", evtType, err)
                }
        }
        return nil
}

// send sends a CDP command and waits for the response.
// On timeout, attempts to reconnect once and retries.
func (c *Client) send(method string, params interface{}) (json.RawMessage, error) {
        result, err := c.sendNoRetry(method, params)
        if err == nil {
                return result, nil
        }
        // On timeout, try reconnecting once
        if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "write") {
                log.Printf("[cdp] %s failed (%v), attempting reconnect", method, err)
                if rerr := c.Reconnect(); rerr != nil {
                        return nil, fmt.Errorf("%w (reconnect failed: %v)", err, rerr)
                }
                return c.sendNoRetry(method, params)
        }
        return result, err
}

// sendNoRetry sends a CDP command and waits for the response (no retry).
func (c *Client) sendNoRetry(method string, params interface{}) (json.RawMessage, error) {
        id := c.nextID.Add(1)
        ch := make(chan cdpResponse, 1)
        c.mu.Lock()
        if c.conn == nil {
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: not connected")
        }
        c.pending[id] = ch
        payload := struct {
                ID     int64       `json:"id"`
                Method string      `json:"method"`
                Params interface{} `json:"params,omitempty"`
        }{
                ID:     id,
                Method: method,
                Params: params,
        }
        // Set a write deadline so a half-open WebSocket fails fast
        _ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
        if err := c.conn.WriteJSON(payload); err != nil {
                delete(c.pending, id)
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: write: %w", err)
        }
        _ = c.conn.SetWriteDeadline(time.Time{}) // reset
        c.mu.Unlock()

        select {
        case r := <-ch:
                return r.result, r.err
        case <-time.After(10 * time.Second):
                c.mu.Lock()
                delete(c.pending, id)
                c.mu.Unlock()
                log.Printf("[cdp] timeout waiting for %s response (10s)", method)
                return nil, fmt.Errorf("cdp: timeout waiting for %s response", method)
        }
}

// IsConnected returns true if the CDP WebSocket appears to be open.
// This is a heuristic — the underlying connection may be half-open
// (broken but not yet detected by the OS).
func (c *Client) IsConnected() bool {
        c.mu.Lock()
        defer c.mu.Unlock()
        return c.conn != nil
}

// Reconnect attempts to reconnect to the same CDP endpoint. Useful when
// the underlying WebSocket has become stale (e.g. the tab worker process
// was killed and respawned, breaking the old connection).
func (c *Client) Reconnect() error {
        c.mu.Lock()
        if c.conn != nil {
                c.conn.Close()
                c.conn = nil
        }
        // Fail all pending requests
        for id, ch := range c.pending {
                ch <- cdpResponse{err: fmt.Errorf("reconnecting")}
                delete(c.pending, id)
        }
        c.mu.Unlock()
        // Reconnect
        conn, _, err := websocket.DefaultDialer.Dial(c.endpoint, nil)
        if err != nil {
                return fmt.Errorf("cdp: reconnect to %s: %w", c.endpoint, err)
        }
        c.mu.Lock()
        c.conn = conn
        c.mu.Unlock()
        go c.readLoop()
        log.Printf("[cdp] reconnected to %s", c.endpoint)
        return nil
}

// sendAsync sends a CDP command without waiting for the response. Used
// for high-frequency commands like Input.dispatchMouseEvent during a
// drag, where waiting for each response would make the drag take 10x
// longer than the requested duration.
func (c *Client) sendAsync(method string, params interface{}) error {
        id := c.nextID.Add(1)
        c.mu.Lock()
        defer c.mu.Unlock()
        if c.conn == nil {
                return fmt.Errorf("cdp: not connected")
        }
        // Set a write deadline so a stalled WebSocket doesn't block the
        // drag goroutine forever.
        _ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
        defer c.conn.SetWriteDeadline(time.Time{}) // reset
        payload := struct {
                ID     int64       `json:"id"`
                Method string      `json:"method"`
                Params interface{} `json:"params,omitempty"`
        }{
                ID:     id,
                Method: method,
                Params: params,
        }
        if err := c.conn.WriteJSON(payload); err != nil {
                return fmt.Errorf("cdp: write: %w", err)
        }
        return nil
}

// sendWithSession sends a CDP command to a specific session (e.g. an
// iframe target attached via Target.attachToTarget). The sessionId
// field routes the command to that target's context.
func (c *Client) sendWithSession(method string, params interface{}, sessionID string) (json.RawMessage, error) {
        id := c.nextID.Add(1)
        ch := make(chan cdpResponse, 1)
        c.mu.Lock()
        if c.conn == nil {
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: not connected")
        }
        c.pending[id] = ch
        payload := struct {
                ID        int64       `json:"id"`
                Method    string      `json:"method"`
                Params    interface{} `json:"params,omitempty"`
                SessionID string      `json:"sessionId,omitempty"`
        }{
                ID:        id,
                Method:    method,
                Params:    params,
                SessionID: sessionID,
        }
        if err := c.conn.WriteJSON(payload); err != nil {
                delete(c.pending, id)
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: write: %w", err)
        }
        c.mu.Unlock()

        select {
        case r := <-ch:
                return r.result, r.err
        case <-time.After(10 * time.Second):
                c.mu.Lock()
                delete(c.pending, id)
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: timeout waiting for %s (session %s) response", method, sessionID)
        }
}

// DispatchRawSession sends a single CDP mouse event to a specific
// session (e.g. iframe target). This is the key method for baxia:
// by dispatching mouseup from the iframe's own session, the event
// routes directly to the iframe's document — same path as mousedown.
func (c *Client) DispatchRawSession(opts RawMouseOpts, sessionID string) error {
        params := dispatchMouseParams{
                Type:       MouseEventType(opts.Type),
                X:          opts.X,
                Y:          opts.Y,
                Button:     MouseButton(opts.Button),
                Buttons:    opts.Buttons,
                ClickCount: opts.ClickCount,
        }
        if opts.Timestamp > 0 {
                params.Timestamp = opts.Timestamp
        }
        _, err := c.sendWithSession("Input.dispatchMouseEvent", params, sessionID)
        return err
}

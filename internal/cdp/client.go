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
        c.captureMu.RLock()
        capturing := c.capturing
        c.captureMu.RUnlock()
        if !capturing {
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
        }
}

// fetchResponseBody calls Network.getResponseBody for a finished request
// and stores the result in the captured request entry.
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

// send sends a CDP command and waits for the response.
func (c *Client) send(method string, params interface{}) (json.RawMessage, error) {
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
        if err := c.conn.WriteJSON(payload); err != nil {
                delete(c.pending, id)
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: write: %w", err)
        }
        c.mu.Unlock()

        select {
        case r := <-ch:
                return r.result, r.err
        case <-time.After(5 * time.Second):
                c.mu.Lock()
                delete(c.pending, id)
                c.mu.Unlock()
                return nil, fmt.Errorf("cdp: timeout waiting for %s response", method)
        }
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

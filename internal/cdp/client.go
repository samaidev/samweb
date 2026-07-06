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
                conn:     conn,
                pending:  map[int64]chan cdpResponse{},
                endpoint: wsURL,
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

// EnableNetwork enables the CDP Network domain and captures all network
// requests. After calling this, GetCapturedRequests returns a list of
// all requests made by the page (including XHR, fetch, and resource
// loads). This is used to capture baxia's verification API calls.
func (c *Client) EnableNetwork() error {
        // Start capturing before enabling
        c.captureMu.Lock()
        c.capturing = true
        c.capturedRequests = nil
        c.captureMu.Unlock()
        // Enable Network domain with response bodies
        _, err := c.send("Network.enable", map[string]interface{}{
                "maxTotalBufferSize":    10 * 1024 * 1024,
                "maxResourceBufferSize": 5 * 1024 * 1024,
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

// CapturedRequest is a network request captured by EnableNetwork.
type CapturedRequest struct {
        URL       string `json:"url"`
        Method    string `json:"method"`
        PostData  string `json:"postData,omitempty"`
        Status    int    `json:"status"`
        ResponseBody string `json:"responseBody,omitempty"`
        ResourceType string `json:"resourceType,omitempty"`
}

// ClearCapturedRequests clears the captured requests buffer.
func (c *Client) ClearCapturedRequests() {
        c.captureMu.Lock()
        c.capturedRequests = nil
        c.captureMu.Unlock()
}

// handleEvent processes CDP events (Network.requestWillBeSent, etc.)
// to capture network traffic.
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
                                URL    string `json:"url"`
                                Method string `json:"method"`
                                PostData string `json:"postData"`
                        } `json:"request"`
                        Type string `json:"type"`
                        RequestID string `json:"requestId"`
                }
                if err := json.Unmarshal(params, &ev); err != nil {
                        return
                }
                c.captureMu.Lock()
                c.capturedRequests = append(c.capturedRequests, CapturedRequest{
                        URL: ev.Request.URL,
                        Method: ev.Request.Method,
                        PostData: ev.Request.PostData,
                        ResourceType: ev.Type,
                })
                // Store request ID for later matching with response
                if len(c.capturedRequests) > 0 {
                        c.capturedRequests[len(c.capturedRequests)-1].Status = 0
                }
                c.captureMu.Unlock()
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
                        ID     int64           `json:"id"`
                        Result json.RawMessage `json:"result"`
                        Error  *struct {
                                Code    int    `json:"code"`
                                Message string `json:"message"`
                        } `json:"error,omitempty"`
                        Method string          `json:"method,omitempty"` // for events
                        Params json.RawMessage `json:"params,omitempty"`
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

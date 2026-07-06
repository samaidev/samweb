package agent

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "net/url"
        "strings"
        "time"
)

// Client is a Go client SDK for the Agent HTTP API.
//
// It is the recommended way for Go-based agents to talk to a running
// SamWeb instance. The client is goroutine-safe and reuses a single
// http.Client.
type Client struct {
        baseURL string
        token   string
        http    *http.Client
}

// NewClient returns a Client for the agent server at baseURL
// (e.g. "http://127.0.0.1:7777"). If token is non-empty, it is sent as
// "Authorization: Bearer <token>" with every request.
func NewClient(baseURL, token string) *Client {
        baseURL = strings.TrimRight(baseURL, "/")
        return &Client{
                baseURL: baseURL,
                token:   token,
                http: &http.Client{
                        Timeout: 120 * time.Second,
                },
        }
}

// ----------------------------- low-level -----------------------------

func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
        var bodyReader io.Reader
        if body != nil {
                b, err := json.Marshal(body)
                if err != nil {
                        return fmt.Errorf("marshal body: %w", err)
                }
                bodyReader = bytes.NewReader(b)
        }
        reqURL := c.baseURL + path
        req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
        if err != nil {
                return err
        }
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        respBody, _ := io.ReadAll(resp.Body)
        if resp.StatusCode >= 400 {
                var errBody struct {
                        Error string `json:"error"`
                }
                _ = json.Unmarshal(respBody, &errBody)
                msg := errBody.Error
                if msg == "" {
                        msg = strings.TrimSpace(string(respBody))
                }
                if msg == "" {
                        msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
                }
                return fmt.Errorf("agent: %s", msg)
        }
        if out != nil {
                if err := json.Unmarshal(respBody, out); err != nil {
                        return fmt.Errorf("decode response: %w", err)
                }
        }
        return nil
}

// ----------------------------- high-level -----------------------------

// Health pings the agent server.
func (c *Client) Health(ctx context.Context) error {
        return c.do(ctx, "GET", "/agent/health", nil, nil)
}

// State returns the current browser state.
func (c *Client) State(ctx context.Context) (*State, error) {
        var st State
        if err := c.do(ctx, "GET", "/agent/state", nil, &st); err != nil {
                return nil, err
        }
        return &st, nil
}

// Navigate navigates the active tab to url.
func (c *Client) Navigate(ctx context.Context, url string) error {
        return c.do(ctx, "POST", "/agent/navigate", NavigateOpts{URL: url}, nil)
}

// Back goes back in history.
func (c *Client) Back(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/back", nil, nil)
}

// Forward goes forward in history.
func (c *Client) Forward(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/forward", nil, nil)
}

// Reload reloads the active tab.
func (c *Client) Reload(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/reload", nil, nil)
}

// Stop stops the current navigation.
func (c *Client) Stop(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/stop", nil, nil)
}

// Click clicks an element.
func (c *Client) Click(ctx context.Context, opts ClickOpts) error {
        return c.do(ctx, "POST", "/agent/click", opts, nil)
}

// Scroll scrolls the page.
func (c *Client) Scroll(ctx context.Context, opts ScrollOpts) error {
        return c.do(ctx, "POST", "/agent/scroll", opts, nil)
}

// Type types text into an element.
func (c *Client) Type(ctx context.Context, opts TypeOpts) error {
        return c.do(ctx, "POST", "/agent/type", opts, nil)
}

// PressKey presses a key.
func (c *Client) PressKey(ctx context.Context, opts KeyOpts) error {
        return c.do(ctx, "POST", "/agent/key", opts, nil)
}

// Drag dispatches a human-like drag (cubic bezier + jitter + random
// delays) from one element/point to another. Used for slider captchas.
func (c *Client) Drag(ctx context.Context, opts DragOpts) error {
        return c.do(ctx, "POST", "/agent/drag", opts, nil)
}

// DragTrusted dispatches a CDP-injected trusted drag (isTrusted=true).
// Required for Aliyun baxia / Geetest sliders that reject JS-dispatched
// events. Coordinates are in CSS pixels relative to the TOP-LEVEL page's
// viewport (NOT iframe-local).
func (c *Client) DragTrusted(ctx context.Context, opts TrustedDragOpts) error {
        return c.do(ctx, "POST", "/agent/drag-trusted", opts, nil)
}

// Eval evaluates a JavaScript expression and returns the JSON-encoded result.
func (c *Client) Eval(ctx context.Context, script string) (json.RawMessage, error) {
        var res EvalResult
        if err := c.do(ctx, "POST", "/agent/eval", EvalOpts{Script: script}, &res); err != nil {
                return nil, err
        }
        return res.Value, nil
}

// Wait waits for an element to appear in the DOM.
func (c *Client) Wait(ctx context.Context, selector string, timeoutMs int) error {
        return c.do(ctx, "POST", "/agent/wait", WaitOpts{Selector: selector, TimeoutMs: timeoutMs}, nil)
}

// Elements returns all elements matching selector.
func (c *Client) Elements(ctx context.Context, selector string) ([]Element, error) {
        var res ElementsResult
        q := url.Values{}
        q.Set("selector", selector)
        if err := c.do(ctx, "GET", "/agent/elements?"+q.Encode(), nil, &res); err != nil {
                return nil, err
        }
        return res.Elements, nil
}

// Element returns the first element matching selector.
func (c *Client) Element(ctx context.Context, selector string) (*Element, error) {
        var el Element
        q := url.Values{}
        q.Set("selector", selector)
        if err := c.do(ctx, "GET", "/agent/element?"+q.Encode(), nil, &el); err != nil {
                return nil, err
        }
        return &el, nil
}

// Screenshot returns a PNG screenshot of the current view.
func (c *Client) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
        q := url.Values{}
        if fullPage {
                q.Set("fullPage", "true")
        }
        req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/agent/screenshot?"+q.Encode(), nil)
        if err != nil {
                return nil, err
        }
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(resp.Body)
                return nil, fmt.Errorf("agent: HTTP %d: %s", resp.StatusCode, body)
        }
        return io.ReadAll(resp.Body)
}

// ResetCookies clears all cookies in the backend's cookie jar so the next
// navigation starts a fresh session (no cached login, no anti-bot cookies).
func (c *Client) ResetCookies(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/reset-cookies", nil, nil)
}

// SaveCookies persists the cookie jar to disk so the session survives
// process restarts. Call this after a successful login to make the
// "log in once, stay logged in forever" workflow work.
func (c *Client) SaveCookies(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/save-cookies", nil, nil)
}

// LoadCookies re-reads the cookie jar from disk, replacing in-memory
// cookies. Useful after SaveCookies on another process, or after
// manually editing the cookie file.
func (c *Client) LoadCookies(ctx context.Context) error {
        return c.do(ctx, "POST", "/agent/load-cookies", nil, nil)
}

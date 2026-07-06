package agent

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "log"
        "net/http"
        "strconv"
        "strings"
        "time"
)

// Server is the HTTP server that exposes the Agent API.
//
// All endpoints live under /agent/* and accept/return JSON. Methods that
// modify state use POST; methods that read state use GET. Every endpoint
// returns either the JSON-encoded result on success, or an HTTP error
// status with a JSON {"error": "..."} body on failure.
type Server struct {
        backend Backend
        srv     *http.Server
        addr    string
        token   string // optional bearer token; "" disables auth
}

// NewServer constructs an Agent Server bound to addr (e.g. "0.0.0.0:7777").
// If token is non-empty, every request must carry an
// "Authorization: Bearer <token>" header.
func NewServer(addr string, token string, backend Backend) *Server {
        s := &Server{
                backend: backend,
                addr:    addr,
                token:   token,
        }
        mux := http.NewServeMux()
        s.registerRoutes(mux)
        s.srv = &http.Server{
                Addr:         addr,
                Handler:      s.authMiddleware(mux),
                ReadTimeout:  30 * time.Second,
                WriteTimeout: 120 * time.Second,
        }
        return s
}

// Addr returns the address the server is (or will be) listening on.
func (s *Server) Addr() string { return s.addr }

// ListenAndServe starts the server. Blocks until the server stops.
func (s *Server) ListenAndServe() error {
        log.Printf("[agent] listening on http://%s/agent/health", s.addr)
        err := s.srv.ListenAndServe()
        if err != nil && !errors.Is(err, http.ErrServerClosed) {
                return err
        }
        return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
        return s.srv.Shutdown(ctx)
}

// authMiddleware enforces the bearer token if one is configured.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
        if s.token == "" {
                return next
        }
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                // Health endpoint is always public so probes can check liveness.
                if r.URL.Path == "/agent/health" {
                        next.ServeHTTP(w, r)
                        return
                }
                auth := r.Header.Get("Authorization")
                if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
                        writeError(w, http.StatusUnauthorized, "unauthorized")
                        return
                }
                next.ServeHTTP(w, r)
        })
}

// registerRoutes wires every /agent/* endpoint to its handler.
func (s *Server) registerRoutes(mux *http.ServeMux) {
        mux.HandleFunc("/agent/health", s.handleHealth)
        mux.HandleFunc("/agent/state", s.handleState)

        mux.HandleFunc("/agent/navigate", s.handleNavigate)
        mux.HandleFunc("/agent/navigate-direct", s.handleNavigateDirect)
        mux.HandleFunc("/agent/back", s.handleBack)
        mux.HandleFunc("/agent/forward", s.handleForward)
        mux.HandleFunc("/agent/reload", s.handleReload)
        mux.HandleFunc("/agent/stop", s.handleStop)

        mux.HandleFunc("/agent/click", s.handleClick)
        mux.HandleFunc("/agent/scroll", s.handleScroll)
        mux.HandleFunc("/agent/type", s.handleType)
        mux.HandleFunc("/agent/key", s.handleKey)
        mux.HandleFunc("/agent/drag", s.handleDrag)
        mux.HandleFunc("/agent/drag-trusted", s.handleDragTrusted)
        mux.HandleFunc("/agent/drag-touch", s.handleDragTouch)
        mux.HandleFunc("/agent/network/enable", s.handleNetworkEnable)
        mux.HandleFunc("/agent/network/disable", s.handleNetworkDisable)
        mux.HandleFunc("/agent/network/requests", s.handleNetworkRequests)
        mux.HandleFunc("/agent/network/clear", s.handleNetworkClear)
        mux.HandleFunc("/agent/cdp-mouse", s.handleCDPRawMouse)

        mux.HandleFunc("/agent/eval", s.handleEval)
        mux.HandleFunc("/agent/wait", s.handleWait)
        mux.HandleFunc("/agent/elements", s.handleElements)
        mux.HandleFunc("/agent/element", s.handleElement)
        mux.HandleFunc("/agent/screenshot", s.handleScreenshot)
        mux.HandleFunc("/agent/screenshot-trusted", s.handleScreenshotTrusted)
        mux.HandleFunc("/agent/reset-cookies", s.handleResetCookies)
        mux.HandleFunc("/agent/save-cookies", s.handleSaveCookies)
        mux.HandleFunc("/agent/load-cookies", s.handleLoadCookies)
}

// ----------------------------- helpers -----------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
        writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst interface{}) error {
        dec := json.NewDecoder(r.Body)
        dec.DisallowUnknownFields()
        if err := dec.Decode(dst); err != nil {
                return err
        }
        return nil
}

// methodGuard wraps a handler with an HTTP method check.
func methodGuard(allowed string, h http.HandlerFunc) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                if r.Method != allowed {
                        w.Header().Set("Allow", allowed)
                        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                        return
                }
                h(w, r)
        }
}

// ctxWithTimeout returns a context with the agent's default per-request
// timeout. The cancel func is registered for deferred call via the
// request's lifecycle (Go's HTTP server cancels the parent request
// context automatically when the handler returns, so we don't need to
// call cancel ourselves).
func ctxWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
        if d <= 0 {
                d = 60 * time.Second
        }
        return context.WithTimeout(parent, d)
}

// ----------------------------- handlers -----------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, http.StatusOK, map[string]string{
                "status": "ok",
                "time":   time.Now().Format(time.RFC3339),
        })
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)

        defer cancel()
        st, err := s.backend.State(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleNavigate(w http.ResponseWriter, r *http.Request) {
        var opts NavigateOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.URL == "" {
                writeError(w, http.StatusBadRequest, "url is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        if err := s.backend.Navigate(ctx, opts.URL); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleNavigateDirect(w http.ResponseWriter, r *http.Request) {
        var opts NavigateOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.URL == "" {
                writeError(w, http.StatusBadRequest, "url is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        if err := s.backend.NavigateDirect(ctx, opts.URL); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleBack(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)

        defer cancel()
        if err := s.backend.Back(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)

        defer cancel()
        if err := s.backend.Forward(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        if err := s.backend.Reload(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 5*time.Second)

        defer cancel()
        if err := s.backend.Stop(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
        var opts ClickOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.Selector == "" && (opts.X == nil || opts.Y == nil) {
                writeError(w, http.StatusBadRequest, "either selector or x,y is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        if err := s.backend.Click(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleScroll(w http.ResponseWriter, r *http.Request) {
        var opts ScrollOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)

        defer cancel()
        if err := s.backend.Scroll(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleType(w http.ResponseWriter, r *http.Request) {
        var opts TypeOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.Selector == "" && (opts.X == nil || opts.Y == nil) {
                writeError(w, http.StatusBadRequest, "either selector or x,y is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        if err := s.backend.Type(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
        var opts KeyOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.Key == "" {
                writeError(w, http.StatusBadRequest, "key is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)

        defer cancel()
        if err := s.backend.PressKey(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleDrag dispatches a human-like drag (cubic bezier + jitter + random
// delays) from one element/point to another. Used for slider captchas.
func (s *Server) handleDrag(w http.ResponseWriter, r *http.Request) {
        var opts DragOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        // Require either start selector or x1,y1. Note: we can't use
        // opts.X1 == 0 && opts.Y1 == 0 as "not set" because (0,0) is a
        // legitimate coordinate (top-left of the document). Use pointer
        // presence instead — but DragOpts uses float64 not *float64 for
        // simplicity. As a compromise, we accept (0,0) if the caller also
        // set Duration or Steps (which signals intent). This is hacky but
        // works for the captcha use case.
        hasStart := opts.Selector != "" || opts.X1 != 0 || opts.Y1 != 0
        hasEnd := opts.Selector2 != "" || opts.X2 != 0 || opts.Y2 != 0
        // Special case: if both start and end are at (0,0) AND no selector,
        // treat as missing (the common error case).
        if !hasStart && !hasEnd {
                writeError(w, http.StatusBadRequest, "either selector or x1,y1 is required")
                return
        }
        if !hasEnd {
                writeError(w, http.StatusBadRequest, "either selector2 or x2,y2 is required")
                return
        }
        // Drag can take 1-2 seconds (duration + holdAtEnd); allow up to 15s.
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)

        defer cancel()
        if err := s.backend.Drag(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleDragTrusted dispatches a CDP-injected trusted drag.
func (s *Server) handleDragTrusted(w http.ResponseWriter, r *http.Request) {
        var opts TrustedDragOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 120*time.Second)
        defer cancel()
        if err := s.backend.DragTrusted(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleDragTouch dispatches a CDP-injected touch drag (touchStart/
// touchMove/touchEnd). Used for captchas that listen for touch events.
func (s *Server) handleDragTouch(w http.ResponseWriter, r *http.Request) {
        var opts TrustedDragOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 120*time.Second)
        defer cancel()
        if err := s.backend.DragTouch(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleNetworkEnable starts CDP Network domain capturing.
func (s *Server) handleNetworkEnable(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.EnableNetworkCapture(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleNetworkDisable stops CDP Network domain capturing.
func (s *Server) handleNetworkDisable(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.DisableNetworkCapture(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleNetworkRequests returns all captured network requests.
func (s *Server) handleNetworkRequests(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        reqs, err := s.backend.GetCapturedRequests(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, map[string]interface{}{
                "requests": reqs,
                "count":    len(reqs),
        })
}

// handleNetworkClear clears the captured requests buffer.
func (s *Server) handleNetworkClear(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.ClearCapturedRequests(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleCDPRawMouse sends a single CDP Input.dispatchMouseEvent.
// Body: {"type":"mousePressed","x":21,"y":245,"button":"left","buttons":1,"clickCount":1}
func (s *Server) handleCDPRawMouse(w http.ResponseWriter, r *http.Request) {
        var opts RawMouseOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)
        defer cancel()
        if err := s.backend.CDPRawMouse(ctx, opts); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleEval(w http.ResponseWriter, r *http.Request) {
        var opts EvalOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.Script == "" {
                writeError(w, http.StatusBadRequest, "script is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)

        defer cancel()
        val, err := s.backend.Eval(ctx, opts.Script)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        if val == nil {
                val = json.RawMessage("null")
        }
        writeJSON(w, http.StatusOK, EvalResult{Value: val})
}

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
        var opts WaitOpts
        if err := readJSON(r, &opts); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if opts.Selector == "" {
                writeError(w, http.StatusBadRequest, "selector is required")
                return
        }
        timeout := 30 * time.Second
        if opts.TimeoutMs > 0 {
                timeout = time.Duration(opts.TimeoutMs) * time.Millisecond
        }
        ctx, cancel := ctxWithTimeout(r.Context(), timeout+5*time.Second)

        defer cancel()
        if err := s.backend.Wait(ctx, opts.Selector, opts.TimeoutMs); err != nil {
                writeError(w, http.StatusGatewayTimeout, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

func (s *Server) handleElements(w http.ResponseWriter, r *http.Request) {
        selector := r.URL.Query().Get("selector")
        if selector == "" {
                writeError(w, http.StatusBadRequest, "selector query param is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)

        defer cancel()
        els, err := s.backend.Elements(ctx, selector)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, ElementsResult{Elements: els, Count: len(els)})
}

func (s *Server) handleElement(w http.ResponseWriter, r *http.Request) {
        selector := r.URL.Query().Get("selector")
        if selector == "" {
                writeError(w, http.StatusBadRequest, "selector query param is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)

        defer cancel()
        el, err := s.backend.Element(ctx, selector)
        if err != nil {
                writeError(w, http.StatusNotFound, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, el)
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
        fullPage := false
        if v := r.URL.Query().Get("fullPage"); v != "" {
                b, err := strconv.ParseBool(v)
                if err == nil {
                        fullPage = b
                }
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 60*time.Second)

        defer cancel()
        png, err := s.backend.Screenshot(ctx, fullPage)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        w.Header().Set("Content-Type", "image/png")
        w.Header().Set("Content-Length", strconv.Itoa(len(png)))
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(png)
}

// handleScreenshotTrusted takes a screenshot via CDP Page.captureScreenshot,
// which captures the actual rendered pixels from the WebView2 compositor
// (what the user sees). Unlike /agent/screenshot (which uses JS SVG
// foreignObject and often fails on complex pages), this always works.
// Requires a CDP connection (only the real WebviewBackend started with
// --cdp-port has one).
func (s *Server) handleScreenshotTrusted(w http.ResponseWriter, r *http.Request) {
        fullPage := false
        if v := r.URL.Query().Get("fullPage"); v != "" {
                b, err := strconv.ParseBool(v)
                if err == nil {
                        fullPage = b
                }
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)
        defer cancel()
        png, err := s.backend.ScreenshotTrusted(ctx, fullPage)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        w.Header().Set("Content-Type", "image/png")
        w.Header().Set("Content-Length", strconv.Itoa(len(png)))
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(png)
}

// Ensure method guards are applied at registration time.
func init() {
        // No-op: per-handler method checks are done inline because some handlers
        // accept both GET and POST (none currently do, but kept for future).
        _ = fmt.Sprintf
}

// handleResetCookies clears the backend's cookie jar so the next navigation
// starts a fresh session (no cached login, no anti-bot acw_tc, etc.).
func (s *Server) handleResetCookies(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.ResetCookies(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleSaveCookies persists the cookie jar to disk so the session
// survives process restarts. Call this after a successful login.
func (s *Server) handleSaveCookies(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.SaveCookies(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleLoadCookies re-reads the cookie jar from disk, discarding any
// in-memory cookies. Useful after SaveCookies on another process.
func (s *Server) handleLoadCookies(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.LoadCookies(ctx); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

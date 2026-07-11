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
        mux.HandleFunc("/agent/cookies", s.handleCookies)
        mux.HandleFunc("/agent/cdp-mouse", s.handleCDPRawMouse)
        mux.HandleFunc("/agent/cdp-navigate-top", s.handleCDPNavigateTop)
        mux.HandleFunc("/agent/breakthrough", s.handleBreakthrough)

        mux.HandleFunc("/agent/eval", s.handleEval)
        mux.HandleFunc("/agent/cdp-eval", s.handleCDPEval)
        mux.HandleFunc("/agent/cdp-dom-text", s.handleCDPDOMText)
        mux.HandleFunc("/agent/wait", s.handleWait)
        mux.HandleFunc("/agent/elements", s.handleElements)
        mux.HandleFunc("/agent/element", s.handleElement)
        mux.HandleFunc("/agent/screenshot", s.handleScreenshot)
        mux.HandleFunc("/agent/screenshot-trusted", s.handleScreenshotTrusted)
        mux.HandleFunc("/agent/reset-cookies", s.handleResetCookies)
        mux.HandleFunc("/agent/save-cookies", s.handleSaveCookies)
        mux.HandleFunc("/agent/load-cookies", s.handleLoadCookies)

        // Profile management (multi-account cookie profiles)
        mux.HandleFunc("/agent/profiles", s.handleProfiles)          // GET = list, POST = create
        mux.HandleFunc("/agent/profiles/switch", s.handleProfileSwitch) // POST {id}
        mux.HandleFunc("/agent/profiles/rename", s.handleProfileRename) // POST {id, name}
        mux.HandleFunc("/agent/profiles/delete", s.handleProfileDelete) // POST {id}

        // Tab worker spawning (multi-profile multi-window support)
        mux.HandleFunc("/agent/spawn-tab", s.handleSpawnTab)           // POST {profile, url}
        mux.HandleFunc("/agent/spawn-all", s.handleSpawnAll)           // POST {url} — spawn all profiles
        mux.HandleFunc("/agent/tab-workers", s.handleListTabWorkers)   // GET
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
// Supports query params for filtering:
//   - url: substring match on URL
//   - method: exact match on HTTP method (GET, POST, etc.)
//   - type: resource type (XHR, Fetch, Document, Script, etc.)
//   - status: HTTP status code (e.g. 200, 404)
//   - hasPostData: if "true", only return requests with POST data
//   - withBody: if "true", only return requests that have a response body
func (s *Server) handleNetworkRequests(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        reqs, err := s.backend.GetCapturedRequests(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }

        // Apply filters from query params
        q := r.URL.Query()
        filterURL := q.Get("url")
        filterMethod := q.Get("method")
        filterType := q.Get("type")
        filterStatus := q.Get("status")
        filterHasPostData := q.Get("hasPostData")
        filterWithBody := q.Get("withBody")

        filtered := reqs
        if filterURL != "" || filterMethod != "" || filterType != "" ||
                filterStatus != "" || filterHasPostData != "" || filterWithBody != "" {
                filtered = make([]CapturedRequest, 0, len(reqs))
                for _, req := range reqs {
                        if filterURL != "" && !strings.Contains(req.URL, filterURL) {
                                continue
                        }
                        if filterMethod != "" && req.Method != filterMethod {
                                continue
                        }
                        if filterType != "" && req.ResourceType != filterType {
                                continue
                        }
                        if filterStatus != "" {
                                code, err := strconv.Atoi(filterStatus)
                                if err == nil && req.Status != code {
                                        continue
                                }
                        }
                        if filterHasPostData == "true" && req.PostData == "" {
                                continue
                        }
                        if filterWithBody == "true" && req.ResponseBody == "" {
                                continue
                        }
                        filtered = append(filtered, req)
                }
        }

        writeJSON(w, http.StatusOK, map[string]interface{}{
                "requests": filtered,
                "count":    len(filtered),
                "total":    len(reqs),
        })
}

// handleCookies returns all cookies from the browser's cookie store.
// Supports query param "domain" to filter cookies by domain (substring match).
func (s *Server) handleCookies(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        cookies, err := s.backend.GetAllCookies(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        // Filter by domain if requested
        filterDomain := r.URL.Query().Get("domain")
        if filterDomain != "" {
                filtered := make([]BrowserCookie, 0, len(cookies))
                for _, ck := range cookies {
                        if strings.Contains(ck.Domain, filterDomain) {
                                filtered = append(filtered, ck)
                        }
                }
                writeJSON(w, http.StatusOK, map[string]interface{}{
                        "cookies": filtered,
                        "count":   len(filtered),
                })
                return
        }
        writeJSON(w, http.StatusOK, map[string]interface{}{
                "cookies": cookies,
                "count":   len(cookies),
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

// handleCDPNavigateTop makes the WebView2 top-level page navigate to
// url. POST /agent/cdp-navigate-top {"url":"https://chat.z.ai"}.
// Used by the "直接打开" UI button and by the "← Back to SamWeb"
// floating button injected on cross-origin pages.
func (s *Server) handleCDPNavigateTop(w http.ResponseWriter, r *http.Request) {
        var body struct {
                URL string `json:"url"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if body.URL == "" {
                writeError(w, http.StatusBadRequest, "missing url")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)
        defer cancel()
        if err := s.backend.CDPNavigateTop(ctx, body.URL); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleSpawnTab spawns a new tab worker process for the given profile.
// POST /agent/spawn-tab {"profile":"qq","url":"https://chat.z.ai"}
// → {"profile_id":"qq","profile_name":"qq","url":"...","agent_port":7780,"cdp_port":9223,"pid":12345}
func (s *Server) handleSpawnTab(w http.ResponseWriter, r *http.Request) {
        var body struct {
                Profile string `json:"profile"`
                URL     string `json:"url"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if body.Profile == "" {
                writeError(w, http.StatusBadRequest, "missing profile")
                return
        }
        if body.URL == "" {
                body.URL = "https://chat.z.ai"
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 30*time.Second)
        defer cancel()
        info, err := s.backend.SpawnTab(ctx, body.Profile, body.URL)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, info)
}

// handleSpawnAll spawns a tab worker for every saved profile.
// POST /agent/spawn-all {"url":"https://chat.z.ai"}
// → {"spawned":[...],"failed":[...]}
func (s *Server) handleSpawnAll(w http.ResponseWriter, r *http.Request) {
        var body struct {
                URL string `json:"url"`
        }
        if r.Method == "POST" {
                _ = readJSON(r, &body)
        }
        if body.URL == "" {
                body.URL = "https://chat.z.ai"
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 300*time.Second)
        defer cancel()
        // Get all profiles
        profs, _, err := s.backend.ListProfiles(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, "list profiles: "+err.Error())
                return
        }
        var spawned []TabWorkerInfo
        var failed []map[string]string
        for _, p := range profs {
                info, err := s.backend.SpawnTab(ctx, p.ID, body.URL)
                if err != nil {
                        failed = append(failed, map[string]string{
                                "profile": p.ID, "error": err.Error(),
                        })
                } else {
                        spawned = append(spawned, info)
                }
        }
        writeJSON(w, http.StatusOK, map[string]interface{}{
                "spawned": spawned,
                "failed":  failed,
        })
}

// handleListTabWorkers returns info about all running tab workers.
// GET /agent/tab-workers → [{"profile_id":"qq",...},...]
func (s *Server) handleListTabWorkers(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        workers, err := s.backend.ListTabWorkers(ctx)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, workers)
}

// handleCDPEval runs a JS eval via CDP Runtime.evaluate (bypasses the
// dispatch layer). Used by tab workers which don't have bootstrap JS.
// POST /agent/cdp-eval {"script":"..."} → {"value":<result>}
func (s *Server) handleCDPEval(w http.ResponseWriter, r *http.Request) {
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
        val, err := s.backend.CDPEval(ctx, opts.Script)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        if val == nil {
                val = json.RawMessage("null")
        }
        writeJSON(w, http.StatusOK, EvalResult{Value: val})
}

// handleCDPDOMText reads element HTML via CDP DOM domain (bypasses JS).
// POST /agent/cdp-dom-text {"selector":".chat-assistant:last-child"}
// → {"html":"<div class='chat-assistant'>...</div>"}
func (s *Server) handleCDPDOMText(w http.ResponseWriter, r *http.Request) {
        var body struct {
                Selector string `json:"selector"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if body.Selector == "" {
                writeError(w, http.StatusBadRequest, "selector is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)
        defer cancel()
        html, err := s.backend.CDPDOMText(ctx, body.Selector)
        if err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// handleBreakthrough automatically detects and bypasses slider captchas.
// POST /agent/breakthrough → {"challenge":"aliyun-baxia-slider","success":true}
func (s *Server) handleBreakthrough(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := ctxWithTimeout(r.Context(), 60*time.Second)
        defer cancel()
        challenge, success, err := s.backend.BreakthroughSlider(ctx)
        if err != nil {
                writeJSON(w, http.StatusOK, map[string]interface{}{
                        "challenge": challenge,
                        "success":   success,
                        "error":     err.Error(),
                })
                return
        }
        writeJSON(w, http.StatusOK, map[string]interface{}{
                "challenge": challenge,
                "success":   success,
        })
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

// ----------------------------- Profile handlers -----------------------------
//
// Profiles are named cookie snapshots that allow the user to switch
// between multiple accounts on the same site (e.g. z.ai) within a
// single samweb instance.

// handleProfiles handles:
//   GET  /agent/profiles              → list all profiles + active ID
//   POST /agent/profiles              → create/update a profile with current cookies
//                                       body: {"name": "z.ai account A"}
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
        switch r.Method {
        case http.MethodGet:
                ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
                defer cancel()
                profiles, activeID, err := s.backend.ListProfiles(ctx)
                if err != nil {
                        writeError(w, http.StatusInternalServerError, err.Error())
                        return
                }
                writeJSON(w, http.StatusOK, map[string]interface{}{
                        "profiles":         profiles,
                        "active_profile_id": activeID,
                        "count":            len(profiles),
                })

        case http.MethodPost:
                var body struct {
                        Name string `json:"name"`
                }
                if err := readJSON(r, &body); err != nil {
                        writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                        return
                }
                if body.Name == "" {
                        writeError(w, http.StatusBadRequest, "name is required")
                        return
                }
                ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)
                defer cancel()
                prof, err := s.backend.SaveCurrentCookiesToProfile(ctx, body.Name)
                if err != nil {
                        writeError(w, http.StatusInternalServerError, err.Error())
                        return
                }
                writeJSON(w, http.StatusOK, prof)

        default:
                w.Header().Set("Allow", http.MethodGet + ", " + http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        }
}

// handleProfileSwitch switches the browser to a different profile.
// The current cookies are cleared and the profile's cookies are loaded.
// Pass an empty id to clear the active profile (cookies are kept as-is).
//
// POST /agent/profiles/switch
// body: {"id": "profile-uuid"}  or {"id": ""} to clear
func (s *Server) handleProfileSwitch(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        var body struct {
                ID string `json:"id"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 15*time.Second)
        defer cancel()
        if err := s.backend.SwitchToProfile(ctx, body.ID); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleProfileRename renames a profile.
//
// POST /agent/profiles/rename
// body: {"id": "profile-uuid", "name": "new name"}
func (s *Server) handleProfileRename(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        var body struct {
                ID   string `json:"id"`
                Name string `json:"name"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if body.ID == "" || body.Name == "" {
                writeError(w, http.StatusBadRequest, "id and name are required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.RenameProfile(ctx, body.ID, body.Name); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

// handleProfileDelete deletes a profile.
//
// POST /agent/profiles/delete
// body: {"id": "profile-uuid"}
func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
                w.Header().Set("Allow", http.MethodPost)
                writeError(w, http.StatusMethodNotAllowed, "method not allowed")
                return
        }
        var body struct {
                ID string `json:"id"`
        }
        if err := readJSON(r, &body); err != nil {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
        }
        if body.ID == "" {
                writeError(w, http.StatusBadRequest, "id is required")
                return
        }
        ctx, cancel := ctxWithTimeout(r.Context(), 10*time.Second)
        defer cancel()
        if err := s.backend.DeleteProfile(ctx, body.ID); err != nil {
                writeError(w, http.StatusInternalServerError, err.Error())
                return
        }
        writeJSON(w, http.StatusOK, OK{OK: true})
}

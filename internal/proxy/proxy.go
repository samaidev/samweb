// Package proxy provides an HTTP reverse proxy that fetches arbitrary URLs
// on behalf of the browser UI and strips frame-busting headers so that pages
// can be rendered inside the Chrome-style iframe used by SamWeb.
//
// The proxy is intentionally simple: it is NOT a general-purpose anonymizing
// proxy. It only exists to make a real web browsing experience possible
// inside an embedded webview iframe, which otherwise would be blocked by
// X-Frame-Options / Content-Security-Policy frame-ancestors headers served
// by most sites.
package proxy

import (
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "log"
        "net/http"
        "net/http/cookiejar"
        "net/url"
        "os"
        "path/filepath"
        "strings"
        "sync"
        "time"

        "golang.org/x/net/publicsuffix"
)

// Server is the HTTP server that exposes the proxy endpoint on its own
// listener. It is kept for backwards compatibility; new code should call
// ServeHTTP directly from inside a parent http.ServeMux instead.
type Server struct {
        addr string
        srv  *http.Server
}

// New constructs a standalone proxy Server bound to addr.
func New(addr string) *Server {
        mux := http.NewServeMux()
        s := &Server{
                addr: addr,
                srv: &http.Server{
                        Addr:              addr,
                        Handler:           mux,
                        ReadHeaderTimeout: 15 * time.Second,
                        ReadTimeout:       60 * time.Second,
                        WriteTimeout:      120 * time.Second,
                },
        }
        mux.HandleFunc("/proxy", func(w http.ResponseWriter, r *http.Request) {
                target := r.URL.Query().Get("url")
                if target == "" {
                        http.Error(w, "missing url parameter", http.StatusBadRequest)
                        return
                }
                ServeHTTP(w, r, target)
        })
        mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK)
                _, _ = w.Write([]byte("pong"))
        })
        return s
}

// Addr returns the address the server is (or will be) listening on.
func (s *Server) Addr() string { return s.addr }

// ListenAndServe starts the proxy server. It blocks until the server stops.
func (s *Server) ListenAndServe() error {
        log.Printf("[proxy] listening on http://%s", s.addr)
        err := s.srv.ListenAndServe()
        if err != nil && !errors.Is(err, http.ErrServerClosed) {
                return err
        }
        return nil
}

// globalJar is the shared cookie jar used by all proxy requests. It persists
// cookies (including login session cookies) across requests so that sites
// which require login can maintain state.
var globalJar *cookiejar.Jar

func init() {
        j, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
        if err != nil {
                log.Printf("[proxy] cookiejar init failed: %v (continuing without jar)", err)
        } else {
                globalJar = j
        }
        // Load persisted cookies from disk so sessions survive process restarts.
        // This is what makes SamWeb "log in once, stay logged in forever".
        if err := LoadCookies(); err != nil {
                log.Printf("[proxy] load cookies: %v (continuing with empty jar)", err)
        }
}

// globalClient is the shared http.Client with the cookie jar and an optional
// upstream HTTP proxy (read from the environment). It is created lazily so
// that environment changes after import time are respected.
var globalClient *http.Client

func getClient() *http.Client {
        if globalClient != nil {
                return globalClient
        }
        transport := &http.Transport{
                MaxIdleConns:        100,
                MaxIdleConnsPerHost: 10,
                IdleConnTimeout:     90 * time.Second,
        }
        // Honor upstream proxy env vars (HTTP_PROXY / HTTPS_PROXY / NO_PROXY).
        // http.ProxyFromEnvironment reads them at request time.
        transport.Proxy = http.ProxyFromEnvironment

        c := &http.Client{
                Timeout:   60 * time.Second,
                Transport: transport,
                Jar:       globalJar,
                CheckRedirect: func(req *http.Request, via []*http.Request) error {
                        if len(via) >= 10 {
                                return errors.New("stopped after 10 redirects")
                        }
                        req.Header.Set("Host", req.URL.Host)
                        return nil
                },
        }
        globalClient = c
        return c
}

// ResetCookies clears the shared cookie jar. Useful when the agent wants to
// start a fresh session (e.g. before a new login attempt).
func ResetCookies() {
        if globalJar == nil {
                return
        }
        j, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
        if err == nil {
                globalJar = j
                globalClient = nil
        }
}

// ----------------------------- Cookie persistence -----------------------------

// cookieFile is the on-disk JSON file used to persist the cookie jar across
// process restarts. It defaults to ~/.samweb/cookies.json but can be
// overridden with SetCookieFile.
var (
        cookieFileMu sync.Mutex
        cookieFile   = defaultCookieFile()
)

func defaultCookieFile() string {
        home, err := os.UserHomeDir()
        if err != nil || home == "" {
                home = "."
        }
        return filepath.Join(home, ".samweb", "cookies.json")
}

// SetCookieFile overrides the on-disk cookie persistence file. Pass an empty
// string to disable persistence.
func SetCookieFile(path string) {
        cookieFileMu.Lock()
        defer cookieFileMu.Unlock()
        cookieFile = path
}

// CookieFile returns the current on-disk cookie persistence file.
func CookieFile() string {
        cookieFileMu.Lock()
        defer cookieFileMu.Unlock()
        return cookieFile
}

// serializedCookie is the JSON representation of an http.Cookie, augmented
// with the host-only and scheme flags that cookiejar tracks but http.Cookie
// does not expose.
type serializedCookie struct {
        Name     string `json:"name"`
        Value    string `json:"value"`
        Path     string `json:"path,omitempty"`
        Domain   string `json:"domain,omitempty"`
        Expires  string `json:"expires,omitempty"` // RFC 3339
        Secure   bool   `json:"secure,omitempty"`
        HTTPOnly bool   `json:"http_only,omitempty"`
        // HostOnly is true if the cookie is host-only (no Domain attribute was
        // set). Host-only cookies are only sent to the exact host that set them.
        HostOnly bool `json:"host_only,omitempty"`
        // Scheme is "http" or "https"; only cookies whose scheme matches are
        // sent on plain-HTTP vs HTTPS requests.
        Scheme string `json:"scheme,omitempty"`
}

// SaveCookies serializes the current cookie jar to CookieFile(). It is a
// no-op if SetCookieFile("") was called or the jar is empty. The file is
// written atomically (temp file + rename) so a crash mid-write cannot
// corrupt the cookie store.
func SaveCookies() error {
        cookieFileMu.Lock()
        path := cookieFile
        cookieFileMu.Unlock()
        if path == "" || globalJar == nil {
                return nil
        }

        // Walk every URL scheme://host that may have cookies. We do this by
        // enumerating the public suffix list's known eTLDs is infeasible;
        // instead we serialize cookies for a curated set of hosts that have
        // been seen by the jar (tracked via trackHost). For backwards
        // compatibility, also try the most common ones.
        hosts := knownHosts()
        var cookies []serializedCookie
        for _, h := range hosts {
                for _, scheme := range []string{"https", "http"} {
                        u := &url.URL{Scheme: scheme, Host: h}
                        for _, c := range globalJar.Cookies(u) {
                                cookies = append(cookies, serializedCookie{
                                        Name: c.Name, Value: c.Value, Path: c.Path,
                                        Domain: c.Domain, Secure: c.Secure, HTTPOnly: c.HttpOnly,
                                        // cookiejar does not expose HostOnly / Scheme; we
                                        // approximate by treating Domain=="host" as host-only.
                                        HostOnly: c.Domain == h || c.Domain == "",
                                        Scheme:   scheme,
                                })
                                if !c.Expires.IsZero() {
                                        cookies[len(cookies)-1].Expires = c.Expires.Format(time.RFC3339)
                                }
                        }
                }
        }

        if len(cookies) == 0 {
                return nil
        }

        if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
                return fmt.Errorf("mkdir cookie dir: %w", err)
        }
        data, err := json.MarshalIndent(cookies, "", "  ")
        if err != nil {
                return fmt.Errorf("marshal cookies: %w", err)
        }
        tmp := path + ".tmp"
        if err := os.WriteFile(tmp, data, 0o600); err != nil {
                return fmt.Errorf("write temp cookie file: %w", err)
        }
        if err := os.Rename(tmp, path); err != nil {
                return fmt.Errorf("rename cookie file: %w", err)
        }
        log.Printf("[proxy] saved %d cookies to %s", len(cookies), path)
        return nil
}

// LoadCookies reads CookieFile() and populates the cookie jar. Missing file
// is not an error. Malformed file logs a warning and returns nil.
func LoadCookies() error {
        cookieFileMu.Lock()
        path := cookieFile
        cookieFileMu.Unlock()
        if path == "" || globalJar == nil {
                return nil
        }
        data, err := os.ReadFile(path)
        if err != nil {
                if os.IsNotExist(err) {
                        return nil
                }
                return fmt.Errorf("read cookie file: %w", err)
        }
        var cookies []serializedCookie
        if err := json.Unmarshal(data, &cookies); err != nil {
                return fmt.Errorf("unmarshal cookies: %w", err)
        }
        loaded := 0
        for _, sc := range cookies {
                c := &http.Cookie{
                        Name:     sc.Name,
                        Value:    sc.Value,
                        Path:     sc.Path,
                        Domain:   sc.Domain,
                        Secure:   sc.Secure,
                        HttpOnly: sc.HTTPOnly,
                }
                if sc.Expires != "" {
                        t, err := time.Parse(time.RFC3339, sc.Expires)
                        if err == nil {
                                c.Expires = t
                        }
                }
                // Determine the URL to set the cookie under. If Domain is set,
                // use https://<domain>; if host-only, use https://<host>.
                host := sc.Domain
                if host == "" {
                        // host-only cookie: we need the original host. We don't have
                        // it stored separately, so use the first known host that
                        // matches by suffix.
                        for _, h := range knownHosts() {
                                if strings.HasSuffix(h, sc.Domain) || sc.Domain == "" {
                                        host = h
                                        break
                                }
                        }
                }
                if host == "" {
                        continue
                }
                scheme := sc.Scheme
                if scheme == "" {
                        scheme = "https"
                }
                u := &url.URL{Scheme: scheme, Host: host}
                globalJar.SetCookies(u, []*http.Cookie{c})
                loaded++
        }
        log.Printf("[proxy] loaded %d cookies from %s", loaded, path)
        return nil
}

// knownHosts returns the set of hosts whose cookies should be persisted.
// It is seeded from the hosts seen by trackHost and is updated on every
// proxy request.
var (
        knownHostsMu sync.RWMutex
        knownHostsSet = map[string]struct{}{}
)

// trackHost records that we have seen cookies for this host so that
// SaveCookies can enumerate it. Called from ServeHTTP on every request.
func trackHost(host string) {
        if host == "" {
                return
        }
        knownHostsMu.Lock()
        knownHostsSet[host] = struct{}{}
        knownHostsMu.Unlock()
}

func knownHosts() []string {
        knownHostsMu.RLock()
        defer knownHostsMu.RUnlock()
        out := make([]string, 0, len(knownHostsSet))
        for h := range knownHostsSet {
                out = append(out, h)
        }
        return out
}

// ServeHTTP fetches target and streams the response to w, stripping
// frame-busting headers. It is exported so the browser package can mount
// the proxy on the same http.ServeMux as the UI (so the iframe is
// same-origin with the parent page, which the agent JS code requires).
func ServeHTTP(w http.ResponseWriter, r *http.Request, target string) {
        u, err := url.Parse(target)
        if err != nil {
                http.Error(w, "invalid url: "+err.Error(), http.StatusBadRequest)
                return
        }
        if u.Scheme != "http" && u.Scheme != "https" {
                http.Error(w, "only http/https are supported", http.StatusBadRequest)
                return
        }
        // Track this host so SaveCookies can enumerate it when persisting
        // the cookie jar.
        trackHost(u.Host)

        outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
        if err != nil {
                http.Error(w, "build request: "+err.Error(), http.StatusBadGateway)
                return
        }
        copyForwardableHeaders(outReq.Header, r.Header, u.Host)

        // Merge cookies from the iframe request (if any) with the jar. The
        // iframe sends its own cookies (scoped to 127.0.0.1); we only care
        // about the upstream cookies stored in the jar, but we forward any
        // Cookie header the browser sent so that test harnesses can inject
        // cookies explicitly. The jar's cookies take precedence.
        if cookieHeader := outReq.Header.Get("Cookie"); cookieHeader != "" {
                // keep it; jar will also add cookies via Client.Do
        }

        client := getClient()
        resp, err := client.Do(outReq)
        if err != nil {
                http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
                return
        }
        defer resp.Body.Close()

        for key, vals := range resp.Header {
                lk := strings.ToLower(key)
                switch lk {
                case "x-frame-options",
                        "content-security-policy",
                        "content-security-policy-report-only",
                        "strict-transport-security",
                        "cross-origin-opener-policy",
                        "cross-origin-embedder-policy",
                        "cross-origin-resource-policy",
                        "permissions-policy":
                        // Strip frame-busting / sandbox headers so the iframe can render.
                        continue
                case "set-cookie":
                        // The jar has already absorbed these cookies. We also re-emit
                        // them to the iframe so that any in-page JS reading
                        // document.cookie sees them, but we strip the Domain attribute
                        // (the cookie will be scoped to the proxy origin 127.0.0.1).
                        // This is a best-effort bridge between the jar and the iframe.
                        for _, v := range vals {
                                stripped := stripCookieDomain(v)
                                w.Header().Add(key, stripped)
                        }
                        continue
                case "location":
                        // Redirects: rewrite absolute Location URLs that point back to
                        // the upstream site into proxy-relative URLs so the iframe
                        // follows them through the proxy instead of escaping.
                        for _, v := range vals {
                                w.Header().Add(key, rewriteLocation(v, u))
                        }
                        continue
                }
                for _, v := range vals {
                        w.Header().Add(key, v)
                }
        }

        w.Header().Set("X-Samweb-Proxied", "1")
        w.WriteHeader(resp.StatusCode)
        _, _ = io.Copy(w, resp.Body)
}

// stripCookieDomain removes the Domain= attribute from a Set-Cookie value
// so the browser scopes the cookie to the proxy origin. Without this, the
// browser would reject Set-Cookie with Domain=modelscope.cn coming from
// 127.0.0.1 (cross-domain cookie rejection).
func stripCookieDomain(setCookie string) string {
        parts := strings.Split(setCookie, ";")
        out := make([]string, 0, len(parts))
        for _, p := range parts {
                t := strings.TrimSpace(p)
                if strings.HasPrefix(strings.ToLower(t), "domain=") {
                        continue
                }
                out = append(out, p)
        }
        return strings.Join(out, ";")
}

// rewriteLocation converts an absolute redirect Location (e.g.
// "https://www.modelscope.cn/login") into a proxy URL
// ("/proxy?url=https://www.modelscope.cn/login") so the iframe stays
// same-origin with the proxy. Relative Locations are returned as-is.
func rewriteLocation(loc string, base *url.URL) string {
        if loc == "" {
                return loc
        }
        parsed, err := url.Parse(loc)
        if err != nil {
                return loc
        }
        if parsed.IsAbs() {
                // Absolute: route through the proxy.
                return "/proxy?url=" + url.QueryEscape(loc)
        }
        // Relative: resolve against base and route through proxy.
        resolved := base.ResolveReference(parsed)
        return "/proxy?url=" + url.QueryEscape(resolved.String())
}

// copyForwardableHeaders copies a curated subset of request headers.
func copyForwardableHeaders(dst, src http.Header, host string) {
        dst.Set("Host", host)
        dst.Set("User-Agent", src.Get("User-Agent"))
        if ua := dst.Get("User-Agent"); ua == "" || strings.Contains(ua, "SamWeb") {
                // Realistic Chrome UA so anti-bot systems (baxia) don't flag the
                // custom SamWeb UA immediately.
                dst.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
                        "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
        }
        for _, h := range []string{
                "Accept", "Accept-Language", "Accept-Encoding",
                "Cookie", "Referer", "Origin",
                // Body headers: without these, upstream APIs reject POSTs as
                // "unsupported media type" or "missing content-length". The Go
                // http.Request.Write will recompute Content-Length from the body
                // when the body is a known-size reader, so we only forward the
                // caller-provided Content-Type here; Content-Length is left to
                // the transport.
                "Content-Type",
                // X-Requested-With is set by jQuery / axios on XHRs; many
                // Aliyun endpoints reject requests without it as "not an XHR".
                "X-Requested-With",
                // CSRF tokens commonly used by Aliyun passport endpoints.
                "X-XSRF-TOKEN", "X-CSRF-Token",
        } {
                if v := src.Get(h); v != "" {
                        // Rewrite Referer/Origin that point at the proxy to point at the
                        // upstream host so the site sees expected same-origin headers.
                        if h == "Referer" || h == "Origin" {
                                v = rewriteRefererOrigin(v, host)
                        }
                        dst.Set(h, v)
                }
        }
        // Note: do NOT forward Accept-Encoding blindly. http.Transport
        // auto-adds gzip support and transparently decompresses; if we
        // forward the client's Accept-Encoding: gzip the Transport won't
        // decompress and the iframe would get raw gzip bytes. We strip it
        // here unless the caller explicitly set it (handled above), in which
        // case the transport will not auto-decompress. The latter matters
        // for sites that misbehave on missing Accept-Encoding.
        if dst.Get("Accept-Encoding") == "" {
                dst.Del("Accept-Encoding")
        }
}

// rewriteRefererOrigin rewrites a Referer/Origin header that points at the
// proxy (http://127.0.0.1:<port>/proxy?url=...) into the upstream URL so the
// site sees the same-origin value it expects.
func rewriteRefererOrigin(v, host string) string {
        if strings.Contains(v, "/proxy?url=") {
                if idx := strings.Index(v, "url="); idx >= 0 {
                        enc := v[idx+4:]
                        if dec, err := url.QueryUnescape(enc); err == nil {
                                return dec
                        }
                }
        }
        if strings.HasPrefix(v, "http://127.0.0.1") || strings.HasPrefix(v, "http://localhost") {
                return "https://" + host + "/"
        }
        return v
}

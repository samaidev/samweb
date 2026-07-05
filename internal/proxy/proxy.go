// Package proxy provides an HTTP reverse proxy that fetches arbitrary URLs
// on behalf of the browser UI and strips frame-busting headers so that pages
// can be rendered inside the Chrome-style iframe used by SamWeb.
//
// The proxy is intentionally simple: it is NOT a general-purpose anonymizing
// proxy. It only exists to make a real web browsing experience possible
// inside an embedded webview iframe, which otherwise would be blocked by
// X-Frame-Options / Content-Security-Policy headers served by most sites.
package proxy

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server is the HTTP server that exposes the proxy endpoint.
type Server struct {
	addr string
	srv  *http.Server
}

// New constructs a proxy Server bound to the given address (e.g. "127.0.0.1:8732").
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
	mux.HandleFunc("/proxy", s.handleProxy)
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

// handleProxy is the main proxy endpoint. It expects a ?url= query parameter
// pointing at the absolute URL the browser wants to load. It fetches that URL
// server-side, strips frame-busting headers, rewrites a few header values,
// and streams the response back to the iframe.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, "invalid url: "+err.Error(), http.StatusBadRequest)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		http.Error(w, "only http/https are supported", http.StatusBadRequest)
		return
	}

	// Build the outgoing request, forwarding a curated set of headers.
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "build request: "+err.Error(), http.StatusBadGateway)
		return
	}
	copyForwardableHeaders(outReq.Header, r.Header, u.Host)

	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow up to 10 redirects; rewrite the Host header on each hop.
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			req.Header.Set("Host", req.URL.Host)
			return nil
		},
	}
	resp, err := client.Do(outReq)
	if err != nil {
		http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy headers but drop the ones that would prevent embedding in an iframe.
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
			"permissions-policy",
			"set-cookie":
			// Drop. These would either block embedding or leak cross-origin state.
			continue
		}
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	// Hint to the iframe that this is a top-level navigation context.
	w.Header().Set("X-Samweb-Proxied", "1")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyForwardableHeaders copies a curated subset of request headers from the
// inbound request to the outbound one. We deliberately do not forward cookies
// or authorization headers because the proxy is shared across all tabs.
func copyForwardableHeaders(dst, src http.Header, host string) {
	dst.Set("Host", host)
	dst.Set("User-Agent", src.Get("User-Agent"))
	if ua := dst.Get("User-Agent"); ua == "" {
		dst.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 "+
			"(KHTML, like Gecko) SamWeb/0.1 Chrome/124.0 Safari/605.1.15")
	}
	for _, h := range []string{"Accept", "Accept-Language", "Accept-Encoding"} {
		if v := src.Get(h); v != "" {
			dst.Set(h, v)
		}
	}
}

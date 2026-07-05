// Package search resolves free-text queries typed into the Chrome-style
// omnibox into concrete URLs that the browser can navigate to.
//
// The omnibox behaves like Chrome's: if the input looks like a URL it is
// opened directly; otherwise it is treated as a search query and dispatched
// to the configured search engine via its public search URL template.
package search

import (
	"net/url"
	"strings"
)

// Engine describes a search provider that can turn a query into a URL.
type Engine struct {
	Name     string
	Template string // must contain exactly one "%s" which is replaced with url-encoded query
}

// Default engines supported out of the box. The first one is used when the
// user does not explicitly pick a different provider.
var (
	Google     = Engine{Name: "Google", Template: "https://www.google.com/search?q=%s"}
	Bing       = Engine{Name: "Bing", Template: "https://www.bing.com/search?q=%s"}
	DuckDuckGo = Engine{Name: "DuckDuckGo", Template: "https://duckduckgo.com/?q=%s"}
	Baidu      = Engine{Name: "Baidu", Template: "https://www.baidu.com/s?wd=%s"}
)

// DefaultEngine is the engine used when the user does not pick one explicitly.
var DefaultEngine = Google

// Engines returns the list of engines the UI offers in its dropdown.
func Engines() []Engine {
	return []Engine{Google, Bing, DuckDuckGo, Baidu}
}

// Resolve converts a raw omnibox input string into a target URL.
//
// Rules (mirroring Chrome's omnibox behaviour):
//   - Empty input -> about:blank
//   - "about:" URLs are passed through (used internally by the UI)
//   - Strings that already look like URLs (scheme://, host.path, localhost) are
//     normalized to a full URL and returned.
//   - Everything else is treated as a search query and dispatched to the
//     supplied engine (or DefaultEngine if engine is the zero value).
func Resolve(input string, engine Engine) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return "about:blank"
	}
	if strings.HasPrefix(input, "about:") {
		return input
	}
	if isLikelyURL(input) {
		return normalizeURL(input)
	}
	if engine.Name == "" || engine.Template == "" {
		engine = DefaultEngine
	}
	return strings.Replace(engine.Template, "%s", url.QueryEscape(input), 1)
}

// isLikelyURL returns true when the input should be treated as a URL rather
// than as a search query. The heuristics intentionally err on the side of
// "looks like a URL" so that things like "example.com" or "localhost:8080"
// are opened directly instead of being searched.
func isLikelyURL(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	if strings.HasPrefix(s, "about:") {
		return true
	}
	if strings.HasPrefix(s, "localhost") {
		return true
	}
	// "example.com", "example.com:8080", "example.com/path", "127.0.0.1"
	if hasDomainLikeFirstSegment(s) {
		return true
	}
	// If the first token contains a dot and no spaces, treat as a hostname.
	first := strings.Fields(s)
	if len(first) == 1 && strings.Contains(first[0], ".") && !strings.ContainsAny(first[0], " ") {
		return true
	}
	return false
}

// hasDomainLikeFirstSegment returns true if the first path/segment of s up to
// the first '/' or ':' looks like a hostname (e.g. "example.com",
// "127.0.0.1", "sub.domain.co.uk").
func hasDomainLikeFirstSegment(s string) bool {
	end := len(s)
	for i, r := range s {
		if r == '/' || r == ':' || r == ' ' {
			end = i
			break
		}
	}
	host := s[:end]
	if host == "" {
		return false
	}
	// must contain at least one dot, or be all-numeric (IPv4)
	if strings.Contains(host, ".") {
		return true
	}
	for _, r := range host {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalizeURL turns a bare host/path string into a full https:// URL.
func normalizeURL(s string) string {
	if strings.Contains(s, "://") {
		return s
	}
	if strings.HasPrefix(s, "about:") {
		return s
	}
	// Bare hostnames get https:// prepended (modern web defaults to HTTPS).
	return "https://" + s
}

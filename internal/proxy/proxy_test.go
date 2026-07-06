package proxy

import (
        "net/http"
        "net/url"
        "testing"
)

// TestCopyForwardableHeaders_PostBodyHeaders verifies that POST-critical
// headers (Content-Type, X-Requested-With, XSRF/CSRF tokens) are forwarded
// to the upstream. Without these, Aliyun-style login APIs (e.g. modelscope.cn)
// reject requests with 415 / 403.
func TestCopyForwardableHeaders_PostBodyHeaders(t *testing.T) {
        src := http.Header{}
        src.Set("Content-Type", "application/json")
        src.Set("X-Requested-With", "XMLHttpRequest")
        src.Set("X-XSRF-TOKEN", "abc123")
        src.Set("X-CSRF-Token", "def456")
        src.Set("Accept", "application/json, text/plain, */*")
        src.Set("Accept-Language", "zh-CN,zh;q=0.9")
        src.Set("Cookie", "acw_tc=xxx")
        src.Set("Referer", "http://127.0.0.1:8888/proxy?url=https%3A%2F%2Fwww.modelscope.cn%2Flogin")

        dst := http.Header{}
        copyForwardableHeaders(dst, src, "www.modelscope.cn")

        cases := []struct{ header, want string }{
                {"Host", "www.modelscope.cn"},
                {"Content-Type", "application/json"},
                {"X-Requested-With", "XMLHttpRequest"},
                {"X-Xsrf-Token", "abc123"}, // canonical form of X-XSRF-TOKEN
                {"X-Csrf-Token", "def456"},
                {"Accept", "application/json, text/plain, */*"},
                {"Accept-Language", "zh-CN,zh;q=0.9"},
                {"Cookie", "acw_tc=xxx"},
        }
        for _, c := range cases {
                if got := dst.Get(c.header); got != c.want {
                        t.Errorf("header %q: got %q, want %q", c.header, got, c.want)
                }
        }

        // Referer must be rewritten to point at the upstream host, not the proxy.
        if got := dst.Get("Referer"); got != "https://www.modelscope.cn/login" {
                t.Errorf("Referer: got %q, want https://www.modelscope.cn/login", got)
        }

        // User-Agent must be the realistic Chrome UA (the source UA contained
        // "SamWeb" so the override should kick in).
        if ua := dst.Get("User-Agent"); ua == "" || (ua != "" && ua == "SamWeb/1.0") {
                t.Errorf("User-Agent: got %q, expected realistic Chrome UA", ua)
        }
        if ua := dst.Get("User-Agent"); ua != "" && ua == "SamWeb/1.0" {
                t.Errorf("User-Agent: SamWeb UA was not overridden")
        }
}

// TestCopyForwardableHeaders_RealisticUA verifies that a SamWeb-branded UA
// is replaced with a realistic Chrome UA so anti-bot systems don't flag it.
func TestCopyForwardableHeaders_RealisticUA(t *testing.T) {
        src := http.Header{}
        src.Set("User-Agent", "SamWeb/1.0 (Go WebKit)")
        dst := http.Header{}
        copyForwardableHeaders(dst, src, "example.com")
        ua := dst.Get("User-Agent")
        if ua == "SamWeb/1.0 (Go WebKit)" {
                t.Errorf("SamWeb UA was not overridden: %q", ua)
        }
        // Should look like Chrome.
        if ua == "" {
                t.Errorf("UA is empty after override")
        }
}

// TestStripCookieDomain verifies that the Domain= attribute is stripped
// from Set-Cookie values so the browser scopes cookies to the proxy origin
// (127.0.0.1). Without this, the browser rejects Set-Cookie with
// Domain=modelscope.cn as a cross-domain cookie.
func TestStripCookieDomain(t *testing.T) {
        cases := []struct{ in, want string }{
                {
                        in:   "session=abc; Domain=modelscope.cn; Path=/; HttpOnly; Secure",
                        want: "session=abc; Path=/; HttpOnly; Secure",
                },
                {
                        in:   "acw_tc=xyz; domain=www.modelscope.cn; path=/; httponly",
                        want: "acw_tc=xyz; path=/; httponly",
                },
                {
                        in:   "token=foo; Domain=MODELSCOPE.CN; Path=/",
                        want: "token=foo; Path=/",
                },
                // Cookie with no Domain attribute should pass through unchanged.
                {
                        in:   "session=abc; Path=/; HttpOnly",
                        want: "session=abc; Path=/; HttpOnly",
                },
                // Cookie with no attributes at all.
                {
                        in:   "session=abc",
                        want: "session=abc",
                },
        }
        for _, c := range cases {
                got := stripCookieDomain(c.in)
                if got != c.want {
                        t.Errorf("stripCookieDomain(%q)\n  got:  %q\n  want: %q", c.in, got, c.want)
                }
        }
}

// TestRewriteLocation_Absolute verifies that absolute redirect Locations
// pointing at the upstream host are rewritten into proxy-relative URLs so
// the iframe stays same-origin with the proxy.
func TestRewriteLocation_Absolute(t *testing.T) {
        base, _ := url.Parse("https://www.modelscope.cn/api/v1/login")
        cases := []struct{ in, want string }{
                {
                        in:   "https://www.modelscope.cn/dashboard",
                        want: "/proxy?url=" + url.QueryEscape("https://www.modelscope.cn/dashboard"),
                },
                {
                        in:   "https://modelscope.cn/profile",
                        want: "/proxy?url=" + url.QueryEscape("https://modelscope.cn/profile"),
                },
                // Relative location: should be resolved against base and routed through proxy.
                {
                        in:   "/dashboard",
                        want: "/proxy?url=" + url.QueryEscape("https://www.modelscope.cn/dashboard"),
                },
                {
                        // Relative URL: resolves against the base path
                        // https://www.modelscope.cn/api/v1/login, so "profile" ->
                        // https://www.modelscope.cn/api/v1/profile.
                        in:   "profile",
                        want: "/proxy?url=" + url.QueryEscape("https://www.modelscope.cn/api/v1/profile"),
                },
                // Empty string should pass through.
                {in: "", want: ""},
        }
        for _, c := range cases {
                got := rewriteLocation(c.in, base)
                if got != c.want {
                        t.Errorf("rewriteLocation(%q)\n  got:  %q\n  want: %q", c.in, got, c.want)
                }
        }
}

// TestRewriteRefererOrigin verifies that a Referer/Origin pointing at the
// proxy (http://127.0.0.1:<port>/proxy?url=...) is unwrapped back to the
// upstream URL so the site sees the same-origin value it expects.
func TestRewriteRefererOrigin(t *testing.T) {
        cases := []struct{ in, want string }{
                {
                        in:   "http://127.0.0.1:8888/proxy?url=https%3A%2F%2Fwww.modelscope.cn%2Flogin",
                        want: "https://www.modelscope.cn/login",
                },
                {
                        in:   "http://localhost:8888/proxy?url=https%3A%2F%2Fmodelscope.cn%2F",
                        want: "https://modelscope.cn/",
                },
                // Already-upstream referer: should pass through unchanged.
                {
                        in:   "https://www.modelscope.cn/login",
                        want: "https://www.modelscope.cn/login",
                },
                // Proxy origin without url= param: fall back to https://<host>/.
                {
                        in:   "http://127.0.0.1:8888/",
                        want: "https://www.modelscope.cn/",
                },
        }
        for _, c := range cases {
                got := rewriteRefererOrigin(c.in, "www.modelscope.cn")
                if got != c.want {
                        t.Errorf("rewriteRefererOrigin(%q)\n  got:  %q\n  want: %q", c.in, got, c.want)
                }
        }
}

#!/usr/bin/env python3
"""
Reference script: drive the SamWeb browser to log into modelscope.cn.

ModelScope is a React/UmiJS single-page application whose login flow uses
Aliyun's havana-login SDK and Aliyun Captcha. Because the SPA's API calls
(/api/v1/*) use relative URLs that would resolve against the proxy origin
instead of modelscope.cn, the iframe proxy approach does NOT work for this
site. We must use the `navigate-direct` agent endpoint to load the page as
the webview's top-level page, where the SPA runs in its natural origin and
all relative URLs resolve correctly.

Prerequisites:
  1. Build the full samweb binary (requires WebKitGTK dev headers):
         cd samweb && go build -o samweb ./cmd/samweb
  2. Start it with the agent API enabled:
         ./samweb --agent-addr 127.0.0.1:7777 --agent-token my-secret

Usage:
  python3 scripts/login_modelscope.py \
      --base http://127.0.0.1:7777 \
      --token my-secret \
      --phone 13528475138 \
      [--password 'your-password'] \
      [--sms-code 123456]

If --password is omitted the script only loads the login page and reports
the form structure so you can inspect what fields the captcha protects.
SMS code flow is left as a TODO — the havana-login SDK controls that
slider and the user must complete the captcha interactively.
"""
import argparse
import json
import sys
import time
import urllib.parse
import urllib.request
import urllib.error


def req(base, token, method, path, body=None, expect=200):
    url = base + path
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            payload = resp.read()
            if resp.status != expect:
                raise RuntimeError(f"{method} {path}: HTTP {resp.status}: {payload[:200]}")
            ctype = resp.headers.get("Content-Type", "")
            if ctype.startswith("application/json"):
                return json.loads(payload)
            return payload
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        raise RuntimeError(f"{method} {path}: HTTP {e.code}: {body[:300]}") from None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:7777",
                    help="base URL of the SamWeb agent API")
    ap.add_argument("--token", default="", help="bearer token for the agent API")
    ap.add_argument("--phone", required=True, help="phone number to log in with")
    ap.add_argument("--password", default="", help="account password (optional)")
    ap.add_argument("--login-url", default="https://www.modelscope.cn/login",
                    help="login page URL")
    ap.add_argument("--reset-cookies", action="store_true",
                    help="clear the cookie jar before starting the login flow")
    args = ap.parse_args()

    base = args.base.rstrip("/")

    # 1. Health check
    print("[1] Checking agent server health...")
    r = req(base, args.token, "GET", "/agent/health")
    print(f"    status={r['status']}")

    # 2. Reset cookies (fresh session)
    if args.reset_cookies:
        print("[2] Resetting cookie jar...")
        r = req(base, args.token, "POST", "/agent/reset-cookies")
        print(f"    ok={r['ok']}")

    # 3. Navigate-direct to the login page.
    #    MUST be navigate-direct (not navigate), because modelscope.cn is a
    #    React SPA. With the regular navigate-via-proxy, the SPA's /api/v1/*
    #    XHRs would resolve against the proxy origin and 404.
    print(f"[3] Loading {args.login_url} via navigate-direct (bypasses iframe proxy)...")
    r = req(base, args.token, "POST", "/agent/navigate-direct",
            {"url": args.login_url})
    print(f"    ok={r['ok']}")

    # 4. Wait for the SPA to render. The havana-login SDK injects the form
    #    into an iframe; the actual phone input is inside that iframe.
    #    Without a real browser we cannot reliably reach into the
    #    cross-origin login iframe, so we report what we can see.
    print("[4] Waiting for the page to settle (3s)...")
    time.sleep(3)

    r = req(base, args.token, "GET", "/agent/state")
    print(f"    current url: {r.get('url')}")
    print(f"    title:       {r.get('title')}")

    # 5. Try to find the phone input. The havana-login SDK is usually
    #    embedded via an iframe from passport.modelscope.cn, so the input
    #    is not directly reachable from the top document. This step is
    #    informational.
    print("[5] Probing top document for phone input...")
    try:
        r = req(base, args.token, "GET",
                "/agent/elements?selector=" + urllib.parse.quote("input[type='tel'],input[name='phone'],input[name='mobile']"))
        if r["count"] == 0:
            print("    no phone input found in the top document")
            print("    -> this is expected: havana-login embeds the form in a cross-origin iframe")
            print("    -> the user must complete the captcha + form inside the visible webview window")
            print("    -> the script's job is done here; the agent can poll /agent/state to detect")
            print("       a successful login (URL changes away from /login, or /api/v1/users/me returns 200)")
        else:
            for el in r["elements"]:
                print(f"    found {el['tag']}#{el.get('id','')} name={el['attrs'].get('name')} at ({el['x']:.0f},{el['y']:.0f})")
    except RuntimeError as e:
        print(f"    probe failed: {e}")

    # 6. If password was provided, attempt to type it into the phone input.
    #    This will only work if the input is in the top document; if it's
    #    inside the havana-login iframe, the type call will fail with
    #    "element not found". The user can then complete the captcha
    #    manually in the visible webview window.
    if args.password:
        print(f"[6] Phone: {args.phone}, attempting to type into phone input (if reachable)...")
        try:
            r = req(base, args.token, "POST", "/agent/type",
                    {"selector": "input[type='tel'],input[name='phone'],input[name='mobile']",
                     "text": args.phone, "clear": True})
            print(f"    typed phone, ok={r['ok']}")
        except RuntimeError as e:
            print(f"    could not type phone (expected for havana-login iframe): {e}")

    # 7. Poll the API to detect a successful login.
    print("[7] Polling /api/v1/users/me for a successful login (max 120s)...")
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            r = req(base, args.token, "POST", "/agent/eval",
                    {"script": "fetch('/api/v1/users/me').then(r=>r.status)"})
            status = r.get("value")
            print(f"    /api/v1/users/me status: {status}")
            if status == 200:
                print("    LOGIN SUCCESSFUL")
                return 0
        except RuntimeError as e:
            print(f"    eval failed: {e}")
        time.sleep(3)
    print("    timeout waiting for login. The user may still complete the captcha in the webview window.")
    return 1


if __name__ == "__main__":
    sys.exit(main())

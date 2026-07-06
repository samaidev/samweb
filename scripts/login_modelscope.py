#!/usr/bin/env python3
"""
End-to-end modelscope.cn login automation for SamWeb.

Workflow:
  1. Try to load persisted cookies. If they're still valid (the user is
     already logged in), skip login entirely. ← This is the "zero human
     participation" mode for every run after the first.
  2. Otherwise, navigate-direct to the passport.modelscope.cn login URL
     so the form runs as the top document (no cross-origin iframe).
  3. Auto-fill the phone/account field, password field, and check the
     agreement checkbox.
  4. Click the login button to trigger the Aliyun baxia slider.
  5. Attempt to auto-drag the slider (see "Baxia slider note" below).
     If auto-drag fails (it usually does, due to isTrusted), wait for
     the user to complete the slider manually in the visible webview
     window.
  6. Poll /api/v1/users/me until it returns 200 (login succeeded).
  7. Save the cookies so the next launch skips all of this.

After the first successful login, every subsequent run of this script
goes straight to step 1 and exits — true "zero human participation"
operation.

=== Baxia slider note ===

Aliyun baxia's NoCaptcha slider verifies `event.isTrusted` on every
pointer event. JS-created events via `dispatchEvent` always have
`isTrusted=false`, so baxia ignores them. This is a browser security
hard limit — the only way to inject trusted events is at the browser
engine level (CDP Input.dispatchMouseEvent or native WebView2 calls),
which webview_go does not expose.

SamWeb's drag API still dispatches the events (in case baxia's
detection weakens in the future, or for other captcha systems that
don't check isTrusted), and the human-like trajectory (cubic bezier +
jitter + smoothstep easing + random pauses) is correct. But for baxia
specifically, the first login requires the user to drag the slider
manually. After that, cookie persistence makes every subsequent run
fully automatic.

Usage:
  python3 scripts/login_modelscope.py \\
      --base http://127.0.0.1:7777 \\
      --token my-secret \\
      --phone 13528475138 \\
      --password 'your-password' \\
      [--login-wait 300]

Requirements:
  - samweb must be running with the agent API enabled.
  - On Windows, samweb must be in the interactive session (use schtasks
    /IT or PsExec -i <session>).
"""
import argparse
import json
import sys
import time
import urllib.parse
import urllib.request
import urllib.error


# The passport login URL that modelscope.cn embeds in its iframe. By
# loading it as the TOP document (via navigate-direct), the form's inputs
# are directly reachable by the agent JS — no cross-origin iframe
# boundary.
PASSPORT_URL = (
    "https://passport.modelscope.cn/mini_login.htm"
    "?lang=zh_cn&appName=modelscope&appEntrance=pc_new"
    "&styleType=vertical&bizParams=&notLoadSsoView=true"
    "&notKeepLogin=false&isMobile=false"
    "&cssUrl=//g.alicdn.com/sail-web/maas/2.13.104/loginIframe/login.css"
    "&returnUrl=https://www.modelscope.cn/"
)

# modelscope's "current user" API. Returns 200 only when authenticated.
WHOAMI_URL = "https://www.modelscope.cn/api/v1/users/me"


def req(base, token, method, path, body=None, expect=200, timeout=60):
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
        with urllib.request.urlopen(r, timeout=timeout) as resp:
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


def is_logged_in(base, token):
    """Return True if the current cookie jar gives us 200 on /api/v1/users/me.

    This works because samweb's proxy cookie jar is shared between the
    webview and the agent — if the user logged in via the webview, the
    session cookies are in the jar, and a navigate-direct to the
    whoami URL will return 200 (the webview will render the JSON
    response as text, which we can read via eval).
    """
    print("[check] testing if already logged in...")
    try:
        # Navigate to the whoami URL. The response is JSON; the webview
        # will render it as text.
        req(base, token, "POST", "/agent/navigate-direct",
            body={"url": WHOAMI_URL}, timeout=30)
        time.sleep(3)
        # Read document.body.innerText
        s, body = _eval(base, token, "document.body ? document.body.innerText.slice(0, 500) : ''")
        if '"Success":true' in body or '"Success": true' in body:
            print(f"[check] already logged in: {body[:200]}")
            return True
        print(f"[check] not logged in: {body[:200]}")
        return False
    except Exception as e:
        print(f"[check] error: {e}")
        return False


def _eval(base, token, script, timeout=30):
    """Helper: run an eval and return (status, body_str)."""
    r = req(base, token, "POST", "/agent/eval",
            body={"script": script}, timeout=timeout)
    if isinstance(r, dict) and "value" in r:
        v = r["value"]
        # eval returns {value: <result>} where <result> may itself be a
        # JSON-encoded value
        if isinstance(v, dict) and "value" in v:
            v = v["value"]
        return 200, str(v)
    return 200, str(r)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:7777")
    ap.add_argument("--token", default="")
    ap.add_argument("--phone", required=True,
                    help="phone number / account name to log in with")
    ap.add_argument("--password", default="",
                    help="account password. Required for auto-slider — "
                         "the baxia slider only appears after the form is "
                         "filled AND the login button is clicked. Without "
                         "a password the button stays disabled.")
    ap.add_argument("--login-wait", type=int, default=300,
                    help="seconds to wait for login to complete")
    ap.add_argument("--no-save", action="store_true",
                    help="don't save cookies after successful login")
    args = ap.parse_args()

    if not args.password:
        print("ERROR: --password is required for auto-slider mode.")
        print("       The Aliyun baxia slider only appears after the form")
        print("       is fully filled and the login button is clicked.")
        print("       Without a password the button stays disabled and no")
        print("       slider is shown.")
        return 1

    base = args.base.rstrip("/")

    # 1. Health
    print("[1] Checking agent server health...")
    r = req(base, args.token, "GET", "/agent/health")
    print(f"    status={r['status']}")

    # 2. Try to load persisted cookies (proxy's init() already does this
    #    on process start, but we call it again in case the cookie file
    #    was updated since the process started).
    print("[2] Loading persisted cookies...")
    try:
        req(base, args.token, "POST", "/agent/load-cookies")
        print("    cookies loaded")
    except Exception as e:
        print(f"    load-cookies failed: {e}")

    # 3. Check if already logged in
    if is_logged_in(base, args.token):
        print("\n>>> Already logged in. Nothing to do.")
        return 0

    # 4. Not logged in -> start login flow
    print(f"\n[3] Navigating to passport login URL (top document)...")
    print(f"    URL: {PASSPORT_URL[:100]}...")
    req(base, args.token, "POST", "/agent/navigate-direct",
        body={"url": PASSPORT_URL}, timeout=30)
    print("    waiting 12s for SPA to render...")
    time.sleep(12)

    # 5. Auto-fill the phone field
    print(f"\n[4] Filling account field with phone {args.phone}...")
    # The passport form uses #fm-login-id for the account input.
    sel = "#fm-login-id"
    try:
        r = req(base, args.token, "POST", "/agent/type",
                body={"selector": sel, "text": args.phone, "clear": True}, timeout=15)
        print(f"    typed phone, ok={r.get('ok')}")
    except Exception as e:
        print(f"    type failed: {e}")
        print("    (the form may not have rendered yet; you can type it manually)")

    # 6. Auto-fill the password
    print(f"\n[5] Filling password field...")
    try:
        r = req(base, args.token, "POST", "/agent/type",
                body={"selector": "#fm-login-password", "text": args.password, "clear": True},
                timeout=15)
        print(f"    typed password, ok={r.get('ok')}")
    except Exception as e:
        print(f"    type failed: {e}")

    # Check the agreement checkbox
    print("\n[6] Checking agreement checkbox...")
    try:
        r = req(base, args.token, "POST", "/agent/click",
                body={"selector": "#fm-agreement-checkbox"}, timeout=10)
        print(f"    clicked, ok={r.get('ok')}")
    except Exception as e:
        print(f"    click failed (may already be checked): {e}")

    # 7. Click the login button FIRST — on modelscope's passport page,
    #    the Aliyun NoCaptcha slider is hidden by default and only
    #    appears after the user clicks "登录". The slider actually
    #    appears inside a same-origin iframe #baxia-dialog-content (also
    #    on passport.modelscope.cn), so we CAN reach into it.
    print(f"\n[7] Clicking login button to trigger slider...")
    try:
        req(base, args.token, "POST", "/agent/click",
            body={"selector": ".fm-button.fm-submit"}, timeout=10)
        print("    clicked login button")
    except Exception as e:
        print(f"    click failed: {e}")
    # Wait for slider iframe to load
    time.sleep(4)

    # 8. Auto-detect and drag the slider inside #baxia-dialog-content iframe.
    #    The iframe is same-origin (passport.modelscope.cn), so we can
    #    reach into its contentDocument to find the slider handle.
    print(f"\n[8] Attempting to auto-drag the Aliyun baxia slider (in iframe)...")
    slider_dragged = False
    for attempt in range(5):
        try:
            # Look for the slider handle INSIDE the baxia iframe.
            script = (
                "(function() {"
                "  var iframe = document.getElementById('baxia-dialog-content');"
                "  if (!iframe) return JSON.stringify({error: 'no baxia iframe'});"
                "  try {"
                "    var doc = iframe.contentDocument;"
                "    if (!doc) return JSON.stringify({error: 'no contentDocument'});"
                "  } catch(e) { return JSON.stringify({error: 'cross-origin: ' + e.message}); }"
                "  var doc = iframe.contentDocument;"
                "  var candidates = ['#nc_1_n1z', '.nc-lang-cnt .btn_slide', "
                "    '.nc_iconfont.btn_slide', '.scale_text.nc-lang-cnt .btn_slide', "
                "    '#nc_1_scale_btn', '.nc-lang-cnt .btn_slide', "
                "    '.btn_slide', '#aliyunCaptcha-btn', '.slide-btn', '#nc_1_n1c'];"
                "  var h = null;"
                "  for (var i = 0; i < candidates.length; i++) {"
                "    h = doc.querySelector(candidates[i]);"
                "    if (h) {"
                "      var r = h.getBoundingClientRect();"
                "      if (r.width > 0 && r.height > 0) break;"
                "      h = null;"
                "    }"
                "  }"
                "  var tSelectors = ['.nc-lang-cnt', '.scale_text', '.nc-container .nc-lang-cnt', '.nc-container', '#aliyunCaptcha-slide-bar', '.nc_scale', '#nc_1_n1t'];"
                "  var t = null;"
                "  for (var j = 0; j < tSelectors.length; j++) {"
                "    t = doc.querySelector(tSelectors[j]);"
                "    if (t) {"
                "      var tr = t.getBoundingClientRect();"
                "      if (tr.width > 0 && tr.height > 0) break;"
                "      t = null;"
                "    }"
                "  }"
                "  if (h && t) {"
                "    var hr = h.getBoundingClientRect(), tr = t.getBoundingClientRect();"
                "    var ir = iframe.getBoundingClientRect();"
                "    return JSON.stringify({"
                "      handle: {x: hr.left + hr.width/2, y: hr.top + hr.height/2, w: hr.width, h: hr.height},"
                "      track: {x: tr.left, y: tr.top, w: tr.width, h: tr.height},"
                "      iframe: {x: ir.left, y: ir.top, w: ir.width, h: ir.height}"
                "    });"
                "  }"
                "  return JSON.stringify({error: 'slider not visible', bodySnippet: doc.body ? doc.body.innerHTML.slice(0,500) : 'no body'});"
                "})()"
            )
            s, body = _eval(base, args.token, script)
            # Unwrap nested JSON string quotes
            if body and body != "null" and body != '"null"':
                import json as _json
                while body.startswith('"') and body.endswith('"') and len(body) > 2:
                    body = body[1:-1].replace('\\"', '"').replace('\\\\', '\\')
                pos = _json.loads(body)
                if "error" in pos:
                    print(f"    attempt {attempt+1}: {pos['error']}")
                    if "bodySnippet" in pos:
                        print(f"    iframe body snippet: {pos['bodySnippet'][:300]}")
                    time.sleep(2)
                    continue
                hx, hy = pos["handle"]["x"], pos["handle"]["y"]
                tx = pos["track"]["x"] + pos["track"]["w"] - 10
                ty = hy
                if hx == 0 and hy == 0 and tx <= 0:
                    print(f"    attempt {attempt+1}: slider coords invalid")
                    time.sleep(2)
                    continue
                print(f"    attempt {attempt+1}: dragging from ({hx:.0f},{hy:.0f}) to ({tx:.0f},{ty:.0f})")
                # Use iframeSelector + selector so the drag dispatches mouse
                # events on the slider handle INSIDE the baxia iframe.
                req(base, args.token, "POST", "/agent/drag",
                    body={"iframeSelector": "#baxia-dialog-content",
                          "selector": "#nc_1_n1z, .btn_slide",
                          "x2": tx, "y2": ty,
                          "duration": 1200, "steps": 80, "jitter": 4},
                    timeout=20)
                slider_dragged = True
                print("    drag dispatched, waiting 4s for verification...")
                time.sleep(4)
                # Check if slider passed (look in iframe)
                script2 = ("(function(){var iframe=document.getElementById('baxia-dialog-content');if(!iframe)return 'no iframe';try{var doc=iframe.contentDocument;if(!doc)return 'no doc';if(doc.querySelector('.nc-lang-cnt .nc_ok, .icon_ok, .nc_iconfont.icon_ok'))return 'passed';if(doc.querySelector('.errloading, .nc-lang-cnt .err, .nc-lang-cnt .nc_iconfont.btn_warn'))return 'failed';return 'pending'}catch(e){return 'err: '+e.message}})()")
                s2, body2 = _eval(base, args.token, script2)
                print(f"    slider state: {body2}")
                if "passed" in str(body2):
                    print("    slider PASSED!")
                    break
                time.sleep(2)
            else:
                print(f"    attempt {attempt+1}: no response from slider probe")
                time.sleep(2)
        except Exception as e:
            print(f"    attempt {attempt+1} failed: {e}")
            time.sleep(2)

    if not slider_dragged:
        print("\n[!] Could not auto-drag slider. You may need to complete it manually in the webview window.")

    # After slider passes, click login again to actually submit
    if slider_dragged:
        print("\n[9] Clicking login button again to submit...")
        try:
            req(base, args.token, "POST", "/agent/click",
                body={"selector": ".fm-button.fm-submit"}, timeout=10)
            print("    clicked login")
        except Exception as e:
            print(f"    click failed: {e}")

    # 10. Wait for login to complete (URL redirect to modelscope.cn)
    print(f"\n[10] Waiting up to {args.login_wait}s for login to complete...")
    deadline = time.time() + args.login_wait
    last_state = ""
    while time.time() < deadline:
        try:
            r = req(base, args.token, "GET", "/agent/state", timeout=10)
            url = r.get("url", "")
            if "modelscope.cn" in url and "passport.modelscope.cn" not in url:
                print(f"    redirected to {url} — login likely succeeded")
                if is_logged_in(base, args.token):
                    break
            if url != last_state:
                print(f"    current URL: {url[:120]}")
                last_state = url
        except Exception:
            pass
        time.sleep(3)
    else:
        print("\n>>> Timed out waiting for login. Cookies not saved.")
        return 1

    # 11. Save cookies for next time
    if args.no_save:
        print("\n[11] Skipping cookie save (--no-save)")
    else:
        print("\n[11] Saving cookies for next launch...")
        try:
            req(base, args.token, "POST", "/agent/save-cookies")
            print("    cookies saved — next launch will skip login entirely")
        except Exception as e:
            print(f"    save-cookies failed: {e}")

    print("\n>>> Login complete. You can now close the webview window.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

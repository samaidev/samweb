#!/usr/bin/env python3
"""
Test script for the SamWeb Agent API.

Starts the samweb-agent-test binary (a headless mock backend), then
exercises every /agent/* endpoint to verify the API contract works
end-to-end. Saves the screenshot to /home/z/my-project/download/.

Usage:
    python3 scripts/test_agent.py
"""
import json
import os
import signal
import subprocess
import sys
import time
import urllib.parse
import urllib.request
import urllib.error

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Pick the right binary name for the current OS.
if os.name == "nt":
    BINARY = os.path.join(REPO, "samweb-agent-test.exe")
    DOWNLOAD_DIR = os.path.join(os.environ.get("USERPROFILE", "C:\\"), "Downloads")
else:
    BINARY = os.path.join(REPO, "samweb-agent-test")
    DOWNLOAD_DIR = "/home/z/my-project/download"
ADDR = "127.0.0.1:7788"
BASE = f"http://{ADDR}"

os.makedirs(DOWNLOAD_DIR, exist_ok=True)


def log(msg):
    print(f"  {msg}")


def req(method, path, body=None, expect=200, token=None):
    url = BASE + path
    data = None
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(r, timeout=15) as resp:
            status = resp.status
            ctype = resp.headers.get("Content-Type", "")
            payload = resp.read()
            if status != expect:
                raise RuntimeError(f"{method} {path}: expected {expect}, got {status}: {payload[:200]}")
            if ctype.startswith("application/json"):
                return json.loads(payload)
            return payload
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        raise RuntimeError(f"{method} {path}: HTTP {e.code}: {body[:300]}") from None


def get_raw(path, expect=200):
    url = BASE + path
    r = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(r, timeout=30) as resp:
        if resp.status != expect:
            raise RuntimeError(f"GET {path}: expected {expect}, got {resp.status}")
        return resp.read()


def section(title):
    print(f"\n=== {title} ===")


def assert_eq(name, actual, expected):
    if actual != expected:
        print(f"  FAIL: {name}: expected {expected!r}, got {actual!r}")
        sys.exit(1)
    log(f"OK: {name} = {actual!r}")


def assert_true(name, cond, detail=""):
    if not cond:
        print(f"  FAIL: {name}: condition false {detail}")
        sys.exit(1)
    log(f"OK: {name}")


def main():
    # 1. Start the agent test binary.
    print(f"Starting {BINARY} on {ADDR} ...")
    proc = subprocess.Popen(
        [BINARY, "--addr", ADDR],
        cwd=REPO,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )

    try:
        # Wait for health endpoint to come up.
        for _ in range(30):
            try:
                req("GET", "/agent/health")
                break
            except Exception:
                time.sleep(0.2)
        else:
            out = proc.stdout.read(2000) if proc.stdout else b""
            print("Server failed to start. Output:")
            print(out.decode(errors="replace"))
            sys.exit(1)
        print(f"Server up at {BASE}")

        # 2. Health
        section("Health")
        r = req("GET", "/agent/health")
        assert_eq("status", r["status"], "ok")
        assert_true("has time", "time" in r)

        # 3. Initial state
        section("Initial state")
        r = req("GET", "/agent/state")
        assert_true("has url", r["url"].startswith("http"))
        assert_true("has title", len(r["title"]) > 0)
        assert_eq("canBack initially", r["canBack"], False)
        assert_eq("canForward initially", r["canForward"], False)
        log(f"initial url = {r['url']}")
        log(f"initial title = {r['title']}")

        # 4. Navigate
        section("Navigate")
        r = req("POST", "/agent/navigate", {"url": "https://example.com/page1"})
        assert_eq("navigate ok", r["ok"], True)
        r = req("GET", "/agent/state")
        assert_true("url changed", "page1" in r["url"], f"url={r['url']}")
        log(f"after navigate url = {r['url']}")

        # Navigate again so we have history
        req("POST", "/agent/navigate", {"url": "https://example.com/page2"})
        r = req("GET", "/agent/state")
        assert_true("url is page2", "page2" in r["url"], f"url={r['url']}")
        assert_true("canBack after 2 navs", r["canBack"] is True)

        # 5. Back / Forward
        section("Back / Forward")
        r = req("POST", "/agent/back")
        assert_eq("back ok", r["ok"], True)
        r = req("GET", "/agent/state")
        assert_true("back to page1", "page1" in r["url"], f"url={r['url']}")
        assert_true("canForward after back", r["canForward"] is True)

        r = req("POST", "/agent/forward")
        r = req("GET", "/agent/state")
        assert_true("forward to page2", "page2" in r["url"], f"url={r['url']}")

        # 6. Reload
        section("Reload")
        r = req("POST", "/agent/reload")
        assert_eq("reload ok", r["ok"], True)

        # 7. Eval
        section("Eval (JS)")
        r = req("POST", "/agent/eval", {"script": "1 + 1"})
        assert_eq("1+1", r["value"], 2)
        r = req("POST", "/agent/eval", {"script": "document.title"})
        log(f"document.title -> {r['value']}")
        r = req("POST", "/agent/eval", {"script": "window.location.href"})
        log(f"window.location.href -> {r['value']}")
        r = req("POST", "/agent/eval", {"script": "document.querySelectorAll('a').length"})
        log(f"a count -> {r['value']}")

        # 8. Elements / Element (with coordinates)
        section("Elements & element coordinates")
        r = req("GET", "/agent/elements?selector=" + urllib.parse.quote("a", safe=""))
        assert_true(">=2 anchors", r["count"] >= 2, f"count={r['count']}")
        log(f"found {r['count']} <a> elements")
        for el in r["elements"][:3]:
            log(f"  - {el['tag']}#{el.get('id','')} x={el['x']:.0f} y={el['y']:.0f} "
                f"w={el['width']:.0f} h={el['height']:.0f} text={el['text'][:30]!r}")

        r = req("GET", "/agent/element?selector=" + urllib.parse.quote("#searchbox", safe=""))
        assert_eq("searchbox tag", r["tag"], "input")
        assert_true("searchbox has x", r["x"] >= 0)
        assert_true("searchbox has y", r["y"] >= 0)
        log(f"searchbox coords = ({r['x']:.0f}, {r['y']:.0f}) size = {r['width']:.0f}x{r['height']:.0f}")

        # Selector that doesn't match -> 404
        try:
            req("GET", "/agent/element?selector=" + urllib.parse.quote(".does-not-exist", safe=""))
            print("  FAIL: expected 404 for missing element")
            sys.exit(1)
        except RuntimeError as e:
            assert_true("404 for missing element", "404" in str(e), str(e))

        # 9. Click
        section("Click")
        r = req("POST", "/agent/click", {"selector": "#link1"})
        assert_eq("click ok", r["ok"], True)
        log(f"clicked #link1, returned tag={r.get('tag')}, text={r.get('text','')[:40]!r}")

        r = req("POST", "/agent/click", {"x": 50, "y": 190})  # near link1
        assert_eq("click-by-coord ok", r["ok"], True)

        # Click with bad selector -> error
        try:
            req("POST", "/agent/click", {"selector": "#nope"})
            print("  FAIL: expected error for missing element click")
            sys.exit(1)
        except RuntimeError as e:
            assert_true("error for missing click", "not found" in str(e).lower() or "500" in str(e), str(e))

        # 10. Type
        section("Type")
        r = req("POST", "/agent/type", {"selector": "#searchbox", "text": "hello world"})
        assert_eq("type ok", r["ok"], True)
        r = req("POST", "/agent/type", {"selector": "#searchbox", "text": "!", "clear": True})
        assert_eq("type clear ok", r["ok"], True)

        # 11. Key press
        section("Key press")
        r = req("POST", "/agent/key", {"key": "Enter"})
        assert_eq("key ok", r["ok"], True)
        r = req("POST", "/agent/key", {"key": "a", "modifiers": ["ctrl"]})
        assert_eq("key+modifier ok", r["ok"], True)

        # 11b. Drag (new endpoint — mocked, just verify it accepts the body)
        section("Drag")
        r = req("POST", "/agent/drag", {"x1": 100, "y1": 200, "x2": 300, "y2": 200})
        assert_eq("drag ok", r["ok"], True)
        # Bad request: missing both start and end
        try:
            req("POST", "/agent/drag", {})
            print("  FAIL: expected 400 for empty drag")
            sys.exit(1)
        except RuntimeError as e:
            assert_true("400 for empty drag", "400" in str(e), str(e))

        # 12. Scroll
        section("Scroll")
        r = req("POST", "/agent/scroll", {"direction": "down", "amount": 300})
        assert_eq("scroll down ok", r["ok"], True)
        log(f"scroll position after down: x={r.get('scrollX')} y={r.get('scrollY')}")
        r = req("POST", "/agent/scroll", {"x": 0, "y": 0})
        assert_eq("scroll to 0,0 ok", r["ok"], True)
        r = req("POST", "/agent/scroll", {"selector": "#link2"})
        assert_eq("scroll to element ok", r["ok"], True)

        # 13. Wait
        section("Wait")
        r = req("POST", "/agent/wait", {"selector": "#link1", "timeoutMs": 1000})
        assert_eq("wait ok", r["ok"], True)

        # 14. Screenshot
        section("Screenshot")
        png = get_raw("/agent/screenshot")
        assert_true("screenshot is non-empty PNG",
                    len(png) > 100 and png[:8] == b"\x89PNG\r\n\x1a\n",
                    f"len={len(png)} header={png[:8]!r}")
        out = os.path.join(DOWNLOAD_DIR, "samweb_agent_screenshot.png")
        with open(out, "wb") as f:
            f.write(png)
        log(f"saved screenshot to {out} ({len(png)} bytes)")

        # Full-page screenshot
        png2 = get_raw("/agent/screenshot?fullPage=true")
        assert_true("full-page screenshot is PNG", png2[:8] == b"\x89PNG\r\n\x1a\n")
        out2 = os.path.join(DOWNLOAD_DIR, "samweb_agent_screenshot_fullpage.png")
        with open(out2, "wb") as f:
            f.write(png2)
        log(f"saved full-page screenshot to {out2} ({len(png2)} bytes)")

        # 15. Stop
        section("Stop")
        r = req("POST", "/agent/stop")
        assert_eq("stop ok", r["ok"], True)

        # 16. Reset cookies (new endpoint)
        section("Reset cookies")
        r = req("POST", "/agent/reset-cookies")
        assert_eq("reset-cookies ok", r["ok"], True)
        # GET on reset-cookies should be 405 method-not-allowed
        try:
            req("GET", "/agent/reset-cookies")
            print("  FAIL: expected 405 for GET on reset-cookies")
            sys.exit(1)
        except RuntimeError as e:
            assert_true("405 for GET on reset-cookies", "405" in str(e), str(e))

        # 17. Save / Load cookies (new endpoints). On the mock backend these
        # are no-ops but the endpoints must still respond 200.
        section("Save / Load cookies")
        r = req("POST", "/agent/save-cookies")
        assert_eq("save-cookies ok", r["ok"], True)
        r = req("POST", "/agent/load-cookies")
        assert_eq("load-cookies ok", r["ok"], True)

    finally:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        print(f"\nServer stopped (exit code {proc.returncode}).")

    # 16. Auth test (restart with token)
    print(f"\n=== Auth (token gate) ===")
    print(f"Starting {BINARY} with --token s3cret ...")
    proc = subprocess.Popen(
        [BINARY, "--addr", ADDR, "--token", "s3cret"],
        cwd=REPO,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    try:
        for _ in range(30):
            try:
                req("GET", "/agent/health")
                break
            except Exception:
                time.sleep(0.2)

        # Without token -> 401
        try:
            req("GET", "/agent/state")
            print("  FAIL: expected 401 without token")
            sys.exit(1)
        except RuntimeError as e:
            assert_true("401 without token", "401" in str(e), str(e))

        # With wrong token -> 401
        try:
            r = urllib.request.Request(BASE + "/agent/state", method="GET",
                                       headers={"Authorization": "Bearer wrong"})
            urllib.request.urlopen(r, timeout=5)
            print("  FAIL: expected 401 with wrong token")
            sys.exit(1)
        except urllib.error.HTTPError as e:
            assert_eq("wrong token status", e.code, 401)

        # With correct token -> 200
        r = urllib.request.Request(BASE + "/agent/state", method="GET",
                                   headers={"Authorization": "Bearer s3cret"})
        with urllib.request.urlopen(r, timeout=5) as resp:
            data = json.loads(resp.read())
            assert_true("with token, state has url", "url" in data)
            log(f"with token, url = {data['url']}")

        # Health is always public
        r = req("GET", "/agent/health")
        assert_eq("health public", r["status"], "ok")

    finally:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()

    print("\n========================================")
    print("  ALL AGENT API TESTS PASSED")
    print("========================================")
    print(f"\nScreenshots saved to:")
    print(f"  - {DOWNLOAD_DIR}/samweb_agent_screenshot.png")
    print(f"  - {DOWNLOAD_DIR}/samweb_agent_screenshot_fullpage.png")


if __name__ == "__main__":
    main()

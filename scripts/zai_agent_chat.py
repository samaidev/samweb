#!/usr/bin/env python3
"""z.ai Agent 模式自动化 — 使用已保存的 cookie 免登录访问。

完整流程：
  1. 加载已保存的 cookie（首次需要手动登录）
  2. 导航到 z.ai
  3. 展开侧边栏
  4. 切换到 Agent 模式
  5. 在输入框输入消息
  6. 点击发送
  7. 获取 AI 输出流
  8. 如果返回"高峰期"提示，自动切换模型并重试

Usage:
  python3 zai_agent_chat.py --message "你好"
  python3 zai_agent_chat.py --message "写一个 Python hello world" --model GLM-5.1
  python3 zai_agent_chat.py --message "你好" --max-retries 5 --retry-delay 10

Requirements:
  - shan 机器运行 samweb（在 RDP session 中，带 GUI）
  - 已保存的 z.ai cookie（首次手动登录后自动保存）
"""
import argparse
import json
import os
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


# z.ai 模型列表（按优先级排序，用于高峰期切换）
AVAILABLE_MODELS = ["GLM-5.2", "GLM-5.1", "GLM-5-Turbo", "GLM-5V-Turbo", "GLM-4.7"]

# 高峰期/错误提示关键词
RETRY_KEYWORDS = [
    "当前模型使用人数较多",
    "请稍后再试",
    "服务繁忙",
    "请求过于频繁",
    "rate limit",
    "服务暂时不可用",
    "请稍候再试",
]


def unwrap(v):
    s = str(v).strip()
    while len(s) >= 2 and s[0] == '"' and s[-1] == '"':
        s = s[1:-1].replace('\\"', '"').replace('\\\\', '\\')
    try:
        return json.loads(s)
    except Exception:
        return s


def shot(a, path):
    png = a.req("GET", "/agent/screenshot-trusted", timeout=30)
    if isinstance(png, (bytes, bytearray)):
        with open(path, "wb") as f:
            f.write(png)


def cdp_click(a, x, y):
    """CDP mouse click at (x, y)."""
    a.post("/agent/cdp-mouse", {
        "type": "mousePressed", "x": x, "y": y,
        "button": "left", "buttons": 1, "clickCount": 1
    }, timeout=15)
    time.sleep(0.1)
    a.post("/agent/cdp-mouse", {
        "type": "mouseReleased", "x": x, "y": y,
        "button": "left", "buttons": 0, "clickCount": 1
    }, timeout=15)


def cdp_click_center(a, selector_script):
    """Click the center of an element found via JS selector script.

    selector_script should return JSON with cx, cy (center coords).
    """
    _, v = a.eval(selector_script)
    info = unwrap(v)
    if isinstance(info, dict) and info.get("cx") is not None:
        cdp_click(a, info["cx"], info["cy"])
        return True
    return False


def ensure_logged_in(a):
    """Check if we're logged in (token in localStorage). If not, prompt
    the user to log in manually."""
    print("=== Checking login status ===")
    script = r"""(function(){
        return JSON.stringify({
            url: location.href,
            token: !!localStorage.getItem('token')
        });
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    print(f"  URL: {info.get('url')}")
    print(f"  token: {'present' if info.get('token') else 'absent'}")

    if info.get("token"):
        return True

    # Not logged in — navigate to /auth and ask user to log in
    print("\n  ⚠ Not logged in. Navigating to /auth...")
    a.post("/agent/navigate-direct", {"url": "https://chat.z.ai/auth"})
    time.sleep(5)
    print("  请在 webview 窗口手动登录 z.ai")
    print("  登录完成后按 Enter 继续...")
    input()

    # Save cookies after manual login
    print("  Saving cookies...")
    a.post("/agent/save-cookies")
    return True


def navigate_to_home(a):
    """Navigate to z.ai home page."""
    print("\n=== Navigating to z.ai ===")
    a.post("/agent/navigate-direct", {"url": "https://chat.z.ai/"})
    time.sleep(5)
    s = a.state()
    print(f"  URL: {s.get('url')}")


def expand_sidebar(a):
    """Click the sidebar toggle button to expand it.

    Uses JS click() instead of CDP coordinates because the toggle button
    can be at negative x (off-screen) when the sidebar is collapsed in
    a narrow window.
    """
    print("\n=== Expanding sidebar ===")
    script = r"""(function(){
        var svgs = document.querySelectorAll('svg.rotate-180, svg[class*="rotate-180"]');
        for (var i = 0; i < svgs.length; i++) {
            var svg = svgs[i];
            var r = svg.getBoundingClientRect();
            if (r.width > 0 && r.height > 0) {
                var el = svg;
                while (el && el !== document.body) {
                    if (el.tagName === 'BUTTON') {
                        el.click();
                        return JSON.stringify({ok: true, tag: el.tagName});
                    }
                    el = el.parentElement;
                }
                svg.click();
                return JSON.stringify({ok: true, fallback: 'svg'});
            }
        }
        return JSON.stringify({ok: false, error: 'no toggle'});
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    print(f"  result: {info}")
    time.sleep(2)
    return info.get("ok", False)


def switch_to_agent_mode(a):
    """Click the 'Agent 模式' button in the sidebar (via JS click)."""
    print("\n=== Switching to Agent 模式 ===")
    script = r"""(function(){
        var btns = document.querySelectorAll('button');
        for (var i = 0; i < btns.length; i++) {
            var b = btns[i];
            if ((b.innerText || '').trim() === 'Agent 模式') {
                var r = b.getBoundingClientRect();
                if (r.width > 0 && r.height > 0) {
                    b.click();
                    return JSON.stringify({ok: true});
                }
            }
        }
        return JSON.stringify({ok: false, error: 'not found'});
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    print(f"  result: {info}")
    time.sleep(2)
    return info.get("ok", False)


def switch_model(a, model_name):
    """Switch the model to model_name (e.g. 'GLM-5.1').

    Returns True if the model was switched successfully.
    """
    print(f"\n=== Switching model to {model_name} ===")
    # Click the model selector button (top-left, class modelSelectorButton)
    script = r"""(function(){
        var btn = document.querySelector('button.modelSelectorButton, button[class*="modelSelectorButton"]');
        if (!btn) return JSON.stringify({error: 'no model selector'});
        var r = btn.getBoundingClientRect();
        return JSON.stringify({cx: Math.round(r.left + r.width/2), cy: Math.round(r.top + r.height/2), text: btn.innerText.trim()});
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    if info.get("cx") is None:
        print(f"  ⚠ model selector not found: {info.get('error')}")
        return False

    print(f"  current model: {info.get('text')}")
    if info.get("text") == model_name:
        print(f"  already on {model_name}, no switch needed")
        return True

    # Click to open dropdown
    cdp_click(a, info["cx"], info["cy"])
    time.sleep(1.5)

    # Find the target model in the dropdown
    script2 = f"""(function(){{
        var all = document.querySelectorAll('div, button, li');
        for (var i = 0; i < all.length; i++) {{
            var el = all[i];
            var t = (el.innerText || '').trim();
            if (t === '{model_name}' || t.startsWith('{model_name}')) {{
                var r = el.getBoundingClientRect();
                if (r.width > 0 && r.height > 0) {{
                    return JSON.stringify({{cx: Math.round(r.left + r.width/2), cy: Math.round(r.top + r.height/2)}});
                }}
            }}
        }}
        return JSON.stringify({{error: 'model {model_name} not found in dropdown'}});
    }})()"""
    _, v2 = a.eval(script2)
    info2 = unwrap(v2)
    if info2.get("cx") is None:
        print(f"  ⚠ {info2.get('error')}")
        # Close dropdown by clicking elsewhere
        cdp_click(a, 300, 400)
        return False

    cdp_click(a, info2["cx"], info2["cy"])
    print(f"  clicked {model_name} at ({info2['cx']},{info2['cy']})")
    time.sleep(1.5)
    return True


def type_message(a, message):
    """Type the message into the #chat-input textarea."""
    print(f"\n=== Typing message: {message!r} ===")
    r = a.post("/agent/type", {
        "selector": "#chat-input",
        "text": message,
        "clear": True
    }, timeout=15)
    print(f"  type result: {r.get('ok')}")

    # Verify
    script = r"""(function(){
        var ta = document.getElementById('chat-input');
        return ta ? ta.value : 'not found';
    })()"""
    _, v = a.eval(script)
    print(f"  textarea value: {v!r}")
    return str(v) == message


def send_message(a):
    """Click the send button via JS click (more reliable than CDP coords)."""
    print("\n=== Sending message ===")
    # Focus the textarea first
    a.eval(r"""(function(){var ta=document.getElementById('chat-input');if(ta)ta.focus();return 'ok';})()""")
    time.sleep(0.3)

    # JS click the send button
    script = r"""(function(){
        var btn = document.querySelector('button.sendMessageButton, button[class*="sendMessageButton"]');
        if (!btn) return JSON.stringify({error: 'no send button'});
        if (btn.disabled) return JSON.stringify({error: 'disabled'});
        btn.click();
        return JSON.stringify({ok: true});
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    print(f"  result: {info}")
    time.sleep(3)
    return info.get("ok", False)


def get_response(a, max_wait=60, stable_count=3):
    """Poll for the AI response. Returns the response text, or None on timeout.

    The response is considered stable when it hasn't changed for
    stable_count consecutive checks.
    """
    print(f"\n=== Waiting for AI response (max {max_wait}s) ===")
    last_text = ""
    stable = 0
    start = time.time()

    while time.time() - start < max_wait:
        script = r"""(function(){
            var asstMsgs = document.querySelectorAll('[class*="chat-assistant"]');
            var userMsgs = document.querySelectorAll('[class*="user-message"]');
            var asstText = [];
            var userText = [];
            for (var i = 0; i < userMsgs.length; i++) {
                userText.push((userMsgs[i].innerText || '').trim());
            }
            for (var i = 0; i < asstMsgs.length; i++) {
                // Get the full text but strip "思考过程" / "正在思考" / "跳过" UI labels
                var t = (asstMsgs[i].innerText || '').trim();
                // Remove thinking-chain UI text
                t = t.replace(/^正在思考\s*跳过\s*/, '').replace(/^思考过程\s*/, '');
                asstText.push(t);
            }
            return JSON.stringify({
                user_count: userMsgs.length,
                asst_count: asstMsgs.length,
                user_messages: userText,
                assistant_messages: asstText
            });
        })()"""
        _, v = a.eval(script)
        info = unwrap(v)
        if not isinstance(info, dict):
            time.sleep(2)
            continue

        asst_msgs = info.get("assistant_messages", [])
        current = asst_msgs[-1] if asst_msgs else ""

        if current and current == last_text:
            stable += 1
            if stable >= stable_count:
                elapsed = int(time.time() - start)
                print(f"  ✓ response stable after {elapsed}s")
                return current
        else:
            stable = 0
            if current:
                elapsed = int(time.time() - start)
                print(f"  [{elapsed}s] {current[:100]}...")
            last_text = current

        time.sleep(2)

    print(f"  ⚠ timeout after {max_wait}s")
    return last_text or None


def is_retry_response(response):
    """Check if the response is a high-traffic / retry prompt."""
    if not response:
        return False
    for kw in RETRY_KEYWORDS:
        if kw in response:
            return True
    return False


def new_chat(a):
    """Start a new chat (click the '新建对话' / 'New Chat' button)."""
    print("\n=== Starting new chat ===")
    script = r"""(function(){
        // Look for "新对话" or "新建对话" or "New Chat" button
        var btns = document.querySelectorAll('button, a');
        for (var i = 0; i < btns.length; i++) {
            var b = btns[i];
            var t = (b.innerText || '').trim();
            if (/新对话|新建对话|New Chat|new chat/i.test(t)) {
                var r = b.getBoundingClientRect();
                if (r.width > 0 && r.height > 0) {
                    return JSON.stringify({cx: Math.round(r.left + r.width/2), cy: Math.round(r.top + r.height/2)});
                }
            }
        }
        return JSON.stringify({error: 'new chat button not found'});
    })()"""
    _, v = a.eval(script)
    info = unwrap(v)
    if info.get("cx") is not None:
        cdp_click(a, info["cx"], info["cy"])
        print(f"  clicked new chat at ({info['cx']},{info['cy']})")
        time.sleep(2)
        return True
    # Fallback: navigate to home
    print("  new chat button not found, navigating to home")
    navigate_to_home(a)
    return False


def send_and_get_response(a, message, model=None, max_retries=5, retry_delay=10):
    """Send a message and get the response, with retry on high-traffic.

    Args:
        a: Agent instance
        message: the message to send
        model: preferred model name (e.g. "GLM-5.2"). If None, use current.
        max_retries: max number of retry attempts
        retry_delay: seconds to wait between retries

    Returns:
        dict with keys: success (bool), response (str), attempts (list)
    """
    attempts = []

    # Determine which models to try
    if model:
        # Try the specified model first, then fall back to others
        models_to_try = [model] + [m for m in AVAILABLE_MODELS if m != model]
    else:
        models_to_try = AVAILABLE_MODELS

    for attempt_idx in range(max_retries):
        # Pick a model for this attempt (cycle through the list)
        current_model = models_to_try[attempt_idx % len(models_to_try)]
        print(f"\n{'='*60}")
        print(f"  Attempt {attempt_idx + 1}/{max_retries} — model: {current_model}")
        print(f"{'='*60}")

        # Switch model (only if not first attempt or if model was specified)
        if attempt_idx > 0 or model:
            switch_model(a, current_model)
            time.sleep(1)

        # Start a new chat (only after first attempt)
        if attempt_idx > 0:
            new_chat(a)
            time.sleep(1)

        # Type and send
        if not type_message(a, message):
            attempts.append({"model": current_model, "error": "type failed"})
            continue
        if not send_message(a):
            attempts.append({"model": current_model, "error": "send failed"})
            continue

        # Get response
        response = get_response(a, max_wait=60)
        attempts.append({"model": current_model, "response": response})

        if response is None:
            print(f"  ⚠ no response")
            time.sleep(retry_delay)
            continue

        # Check if it's a retry prompt
        if is_retry_response(response):
            print(f"\n  ⚠ high-traffic response: {response[:80]}")
            print(f"  waiting {retry_delay}s before retry with next model...")
            time.sleep(retry_delay)
            continue

        # Success!
        print(f"\n>>> SUCCESS with {current_model}")
        return {
            "success": True,
            "response": response,
            "model": current_model,
            "attempts": attempts,
        }

    # All retries failed
    print(f"\n>>> All {max_retries} attempts failed")
    return {
        "success": False,
        "response": response if 'response' in dir() else None,
        "attempts": attempts,
    }


def main():
    ap = argparse.ArgumentParser(description="z.ai Agent 模式自动化")
    ap.add_argument("--message", "-m", required=True,
                    help="要发送的消息")
    ap.add_argument("--model", default=None,
                    help=f"首选模型 (默认用当前)。可选: {', '.join(AVAILABLE_MODELS)}")
    ap.add_argument("--max-retries", type=int, default=5,
                    help="最大重试次数（默认 5）")
    ap.add_argument("--retry-delay", type=int, default=10,
                    help="重试间隔秒数（默认 10）")
    ap.add_argument("--save-screenshot", action="store_true",
                    help="保存最终截图到 download/")
    args = ap.parse_args()

    a = Agent(verbose=False)
    try:
        # 1. Health check
        print("=== Health check ===")
        r = a.get("/agent/health")
        print(f"  status: {r.get('status')}")

        # 2. Load cookies (from previous manual login)
        print("\n=== Loading saved cookies ===")
        try:
            a.post("/agent/load-cookies")
            print("  cookies loaded")
        except Exception as e:
            print(f"  load-cookies: {e}")

        # 3. Navigate to z.ai
        navigate_to_home(a)

        # 4. Check login
        if not ensure_logged_in(a):
            print("\n>>> Failed to log in. Please run manual login first.")
            return 1

        # 5. Expand sidebar
        expand_sidebar(a)

        # 6. Switch to Agent 模式
        if not switch_to_agent_mode(a):
            print(">>> Could not switch to Agent mode")
            return 1

        # 7. Send message with retry logic
        result = send_and_get_response(
            a, args.message,
            model=args.model,
            max_retries=args.max_retries,
            retry_delay=args.retry_delay,
        )

        # 8. Print result
        print(f"\n{'='*60}")
        print(f"RESULT")
        print(f"{'='*60}")
        print(f"success: {result['success']}")
        if result.get("model"):
            print(f"model: {result['model']}")
        print(f"attempts: {len(result['attempts'])}")
        print(f"\nresponse:")
        print(result.get("response") or "(no response)")

        # 9. Save screenshot
        if args.save_screenshot:
            shot(a, f"/home/z/my-project/download/zai_agent_result.png")
            print(f"\nscreenshot saved -> /home/z/my-project/download/zai_agent_result.png")

        # 10. Save cookies for next run
        print("\n=== Saving cookies ===")
        a.post("/agent/save-cookies")
        print("  cookies saved")

        return 0 if result["success"] else 1

    finally:
        a.close()


if __name__ == "__main__":
    sys.exit(main())

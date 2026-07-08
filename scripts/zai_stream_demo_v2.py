#!/usr/bin/env python3
"""Stream demo v2 — fixed flow:
  1. Navigate to z.ai
  2. Expand sidebar (JS click)
  3. Click "Agent 模式"
  4. Wait for the agent page to load
  5. Type message + send
  6. Poll for streaming output (with proper thinking/response detection)
"""
import json
import os
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def unwrap(v):
    s = str(v).strip()
    while len(s) >= 2 and s[0] == '"' and s[-1] == '"':
        s = s[1:-1].replace('\\"', '"').replace('\\\\', '\\')
    try:
        return json.loads(s)
    except Exception:
        return s


def cdp_click(a, x, y):
    a.post("/agent/cdp-mouse", {
        "type": "mousePressed", "x": x, "y": y,
        "button": "left", "buttons": 1, "clickCount": 1
    }, timeout=15)
    time.sleep(0.1)
    a.post("/agent/cdp-mouse", {
        "type": "mouseReleased", "x": x, "y": y,
        "button": "left", "buttons": 0, "clickCount": 1
    }, timeout=15)


def js_click(a, script):
    """Run a JS script that finds and clicks an element. Returns the result."""
    _, v = a.eval(script)
    return unwrap(v)


def main():
    message = "搜索今天3大AI新闻"
    
    a = Agent(verbose=False)
    try:
        # 1. Health + load cookies
        print("=== Health check ===")
        print(f"  status: {a.get('/agent/health').get('status')}")
        
        print("\n=== Loading cookies ===")
        a.post("/agent/load-cookies")
        
        # 2. Navigate to z.ai
        print("\n=== Navigating to z.ai ===")
        a.post("/agent/navigate-direct", {"url": "https://chat.z.ai/"})
        time.sleep(5)
        
        # 3. Expand sidebar (JS click — button is off-screen at x=-10)
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
                            return JSON.stringify({ok: true, tag: el.tagName, cls: (el.className || '').toString().slice(0, 60)});
                        }
                        el = el.parentElement;
                    }
                    svg.click();
                    return JSON.stringify({ok: true, fallback: 'svg'});
                }
            }
            return JSON.stringify({ok: false});
        })()"""
        info = js_click(a, script)
        print(f"  result: {info}")
        time.sleep(2)
        
        # 4. Switch to Agent 模式
        print("\n=== Switching to Agent 模式 ===")
        script = r"""(function(){
            var btns = document.querySelectorAll('button');
            for (var i = 0; i < btns.length; i++) {
                var b = btns[i];
                if ((b.innerText || '').trim() === 'Agent 模式') {
                    var r = b.getBoundingClientRect();
                    if (r.width > 0 && r.height > 0) {
                        b.click();
                        return JSON.stringify({ok: true, x: Math.round(r.left + r.width/2), y: Math.round(r.top + r.height/2)});
                    }
                }
            }
            return JSON.stringify({ok: false});
        })()"""
        info = js_click(a, script)
        print(f"  result: {info}")
        time.sleep(3)
        
        # 5. Verify we're in Agent mode (check URL or page state)
        s = a.state()
        print(f"  URL after Agent click: {s.get('url')}")
        
        # 6. Type message
        print(f"\n=== Typing message: {message!r} ===")
        a.post("/agent/type", {"selector": "#chat-input", "text": message, "clear": True}, timeout=15)
        time.sleep(1)
        
        # Verify
        _, v = a.eval(r"""(function(){var ta=document.getElementById('chat-input');return ta?ta.value:'none';})()""")
        print(f"  textarea value: {v}")
        
        # 7. Send — use JS btn.click() instead of CDP coordinates
        # CDP coordinates are unreliable because the window may have shifted.
        # JS click() directly invokes the button's click handler.
        print("\n=== Sending (via JS click) ===")
        a.eval(r"""(function(){var ta=document.getElementById('chat-input');if(ta)ta.focus();return 'focused';})()""")
        time.sleep(0.3)
        
        script = r"""(function(){
            var btn = document.querySelector('button.sendMessageButton, button[class*="sendMessageButton"]');
            if (!btn) return JSON.stringify({error: 'no send btn'});
            if (btn.disabled) return JSON.stringify({error: 'disabled'});
            btn.click();
            return JSON.stringify({ok: true});
        })()"""
        info = js_click(a, script)
        print(f"  send result: {info}")
        time.sleep(5)
        
        # Verify message was sent (URL should change to /c/<chat-id>)
        s = a.state()
        print(f"  URL after send: {s.get('url')}")
        
        # 8. Poll for streaming output
        print(f"\n{'='*60}")
        print(f"STREAMING OUTPUT (polling every 2s, max 240s)")
        print(f"{'='*60}\n")
        
        last_response = ""
        last_thinking = ""
        stable_count = 0
        start = time.time()
        seen_first_response = False
        
        while time.time() - start < 240:
            script = r"""(function(){
                var asstMsgs = document.querySelectorAll('[class*="chat-assistant"]');
                var userMsgs = document.querySelectorAll('[class*="user-message"]');
                
                var result = {
                    user_count: userMsgs.length,
                    asst_count: asstMsgs.length,
                    thinking: '',
                    response: '',
                    stage: 'idle'
                };
                
                if (asstMsgs.length === 0) {
                    result.stage = 'waiting';
                    return JSON.stringify(result);
                }
                
                var last = asstMsgs[asstMsgs.length - 1];
                var fullText = (last.innerText || '').trim();
                result.full_text = fullText.slice(0, 300);
                
                // Detect stages:
                // 1. "正在思考" / "跳过" = thinking in progress (no response yet)
                // 2. Response content in <p> tags
                
                // Check for thinking indicator
                var thinkingIndicator = last.querySelector('[class*="thinking-chain"], [class*="思考"]');
                if (thinkingIndicator) {
                    var tt = (thinkingIndicator.innerText || '').trim();
                    if (tt) {
                        result.thinking = tt.slice(0, 200);
                        result.stage = 'thinking';
                    }
                }
                
                // Check for "正在思考" text directly
                if (/正在思考|跳过/.test(fullText) && fullText.length < 100) {
                    result.stage = 'thinking';
                    result.thinking = fullText;
                }
                
                // Extract response from <p> tags (the actual answer)
                var ps = last.querySelectorAll('p');
                var parts = [];
                for (var i = 0; i < ps.length; i++) {
                    var t = (ps[i].innerText || '').trim();
                    // Skip UI text
                    if (t && t !== '思考过程' && t !== '正在思考' && t !== '跳过' && t.length > 1) {
                        parts.push(t);
                    }
                }
                if (parts.length > 0) {
                    result.response = parts.join('\n\n');
                    result.stage = result.thinking ? 'thinking_and_responding' : 'responding';
                }
                
                return JSON.stringify(result);
            })()"""
            _, v = a.eval(script)
            info = unwrap(v)
            if not isinstance(info, dict):
                time.sleep(2)
                continue
            
            elapsed = int(time.time() - start)
            stage = info.get("stage", "idle")
            
            if stage == "waiting":
                if elapsed % 10 < 2:
                    print(f"[{elapsed}s] waiting for response...")
            elif stage == "thinking":
                thinking = info.get("thinking", "")
                if thinking != last_thinking:
                    print(f"[{elapsed}s] 🤔 思考中: {thinking[:80]}")
                    last_thinking = thinking
            elif stage in ("responding", "thinking_and_responding"):
                response = info.get("response", "")
                if response and response != last_response:
                    if not seen_first_response:
                        print(f"[{elapsed}s] 📤 Response starts:\n")
                        print(response)
                        seen_first_response = True
                    elif last_response and response.startswith(last_response):
                        # Incremental — print new part
                        new_part = response[len(last_response):]
                        sys.stdout.write(new_part)
                        sys.stdout.flush()
                    else:
                        # Response changed (e.g. re-generated)
                        print(f"\n[{elapsed}s] 📤 Response updated:\n{response}")
                    last_response = response
                    stable_count = 0
                else:
                    stable_count += 1
                    if stable_count >= 4 and elapsed > 15:
                        print(f"\n\n[{elapsed}s] ✓ Response complete (stable 8s)")
                        break
            
            time.sleep(2)
        
        # Final output
        print(f"\n\n{'='*60}")
        print(f"FINAL RESPONSE (after {int(time.time() - start)}s)")
        print(f"{'='*60}")
        print(last_response or "(no response captured)")
        
        # Save
        png = a.req("GET", "/agent/screenshot-trusted", timeout=30)
        with open("/home/z/my-project/download/zai_stream_result.png", "wb") as f:
            f.write(png)
        with open("/home/z/my-project/download/zai_stream_response.txt", "w", encoding="utf-8") as f:
            f.write(last_response or "(no response)")
        print(f"\n>>> saved: zai_stream_result.png, zai_stream_response.txt")
        
        return 0
        
    finally:
        a.close()


if __name__ == "__main__":
    sys.exit(main())

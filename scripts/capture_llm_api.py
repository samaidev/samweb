#!/usr/bin/env python3
"""Capture z.ai's LLM API calls during a chat session and replay them.

Flow:
  1. Connect to samweb on shan via SSH tunnel
  2. Enable network capture
  3. Navigate to z.ai, switch to Agent mode, send a message
  4. Wait for response
  5. Extract LLM API calls from captured network traffic
  6. Try to replay the API call directly (using captured cookies/headers)
"""
import argparse
import json
import os
import sys
import time
import urllib.request
import urllib.error

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "samweb", "scripts"))
from shan_lib.agent import Agent


def unwrap(v):
    s = str(v).strip()
    while len(s) >= 2 and s[0] == '"' and s[-1] == '"':
        s = s[1:-1].replace('\\"', '"').replace('\\\\', '\\')
    try:
        return json.loads(s)
    except Exception:
        return s


def send_chat_message(a, message):
    """Full flow: navigate -> agent mode -> type -> send."""
    print("\n=== Loading cookies ===")
    try:
        a.post("/agent/load-cookies")
        print("  cookies loaded")
    except Exception as e:
        print(f"  load-cookies: {e}")

    print("\n=== Navigating to z.ai ===")
    a.post("/agent/navigate-direct", {"url": "https://chat.z.ai/"})
    time.sleep(5)
    s = a.state()
    print(f"  URL: {s.get('url')}")

    # Check login
    print("\n=== Checking login ===")
    _, v = a.eval(r"""(function(){return JSON.stringify({url:location.href,token:!!localStorage.getItem('token')});})()""")
    info = unwrap(v)
    print(f"  token: {'present' if info.get('token') else 'absent'}")
    if not info.get('token'):
        print("  Not logged in!")
        return False

    # Expand sidebar
    print("\n=== Expanding sidebar ===")
    a.eval(r"""(function(){
        var svgs=document.querySelectorAll('svg.rotate-180,svg[class*="rotate-180"]');
        for(var i=0;i<svgs.length;i++){var svg=svgs[i];var r=svg.getBoundingClientRect();
        if(r.width>0&&r.height>0){var el=svg;while(el&&el!==document.body){
        if(el.tagName==='BUTTON'){el.click();return JSON.stringify({ok:true});}el=el.parentElement;}
        svg.click();return JSON.stringify({ok:true,fallback:'svg'});}}
        return JSON.stringify({ok:false});
    })()""")
    time.sleep(2)

    # Switch to Agent mode
    print("\n=== Switching to Agent mode ===")
    _, v = a.eval(r"""(function(){
        var btns=document.querySelectorAll('button');
        for(var i=0;i<btns.length;i++){var b=btns[i];
        if((b.innerText||'').trim()==='Agent 模式'){var r=b.getBoundingClientRect();
        if(r.width>0&&r.height>0){b.click();return JSON.stringify({ok:true});}}}
        return JSON.stringify({ok:false});
    })()""")
    time.sleep(3)

    # Type message
    print(f"\n=== Typing: {message!r} ===")
    a.post("/agent/type", {"selector": "#chat-input", "text": message, "clear": True}, timeout=15)
    time.sleep(1)

    # Send
    print("\n=== Sending ===")
    a.eval(r"""(function(){var ta=document.getElementById('chat-input');if(ta)ta.focus();return 'ok';})()""")
    time.sleep(0.3)
    _, v = a.eval(r"""(function(){
        var btn=document.querySelector('button.sendMessageButton,button[class*="sendMessageButton"]');
        if(!btn)return JSON.stringify({error:'no btn'});if(btn.disabled)return JSON.stringify({error:'disabled'});
        btn.click();return JSON.stringify({ok:true});
    })()""")
    info = unwrap(v)
    print(f"  result: {info}")
    return info.get("ok", False)


def wait_for_response(a, max_wait=90):
    """Wait for AI response to complete."""
    print(f"\n=== Waiting for response (max {max_wait}s) ===")
    last_text = ""
    stable = 0
    start = time.time()
    while time.time() - start < max_wait:
        _, v = a.eval(r"""(function(){
            var msgs=document.querySelectorAll('[class*="chat-assistant"]');
            var texts=[];
            for(var i=0;i<msgs.length;i++){var t=(msgs[i].innerText||'').trim();
            t=t.replace(/^正在思考\s*跳过\s*/,'').replace(/^思考过程\s*/,'');
            texts.push(t);}
            return JSON.stringify({count:msgs.length,messages:texts});
        })()""")
        info = unwrap(v)
        if not isinstance(info, dict):
            time.sleep(2)
            continue
        msgs = info.get("messages", [])
        current = msgs[-1] if msgs else ""
        if current and current == last_text:
            stable += 1
            if stable >= 4:
                print(f"  Response stable after {int(time.time()-start)}s")
                return current
        else:
            stable = 0
            if current:
                elapsed = int(time.time() - start)
                print(f"  [{elapsed}s] {current[:120]}...")
            last_text = current
        time.sleep(2)
    print(f"  Timeout after {max_wait}s")
    return last_text or None


def extract_llm_api_calls(requests):
    """Find LLM API calls from captured network requests."""
    llm_calls = []
    for req in requests:
        url = req.get("url", "")
        rtype = req.get("resourceType", "")
        method = req.get("method", "")
        # Match XHR/Fetch requests to likely API endpoints
        if rtype not in ("XHR", "Fetch", "WebSocket"):
            continue
        # Skip static resources
        if any(url.endswith(ext) for ext in [".js", ".css", ".png", ".svg", ".woff", ".ico", ".map"]):
            continue
        # Include POST requests or requests to API-like paths
        if method == "POST" or any(kw in url.lower() for kw in [
            "api", "chat", "stream", "completions", "conversation", "message", "agent", "glm"
        ]):
            llm_calls.append(req)
    return llm_calls


def format_request_detail(req):
    """Pretty-print a captured request."""
    lines = []
    lines.append(f"  URL: {req.get('url', '')}")
    lines.append(f"  Method: {req.get('method', '')}  Status: {req.get('status', 0)}  Type: {req.get('resourceType', '')}")
    if req.get('responseContentType'):
        lines.append(f"  Content-Type: {req.get('responseContentType', '')}")
    if req.get('duration', 0) > 0:
        lines.append(f"  Duration: {req['duration']*1000:.0f}ms")
    if req.get('responseSize', 0) > 0:
        lines.append(f"  Response Size: {req['responseSize']} bytes")

    if req.get('cookies'):
        lines.append(f"  Cookies ({len(req['cookies'])}):")
        for c in req['cookies']:
            lines.append(f"    {c['name']} = {c['value'][:60]}")

    if req.get('requestHeaders'):
        lines.append(f"  Request Headers ({len(req['requestHeaders'])}):")
        for h in req['requestHeaders']:
            val = h['value']
            if h['name'].lower() == 'authorization':
                val = val[:40] + "..."
            lines.append(f"    {h['name']}: {val[:100]}")

    if req.get('postData'):
        pd = req['postData']
        lines.append(f"  PostData ({len(pd)} chars):")
        try:
            pd_json = json.loads(pd)
            lines.append(f"    {json.dumps(pd_json, indent=2, ensure_ascii=False)[:600]}")
        except:
            lines.append(f"    {pd[:600]}")

    if req.get('responseHeaders'):
        lines.append(f"  Response Headers ({len(req['responseHeaders'])}):")
        for h in req['responseHeaders']:
            lines.append(f"    {h['name']}: {h['value'][:100]}")

    if req.get('responseBody'):
        rb = req['responseBody']
        lines.append(f"  ResponseBody ({len(rb)} chars):")
        try:
            rb_json = json.loads(rb)
            lines.append(f"    {json.dumps(rb_json, indent=2, ensure_ascii=False)[:1000]}")
        except:
            lines.append(f"    {rb[:600]}")

    return "\n".join(lines)


def try_replay_api(req, all_cookies):
    """Try to directly call the captured API."""
    url = req.get("url", "")
    method = req.get("method", "POST")
    post_data = req.get("postData", "")
    if not url:
        print("  No URL to replay")
        return

    print(f"\n{'='*60}")
    print(f"REPLAYING API CALL")
    print(f"{'='*60}")
    print(f"  URL: {url}")
    print(f"  Method: {method}")

    # Build cookie string
    cookie_str = "; ".join(f"{c['name']}={c['value']}" for c in all_cookies)

    # Build headers
    headers = {}
    for h in req.get("requestHeaders", []):
        if h["name"].lower() == "cookie":
            continue
        headers[h["name"]] = h["value"]
    headers["Cookie"] = cookie_str
    if not headers.get("Content-Type") and post_data:
        headers["Content-Type"] = "application/json"

    print(f"  Headers:")
    for k, v in headers.items():
        if k.lower() == "authorization":
            print(f"    {k}: {v[:40]}...")
        elif k.lower() == "cookie":
            print(f"    {k}: {v[:80]}...")
        else:
            print(f"    {k}: {v[:100]}")

    if post_data:
        print(f"  Body: {post_data[:300]}...")

    print(f"\n  Sending...")
    try:
        data = post_data.encode("utf-8") if post_data else None
        r = urllib.request.Request(url, data=data, method=method, headers=headers)
        with urllib.request.urlopen(r, timeout=60) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            print(f"  Status: {resp.status}")
            print(f"  Length: {len(body)} chars")
            print(f"\n  Response (first 1500 chars):")
            print(f"  {body[:1500]}")
            return body
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        print(f"  HTTP Error {e.code}: {body[:500]}")
        return None
    except Exception as e:
        print(f"  Error: {e}")
        return None


def main():
    parser = argparse.ArgumentParser(description="Capture z.ai LLM API calls")
    parser.add_argument("--message", "-m", default="你好，请简短回复",
                        help="Message to send")
    args = parser.parse_args()

    a = Agent(verbose=False)
    try:
        print("=== Health check ===")
        r = a.get("/agent/health")
        print(f"  status: {r.get('status')}")

        # Enable network capture BEFORE anything
        print("\n=== Enabling network capture ===")
        a.post("/agent/network/enable")
        a.post("/agent/network/clear")
        print("  Network capture enabled, buffer cleared")

        # Send the message
        if not send_chat_message(a, args.message):
            print("\n>>> Failed to send message")
            return 1

        # Wait for response
        response = wait_for_response(a, max_wait=90)
        print(f"\n>>> AI Response: {(response or '(none)')[:200]}")
        time.sleep(2)

        # Get all captured requests
        print(f"\n{'='*60}")
        print(f"CAPTURED NETWORK TRAFFIC")
        print(f"{'='*60}")

        all_reqs = a.get("/agent/network/requests")
        total = all_reqs.get("total", 0)
        print(f"\nTotal requests: {total}")

        # Summary table
        print(f"\n--- All Requests ---")
        for req in all_reqs.get("requests", []):
            url = req.get("url", "")[:90]
            m = req.get("method", "")
            s = req.get("status", 0)
            rt = req.get("resourceType", "")
            flags = []
            if req.get("responseBody"): flags.append("BODY")
            if req.get("postData"): flags.append("POST_DATA")
            if req.get("cookies"): flags.append(f"{len(req['cookies'])}CK")
            flag_str = ",".join(flags) if flags else ""
            print(f"  [{m:6s}] {s:4d} {rt:10s} {flag_str:15s} {url}")

        # Extract LLM-related calls
        llm_calls = extract_llm_api_calls(all_reqs.get("requests", []))

        print(f"\n{'='*60}")
        print(f"LLM API CALLS ({len(llm_calls)} found)")
        print(f"{'='*60}")

        for i, call in enumerate(llm_calls):
            print(f"\n--- LLM API Call #{i+1} ---")
            print(format_request_detail(call))

        # Get all cookies
        cookies_result = a.get("/agent/cookies")
        all_cookies = cookies_result.get("cookies", [])
        print(f"\nTotal browser cookies: {cookies_result.get('count', 0)}")

        # Replay the best candidate
        if llm_calls:
            replay_target = None
            for call in llm_calls:
                if call.get("postData") and "chat" in call.get("url", "").lower():
                    replay_target = call
                    break
            if not replay_target:
                for call in llm_calls:
                    if call.get("postData"):
                        replay_target = call
                        break
            if not replay_target:
                replay_target = llm_calls[0]

            try_replay_api(replay_target, all_cookies)
        else:
            print("\n  No obvious LLM API calls found.")
            print("  Showing all non-static requests:")
            for req in all_reqs.get("requests", []):
                rt = req.get("resourceType", "")
                if rt in ("XHR", "Fetch"):
                    print(f"\n--- {rt} ---")
                    print(format_request_detail(req))

        # Save full results
        result_data = {
            "total_requests": total,
            "llm_calls": llm_calls,
            "all_requests": all_reqs.get("requests", []),
            "cookies": all_cookies,
        }
        out_path = "/home/z/my-project/download/llm_api_capture.json"
        os.makedirs(os.path.dirname(out_path), exist_ok=True)
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(result_data, f, indent=2, ensure_ascii=False)
        print(f"\n>>> Full capture saved to {out_path}")

        a.post("/agent/network/disable")
        return 0

    finally:
        a.close()


if __name__ == "__main__":
    sys.exit(main())
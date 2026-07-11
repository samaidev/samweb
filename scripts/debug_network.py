#!/usr/bin/env python3
"""Debug: capture z.ai network requests to find the API URL."""
import argparse
import asyncio
import json
import sys
import time

import aiohttp


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args()
    base = f"http://127.0.0.1:{args.port}"

    async with aiohttp.ClientSession() as session:
        # Enable Network capture
        print("[1] Enabling Network capture...")
        resp = await session.post(f"{base}/agent/network/enable",
                                  json={}, timeout=aiohttp.ClientTimeout(total=10))
        print(f"   {await resp.json()}")

        # Type + send a message
        print("\n[2] Typing message...")
        msg = "Hi"
        r = await session.post(f"{base}/agent/cdp-eval",
                               json={"script": f"""(function(){{
            var el = document.querySelector('#chat-input,textarea[class*="chat-input"],div[contenteditable="true"]');
            if (!el) return 'no_input';
            if (el.contentEditable === 'true') {{
                el.focus();
                document.execCommand('insertText', false, {json.dumps(msg)});
                return 'typed_ce';
            }}
            var proto = el.tagName === 'TEXTAREA' ?
                window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
            var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
            setter.call(el, {json.dumps(msg)});
            el.dispatchEvent(new InputEvent('input', {{bubbles: true, data: {json.dumps(msg)}}}));
            return 'typed_value';
        }})()"""},
                               timeout=aiohttp.ClientTimeout(total=10))
        print(f"   {await r.json()}")
        await asyncio.sleep(1)

        print("\n[3] Clicking send...")
        r = await session.post(f"{base}/agent/cdp-eval",
                               json={"script": """(function(){
            var btn = document.querySelector('button.sendMessageButton');
            if (btn && !btn.disabled) { btn.click(); return 'sent'; }
            return 'no_btn';
        })()"""},
                               timeout=aiohttp.ClientTimeout(total=10))
        print(f"   {await r.json()}")

        # Wait 15 seconds for network activity
        print("\n[4] Waiting 15s for network activity...")
        await asyncio.sleep(15)

        # Get captured requests
        print("\n[5] Getting captured requests...")
        resp = await session.get(f"{base}/agent/network/requests",
                                 timeout=aiohttp.ClientTimeout(total=15))
        data = await resp.json()
        requests = data if isinstance(data, list) else data.get("requests", [])

        print(f"\n   Total captured requests: {len(requests)}")
        # Show XHR/Fetch/Document requests (skip images/scripts)
        interesting = [r for r in requests
                       if r.get("resourceType") in ("XHR", "Fetch", "Document", "WebSocket")
                       or "api" in r.get("url", "").lower()
                       or "chat" in r.get("url", "").lower()
                       or "z.ai" in r.get("url", "")]
        print(f"   Interesting (XHR/Fetch/api/chat/z.ai): {len(interesting)}")
        for r in interesting[:30]:
            url = r.get("url", "")
            method = r.get("method", "")
            status = r.get("status", 0)
            rtype = r.get("resourceType", "")
            body_len = len(r.get("responseBody", ""))
            print(f"      [{rtype}] {method} {status} {url[:100]}")
            if body_len:
                body = r.get("responseBody", "")
                print(f"         body ({body_len} chars): {body[:300]!r}")

        # Save full data
        with open("C:/samweb/network_capture.json", "w") as f:
            json.dump(requests, f, indent=2, ensure_ascii=False)
        print(f"\n   saved full capture to C:/samweb/network_capture.json")


if __name__ == "__main__":
    asyncio.run(main())

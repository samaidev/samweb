#!/usr/bin/env python3
"""Capture z.ai Agent streaming response via Fetch domain.

This script:
1. Enables Fetch capture on a z.ai tab worker
2. Sends a message to z.ai
3. Polls for streaming chunks (from text/event-stream responses)
4. Saves the captured stream content + screenshots as proof

Usage:
    python capture_zai_stream.py --port <agent_port> [--message "Hello"]
"""
import argparse
import asyncio
import json
import sys
import time

import aiohttp


async def cdp_eval(session, base, script, timeout=15):
    try:
        resp = await session.post(f"{base}/agent/cdp-eval",
                                  json={"script": script},
                                  timeout=aiohttp.ClientTimeout(total=timeout))
        data = await resp.json()
        v = data.get("value", data)
        if isinstance(v, dict) and "value" in v:
            v = v["value"]
        if isinstance(v, str):
            try:
                v = json.loads(v)
            except Exception:
                pass
        return v
    except Exception as e:
        print(f"  eval error: {e}")
        return None


async def fetch_enable(session, base, filter_str):
    resp = await session.post(f"{base}/agent/fetch-enable",
                              json={"filter": filter_str},
                              timeout=aiohttp.ClientTimeout(total=15))
    return await resp.json()


async def fetch_poll(session, base):
    await session.post(f"{base}/agent/fetch-poll",
                       json={}, timeout=aiohttp.ClientTimeout(total=10))


async def fetch_chunks(session, base):
    resp = await session.get(f"{base}/agent/fetch-chunks",
                             timeout=aiohttp.ClientTimeout(total=10))
    return await resp.json()


async def fetch_paused(session, base):
    resp = await session.get(f"{base}/agent/fetch-paused",
                             timeout=aiohttp.ClientTimeout(total=10))
    return await resp.json()


async def fetch_finish(session, base):
    await session.post(f"{base}/agent/fetch-finish",
                       json={}, timeout=aiohttp.ClientTimeout(total=15))


async def screenshot(session, base):
    resp = await session.get(f"{base}/agent/screenshot-trusted",
                             timeout=aiohttp.ClientTimeout(total=30))
    return await resp.read()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--message", default="Hello! Please respond with a short greeting.")
    parser.add_argument("--filter", default="chat.z.ai")
    parser.add_argument("--max-wait", type=int, default=60)
    args = parser.parse_args()

    base = f"http://127.0.0.1:{args.port}"
    print(f"=== z.ai Fetch Capture Test ===")
    print(f"Agent API: {base}")
    print(f"Filter: {args.filter!r}")
    print(f"Message: {args.message!r}")
    print()

    async with aiohttp.ClientSession() as session:
        # 1. Enable Fetch capture
        print("[1] Enabling Fetch capture...")
        r = await fetch_enable(session, base, args.filter)
        print(f"    {r}")

        # 2. Switch to Agent mode
        print("\n[2] Switching to Agent mode...")
        r = await cdp_eval(session, base, """(function(){
            var all = document.querySelectorAll('div');
            for (var i = 0; i < all.length; i++) {
                var el = all[i];
                var dt = '';
                for (var j = 0; j < el.childNodes.length; j++) {
                    var n = el.childNodes[j];
                    if (n.nodeType === 3) dt += n.nodeValue;
                }
                dt = dt.trim();
                if (dt === 'Agent \u6a21\u5f0f' || dt === 'Agent Mode') {
                    var r = el.getBoundingClientRect();
                    if (r.width > 0 && r.height > 0) {
                        el.click();
                        return 'clicked';
                    }
                }
            }
            return 'not_found';
        })()""")
        print(f"    {r}")
        await asyncio.sleep(5)

        # 3. Wait for chat input
        print("\n[3] Waiting for chat input...")
        for i in range(10):
            r = await cdp_eval(session, base, """(function(){
                var el = document.querySelector('#chat-input,textarea[class*="chat-input"],div[contenteditable="true"]');
                return el && el.getBoundingClientRect().width > 0 ? 'ready' : 'not_ready';
            })()""")
            if r == "ready":
                print(f"    chat input ready (attempt {i+1})")
                break
            await asyncio.sleep(2)
        else:
            print("    chat input not ready, trying anyway...")

        # 4. Take BEFORE screenshot
        print("\n[4] Taking BEFORE screenshot...")
        png = await screenshot(session, base)
        with open("C:/samweb/stream_before.png", "wb") as f:
            f.write(png)
        print(f"    saved ({len(png)} bytes)")

        # 5. Type + send message
        print("\n[5] Typing + sending message...")
        msg = args.message
        r = await cdp_eval(session, base, f"""(function(){{
            var el = document.querySelector('#chat-input,textarea[class*="chat-input"],div[contenteditable="true"]');
            if (!el) return 'no_input';
            if (el.contentEditable === 'true') {{
                el.focus();
                document.execCommand('insertText', false, {json.dumps(msg)});
                return 'typed_ce';
            }}
            var proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
            var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
            setter.call(el, {json.dumps(msg)});
            el.dispatchEvent(new InputEvent('input', {{bubbles: true, data: {json.dumps(msg)}}}));
            return 'typed_value';
        }})()""")
        print(f"    type: {r}")
        await asyncio.sleep(1)
        r = await cdp_eval(session, base, """(function(){
            var btn = document.querySelector('button.sendMessageButton');
            if (btn && !btn.disabled) { btn.click(); return 'sent'; }
            return 'no_btn';
        })()""")
        print(f"    send: {r}")

        # 6. Poll for streaming chunks
        print(f"\n[6] Polling for streaming chunks (max {args.max_wait}s)...")
        all_chunks = []
        stream_chunks = []
        start = time.time()
        last_screenshot = 0
        while time.time() - start < args.max_wait:
            await fetch_poll(session, base)
            chunks = await fetch_chunks(session, base)
            paused = await fetch_paused(session, base)
            chunks = chunks if isinstance(chunks, list) else []
            paused = paused if isinstance(paused, list) else []

            if chunks:
                all_chunks.extend(chunks)
                # Filter for streaming chunks (from event-stream URLs)
                for c in chunks:
                    url = c.get("url", "")
                    chunk_text = c.get("chunk", "")
                    # Streaming endpoints have event-stream content-type,
                    # which we detect via URL pattern
                    is_stream = ("workspaces/up" in url or
                                 "event-stream" in url or
                                 "completion" in url or
                                 "stream" in url.lower())
                    if is_stream and chunk_text:
                        stream_chunks.append(c)
                        elapsed = int(time.time() - start)
                        print(f"   [{elapsed}s] STREAM CHUNK ({len(chunk_text)} chars): {chunk_text[:200]!r}")

            # Take a screenshot every 10s to show progress
            elapsed = int(time.time() - start)
            if elapsed >= last_screenshot + 10 and stream_chunks:
                png = await screenshot(session, base)
                fname = f"C:/samweb/stream_during_{elapsed}s.png"
                with open(fname, "wb") as f:
                    f.write(png)
                print(f"   [{elapsed}s] screenshot saved: {fname} ({len(png)} bytes)")
                last_screenshot = elapsed

            await asyncio.sleep(2)

        # 7. Final state
        print(f"\n[7] Final state:")
        print(f"    Total chunks: {len(all_chunks)}")
        print(f"    Stream chunks: {len(stream_chunks)}")
        if stream_chunks:
            full_stream = "".join(c.get("chunk", "") for c in stream_chunks)
            print(f"    Full stream length: {len(full_stream)} chars")
            print(f"    Stream preview:")
            print("    " + full_stream[:1000].replace("\n", "\n    "))

        # 8. Take AFTER screenshot
        print(f"\n[8] Taking AFTER screenshot...")
        png = await screenshot(session, base)
        with open("C:/samweb/stream_after.png", "wb") as f:
            f.write(png)
        print(f"    saved ({len(png)} bytes)")

        # 9. Save all chunks to file
        with open("C:/samweb/stream_chunks.json", "w") as f:
            json.dump({
                "all_chunks": all_chunks,
                "stream_chunks": stream_chunks,
                "message": args.message,
                "timestamp": time.time(),
            }, f, indent=2, ensure_ascii=False)
        print(f"\n[9] Saved all chunks to C:/samweb/stream_chunks.json")

        # 10. Finish (resume paused requests)
        print(f"\n[10] Finishing Fetch capture...")
        await fetch_finish(session, base)
        print(f"     done")

        print(f"\n=== DONE ===")
        print(f"Captured {len(stream_chunks)} stream chunks from z.ai Agent response")


if __name__ == "__main__":
    asyncio.run(main())

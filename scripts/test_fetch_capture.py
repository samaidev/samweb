#!/usr/bin/env python3
"""Test Fetch domain capture on a z.ai tab worker.

Sends a short message to z.ai and captures the streaming response via
the Fetch domain. Verifies that chunks arrive even when z.ai's JS thread
is busy (during the initial page load + API call).

Usage:
    python test_fetch_capture.py --port 50683
"""
import argparse
import asyncio
import json
import sys
import time

import aiohttp

sys.path.insert(0, "/home/z/my-project/scripts")


async def cdp_eval(session, base, script, timeout=10):
    """Run a CDP eval and return the value."""
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
                       json={},
                       timeout=aiohttp.ClientTimeout(total=10))


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
                       json={},
                       timeout=aiohttp.ClientTimeout(total=15))


async def screenshot(session, base, full_page=False):
    resp = await session.get(f"{base}/agent/screenshot-trusted?fullPage={str(full_page).lower()}",
                             timeout=aiohttp.ClientTimeout(total=30))
    return await resp.read()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True, help="agent API port of tab worker")
    parser.add_argument("--message", default="Hello! Please respond with just OK to confirm you got this.",
                        help="message to send to z.ai")
    parser.add_argument("--filter", default="chat.z.ai", help="URL filter for Fetch capture")
    parser.add_argument("--max-wait", type=int, default=120, help="max seconds to wait for response")
    args = parser.parse_args()

    base = f"http://127.0.0.1:{args.port}"
    print(f"Testing Fetch capture on {base}")
    print(f"Filter: {args.filter!r}")
    print(f"Message: {args.message!r}")
    print()

    async with aiohttp.ClientSession() as session:
        # Step 0: take initial screenshot
        print("[0] Taking initial screenshot...")
        png = await screenshot(session, base)
        with open(f"C:/samweb/fetch_test_initial.png", "wb") as f:
            f.write(png)
        print(f"   saved initial screenshot ({len(png)} bytes)")

        # Step 1: Switch to Agent mode (idempotent)
        print("\n[1] Switching to Agent mode...")
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
        print(f"   result: {r}")
        # Wait longer for Agent mode UI to load
        await asyncio.sleep(8)

        # Verify the chat input is available before proceeding
        for ready_attempt in range(10):
            check = await cdp_eval(session, base, """(function(){
                var input = document.querySelector('#chat-input,textarea[class*="chat-input"],div[contenteditable="true"]');
                var hasInput = input && input.getBoundingClientRect().width > 0;
                return JSON.stringify({has_input: hasInput});
            })()""")
            if isinstance(check, str):
                try: check = json.loads(check)
                except: pass
            if isinstance(check, dict) and check.get("has_input"):
                print(f"   chat input ready (attempt {ready_attempt+1})")
                break
            print(f"   waiting for chat input (attempt {ready_attempt+1}/10)...")
            await asyncio.sleep(2)

        # Step 2: Enable Fetch capture BEFORE sending
        print(f"\n[2] Enabling Fetch capture (filter={args.filter!r})...")
        r = await fetch_enable(session, base, args.filter)
        print(f"   result: {r}")

        # Step 3: Type the message
        print(f"\n[3] Typing message...")
        r = await cdp_eval(session, base, f"""(function(){{
            var el = document.querySelector('#chat-input,textarea[class*="chat-input"],div[contenteditable="true"]');
            if (!el) return 'no_input';
            if (el.contentEditable === 'true') {{
                el.focus();
                document.execCommand('insertText', false, {json.dumps(args.message)});
                return 'typed_ce';
            }}
            var proto = el.tagName === 'TEXTAREA' ?
                window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
            var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
            setter.call(el, {json.dumps(args.message)});
            el.dispatchEvent(new InputEvent('input', {{bubbles: true, data: {json.dumps(args.message)}}}));
            return 'typed_value';
        }})()""")
        print(f"   result: {r}")
        await asyncio.sleep(1)

        # Step 4: Click send
        print(f"\n[4] Clicking send...")
        r = await cdp_eval(session, base, """(function(){
            var sels = [
                'button.sendMessageButton',
                'button[class*="sendMessageButton"]',
                'button[class*="send-button"]',
                'button[type="submit"]',
                'button[aria-label*="Send"]',
                'button[aria-label*="\u53d1\u9001"]'
            ];
            for (var i = 0; i < sels.length; i++) {
                var btn = document.querySelector(sels[i]);
                if (btn && !btn.disabled) { btn.click(); return 'sent:' + sels[i]; }
            }
            var input = document.querySelector('#chat-input,textarea,div[contenteditable="true"]');
            if (input) {
                var parent = input.closest('form,div[class*="input"],div[class*="chat"]');
                if (parent) {
                    var btns = parent.querySelectorAll('button');
                    for (var j = 0; j < btns.length; j++) {
                        if (!btns[j].disabled && btns[j].getBoundingClientRect().width > 0) {
                            btns[j].click(); return 'sent:fallback_' + j;
                        }
                    }
                }
            }
            return 'no_send_btn';
        })()""")
        print(f"   result: {r}")

        # Step 5: Poll for Fetch chunks
        print(f"\n[5] Polling for Fetch chunks (max {args.max_wait}s)...")
        all_chunks = []
        start = time.time()
        last_paused_count = 0
        poll_interval = 2  # seconds
        while time.time() - start < args.max_wait:
            await fetch_poll(session, base)
            chunks = await fetch_chunks(session, base)
            paused = await fetch_paused(session, base)
            paused_count = len(paused) if isinstance(paused, list) else 0
            chunks_count = len(chunks) if isinstance(chunks, list) else 0

            elapsed = int(time.time() - start)
            if chunks_count > 0 or paused_count != last_paused_count:
                print(f"   [{elapsed}s] paused={paused_count}, new_chunks={chunks_count}")
                last_paused_count = paused_count
            else:
                # Print heartbeat every 10s
                if elapsed % 10 == 0 and elapsed > 0:
                    print(f"   [{elapsed}s] paused={paused_count}, new_chunks={chunks_count} (waiting...)")

            if chunks_count > 0:
                all_chunks.extend(chunks)
                for c in chunks[:5]:
                    chunk_text = c.get("chunk", "")
                    print(f"      reqId={c.get('requestId', '')[:24]}")
                    print(f"      url={c.get('url', '')[:80]}")
                    print(f"      offset={c.get('offset', 0)} chunk_len={len(chunk_text)}")
                    print(f"      chunk_preview={chunk_text[:300]!r}")
                    print()

            # If we have a chunk with substantial content, take a screenshot
            # to show what z.ai looks like during the response.
            if len(all_chunks) >= 1 and elapsed == int(start - start + 4):  # ~4s in
                png = await screenshot(session, base)
                with open(f"C:/samweb/fetch_test_during.png", "wb") as f:
                    f.write(png)
                print(f"   [screenshot] saved during-response screenshot ({len(png)} bytes)")

            await asyncio.sleep(poll_interval)

        # Step 6: Final state
        print(f"\n[6] Final state:")
        print(f"   total chunks collected: {len(all_chunks)}")
        if all_chunks:
            full_body = "".join(c.get("chunk", "") for c in all_chunks)
            print(f"   total body length: {len(full_body)} chars")
            print(f"   body preview (first 1000 chars):")
            print("   " + full_body[:1000].replace("\n", "\n   "))

        # Step 7: Take final screenshot
        print(f"\n[7] Taking final screenshot...")
        png = await screenshot(session, base)
        with open(f"C:/samweb/fetch_test_final.png", "wb") as f:
            f.write(png)
        print(f"   saved final screenshot ({len(png)} bytes)")

        # Step 8: Finish (resume paused requests)
        print(f"\n[8] Finishing Fetch capture (resume paused requests)...")
        await fetch_finish(session, base)
        print(f"   done")

        # Save raw chunks to a file for debugging
        if all_chunks:
            with open(f"C:/samweb/fetch_chunks.json", "w") as f:
                json.dump(all_chunks, f, indent=2, ensure_ascii=False)
            print(f"   saved raw chunks to fetch_chunks.json")


if __name__ == "__main__":
    asyncio.run(main())

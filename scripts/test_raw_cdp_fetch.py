#!/usr/bin/env python3
"""Direct CDP test: bypass the Go client and use raw CDP WebSocket to
verify that Fetch.enable + Fetch.requestPaused actually works on this
WebView2 instance.

This isolates the issue: if raw CDP works, the bug is in our Go code.
If raw CDP doesn't work, Fetch domain is not properly supported.
"""
import argparse
import asyncio
import json
import sys
import time

import aiohttp
import websockets


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--cdp-port", type=int, required=True)
    parser.add_argument("--filter", default="*chat.z.ai*",
                        help="urlMatch glob pattern (use * as wildcard)")
    args = parser.parse_args()

    # 1) Discover the page target
    async with aiohttp.ClientSession() as s:
        resp = await s.get(f"http://127.0.0.1:{args.cdp_port}/json")
        targets = await resp.json()
    page = next((t for t in targets if t.get("type") == "page"), None)
    if not page:
        print("no page target found")
        return
    ws_url = page["webSocketDebuggerUrl"]
    print(f"[1] Page target: {page['url']}")
    print(f"    WS URL: {ws_url}")

    # 2) Connect to the page's CDP WebSocket
    ws = await websockets.connect(ws_url, max_size=20 * 1024 * 1024)
    print(f"[2] Connected to CDP WebSocket")

    next_id = 1
    pending = {}
    paused_events = []
    network_responses = []
    all_events_count = 0

    async def reader_loop():
        """Background task that reads all incoming CDP messages and
        dispatches events. Runs until ws is closed."""
        nonlocal all_events_count
        print(f"   [reader] started")
        msg_count = 0
        try:
            async for raw in ws:
                msg_count += 1
                if msg_count <= 5:
                    print(f"   [reader] received msg #{msg_count}: {raw[:200]}")
                try:
                    data = json.loads(raw)
                except Exception:
                    continue
                if "id" in data:
                    # Response to a command — wake up the sender.
                    if data["id"] in pending:
                        pending[data["id"]].set_result(data)
                elif "method" in data:
                    all_events_count += 1
                    if all_events_count <= 10:
                        print(f"   [reader] event #{all_events_count}: {data['method']}")
                    await handle_event(data["method"], data.get("params", {}))
        except websockets.exceptions.ConnectionClosed as e:
            print(f"   [reader] connection closed: {e}")
        except Exception as e:
            print(f"   [reader] error: {e}")
        print(f"   [reader] exiting, total msgs received: {msg_count}")

    async def handle_event(method, params):
        if method == "Fetch.requestPaused":
            paused_events.append(params)
            url = params.get("request", {}).get("url", "")
            print(f"   [EVENT] Fetch.requestPaused: {url[:120]}")
            # Try to get the response body
            err, result = await send_cmd("Fetch.getResponseBody", {"requestId": params["requestId"]})
            if err:
                print(f"           getResponseBody error: {err}")
            else:
                body = result.get("body", "")
                print(f"           body len={len(body)} base64={result.get('base64Encoded', False)}")
                if body and not result.get("base64Encoded"):
                    print(f"           body preview: {body[:500]!r}")
            # Continue the request so the page isn't blocked
            await send_cmd("Fetch.continueResponse", {"requestId": params["requestId"]})
        elif method == "Network.responseReceived":
            url = params.get("response", {}).get("url", "")
            rtype = params.get("type", "")
            if rtype in ("XHR", "Fetch", "WebSocket") or "api" in url.lower():
                network_responses.append(params)
                print(f"   [EVENT] Network.responseReceived [{rtype}] {url[:120]}")

    async def send_cmd(method, params=None, timeout=15):
        nonlocal next_id
        cid = next_id
        next_id += 1
        msg = {"id": cid, "method": method}
        if params:
            msg["params"] = params
        fut = asyncio.get_event_loop().create_future()
        pending[cid] = fut
        await ws.send(json.dumps(msg))
        try:
            data = await asyncio.wait_for(fut, timeout=timeout)
        except asyncio.TimeoutError:
            del pending[cid]
            return {"message": "timeout"}, None
        if "error" in data:
            return data["error"], None
        return None, data.get("result")

    # Start the background reader
    reader_task = asyncio.create_task(reader_loop())

    # 3) Enable Network and Fetch domains
    print(f"\n[3] Enabling Network domain...")
    err, _ = await send_cmd("Network.enable", {"maxTotalBufferSize": 50 * 1024 * 1024})
    print(f"    Network.enable: err={err}")

    print(f"\n[4] Enabling Fetch domain with urlMatch={args.filter!r}...")
    fetch_params = {
        "patterns": [
            {
                "urlMatch": args.filter,
                "requestStage": "Response",
            }
        ]
    }
    err, _ = await send_cmd("Fetch.enable", fetch_params)
    print(f"    Fetch.enable: err={err}")
    if err:
        print(f"    ERROR — Fetch.enable failed: {err}")
        # Try without filter
        print(f"    Trying with NO filter...")
        err, _ = await send_cmd("Fetch.enable", {"patterns": [{"requestStage": "Response"}]})
        print(f"    Fetch.enable (no filter): err={err}")

    # 4) Wait 30 seconds for events
    print(f"\n[5] Waiting 30s for events (try interacting with the page)...")
    # Also reload the page to trigger network activity
    print(f"   triggering a Page.reload to force network events...")
    err, _ = await send_cmd("Page.reload", {})
    print(f"   Page.reload: err={err}")
    start = time.time()
    last_count = 0
    while time.time() - start < 30:
        await asyncio.sleep(2)
        if all_events_count != last_count:
            print(f"   [{int(time.time()-start)}s] total events so far: {all_events_count}")
            last_count = all_events_count

    print(f"\n[6] Final state:")
    print(f"    Total CDP events received: {all_events_count}")
    print(f"    Fetch.requestPaused events: {len(paused_events)}")
    print(f"    Network.responseReceived (XHR/Fetch): {len(network_responses)}")
    for p in paused_events[:5]:
        url = p.get("request", {}).get("url", "")
        print(f"       paused URL: {url[:120]}")
    for n in network_responses[:10]:
        url = n.get("response", {}).get("url", "")
        print(f"       network URL: {url[:120]}")

    reader_task.cancel()
    await ws.close()
    print(f"\n[7] Done")


if __name__ == "__main__":
    asyncio.run(main())

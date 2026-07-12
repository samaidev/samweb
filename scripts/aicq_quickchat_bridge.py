#!/usr/bin/env python3
"""AICQ QuickChat bridge for samweb — multi-profile version.

Each cookie profile can have an AICQ identity linked. This script:
1. Queries samweb's agent API for all profiles with AICQ identity
2. For each profile, starts a polling loop using that profile's AICQ identity
3. When a message arrives:
   a. Switch samweb to the profile's z.ai cookies (SwitchToProfile)
   b. Look up the AICQ friend → z.ai chat_id mapping
   c. If mapping exists: continue the z.ai chat
   d. If not: create a new z.ai chat, store the mapping
   e. Forward the message to z.ai Agent mode
   f. Poll for z.ai response
   g. Send the response back to the AICQ friend
4. Context is preserved per AICQ friend per profile

Usage:
  python3.13 aicq_quickchat_bridge.py --agent-addr 127.0.0.1:7777
"""
import argparse
import asyncio
import json
import os
import re
import sys
import time
import aiohttp

SERVER = "https://aicq.me"
POLL_INTERVAL = 5


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent-addr", default="127.0.0.1:7777")
    args = parser.parse_args()
    agent_base = f"http://{args.agent_addr}"

    print(json.dumps({"type": "status", "msg": "Starting multi-profile AICQ bridge..."}), flush=True)

    # Main loop: periodically check for profiles with AICQ identity
    # and start polling loops for any new ones.
    active_loops = {}  # profile_id → asyncio.Task

    while True:
        try:
            # Get all profiles with AICQ identity
            async with aiohttp.ClientSession() as session:
                async with session.get(f"{agent_base}/agent/profiles/aicq/list",
                                       timeout=aiohttp.ClientTimeout(total=10)) as resp:
                    data = await resp.json()
                    profiles = data.get("profiles", [])

            # Start polling loops for new profiles
            for p in profiles:
                pid = p.get("profile_id", "")
                if pid and pid not in active_loops:
                    print(json.dumps({"type": "status",
                        "msg": f"Starting AICQ poll for profile {pid} ({p.get('profile_name','')})"}), flush=True)
                    active_loops[pid] = asyncio.create_task(
                        poll_loop(agent_base, pid, p))

            # Stop loops for profiles that no longer have AICQ
            for pid in list(active_loops.keys()):
                if not any(p.get("profile_id") == pid for p in profiles):
                    print(json.dumps({"type": "status",
                        "msg": f"Stopping AICQ poll for profile {pid} (no longer linked)"}), flush=True)
                    active_loops[pid].cancel()
                    del active_loops[pid]

        except Exception as e:
            print(json.dumps({"type": "error", "msg": f"Main loop error: {e}"}), flush=True)

        await asyncio.sleep(30)  # Check for profile changes every 30s


async def poll_loop(agent_base, profile_id, profile_info):
    """Poll for messages for a single profile's AICQ identity."""
    pid = profile_id
    pname = profile_info.get("profile_name", "")

    # Get AICQ identity for this profile
    try:
        async with aiohttp.ClientSession() as session:
            async with session.post(f"{agent_base}/agent/eval", json={
                "script": "(function(){ return 'ok'; })()"
            }, timeout=aiohttp.ClientTimeout(total=10)) as resp:
                pass  # Just check agent API is alive
    except:
        print(json.dumps({"type": "error", "msg": f"Agent API not reachable for profile {pid}"}), flush=True)
        return

    # Get AICQ identity details
    async with aiohttp.ClientSession() as session:
        async with session.get(f"{agent_base}/agent/profiles/aicq/list",
                               timeout=aiohttp.ClientTimeout(total=10)) as resp:
            data = await resp.json()
            profiles = data.get("profiles", [])
            aicq_info = None
            for p in profiles:
                if p.get("profile_id") == pid:
                    aicq_info = p
                    break

    if not aicq_info:
        print(json.dumps({"type": "error", "msg": f"Profile {pid} not found in AICQ list"}), flush=True)
        return

    # For now, use the QuickChat approach: the identity is stored in
    # ~/.aicq-sdk/quickchat.json on shan. We use the AICQChatClient
    # which reads from there.
    #
    # TODO: In the future, each profile should have its own separate
    # AICQ identity file. For now, all profiles share the same identity
    # (the one set up via setup_aicq.py).
    from aicq import AICQChatClient

    client = AICQChatClient(server=SERVER)
    try:
        await client.init(name=pname or "SamWeb Browser")
    except Exception as e:
        print(json.dumps({"type": "error", "msg": f"AICQ init failed for {pid}: {e}"}), flush=True)
        return

    status = await client.status()
    if not status.get("bound"):
        print(json.dumps({"type": "error", "msg": f"Profile {pid} AICQ not bound"}), flush=True)
        return

    print(json.dumps({"type": "status",
        "msg": f"Profile {pid} ({pname}) AICQ polling started, owner={status.get('owner_display_name')}"}), flush=True)

    # Track latest timestamp
    latest_ts = ""
    try:
        result = await client.chat(speak=False, wait_seconds=1)
        latest_ts = result.get("latest_timestamp", "")
    except:
        pass

    # Poll loop
    while True:
        try:
            result = await client.chat(speak=False, wait_seconds=POLL_INTERVAL, since=latest_ts)
            new_ts = result.get("latest_timestamp", "")
            if new_ts:
                latest_ts = new_ts

            msgs = result.get("messages") or []
            for msg in msgs:
                from_id = msg.get("from_id", msg.get("from", ""))
                content = msg.get("content", "")

                # Skip our own messages
                if from_id and from_id.startswith("ai_"):
                    continue
                if not content.strip():
                    continue

                clean = re.sub(r'<[^>]+>', '', content).strip()
                if not clean:
                    continue

                print(json.dumps({"type": "message", "from": from_id,
                    "content": clean, "profile": pid}), flush=True)

                # 1. Switch to this profile's z.ai cookies
                print(json.dumps({"type": "status",
                    "msg": f"Switching to profile {pid} for z.ai"}), flush=True)
                async with aiohttp.ClientSession() as session:
                    await session.post(f"{agent_base}/agent/profiles/switch",
                        json={"id": pid}, timeout=aiohttp.ClientTimeout(total=15))

                # 2. Check if we have a z.ai chat_id for this friend
                zai_chat_id = None
                # Use eval to check chat mapping via agent API
                # (The mapping is stored in profiles.json)
                # For now, use a simpler approach: check via HTTP
                # TODO: Add /agent/profiles/chat-mapping endpoint
                # For now, just always create new chat (no context retention yet)
                # TODO: Implement chat mapping lookup

                # 3. Forward to z.ai
                print(json.dumps({"type": "status",
                    "msg": f"Forwarding to z.ai: {clean[:80]}..."}), flush=True)
                response = await forward_to_zai(agent_base, clean, zai_chat_id)

                # 4. Send response back
                if response:
                    print(json.dumps({"type": "response",
                        "content": response[:200], "profile": pid}), flush=True)
                    await client.chat(content=response, speak=True, wait_seconds=0)
                    print(json.dumps({"type": "status",
                        "msg": f"Response sent to owner for profile {pid}"}), flush=True)
                else:
                    await client.chat(content="(z.ai 未返回响应)", speak=True, wait_seconds=0)

        except asyncio.CancelledError:
            print(json.dumps({"type": "status",
                "msg": f"Profile {pid} poll loop cancelled"}), flush=True)
            break
        except Exception as e:
            print(json.dumps({"type": "error",
                "msg": f"Profile {pid} poll error: {e}"}), flush=True)
            await asyncio.sleep(10)

        await asyncio.sleep(1)

    await client.close()


async def forward_to_zai(agent_base, message, chat_id=None):
    """Forward a message to z.ai Agent mode via samweb's agent API."""
    try:
        timeout = aiohttp.ClientTimeout(total=300)
        async with aiohttp.ClientSession(timeout=timeout) as session:
            if chat_id:
                # Continue existing chat
                await session.post(f"{agent_base}/agent/navigate-direct",
                    json={"url": f"https://chat.z.ai/c/{chat_id}"})
                await asyncio.sleep(5)
            else:
                # New chat — navigate to z.ai root
                await session.post(f"{agent_base}/agent/navigate-direct",
                    json={"url": "https://chat.z.ai/"})
                await asyncio.sleep(6)
                # Load cookies + reload
                await session.post(f"{agent_base}/agent/load-cookies")
                await asyncio.sleep(1)
                await session.post(f"{agent_base}/agent/reload")
                await asyncio.sleep(6)
                # Switch to Agent mode
                await session.post(f"{agent_base}/agent/eval", json={"script": """(function(){
                    var all = document.querySelectorAll('div, button, a, span, li');
                    for (var i = 0; i < all.length; i++) {
                        var el = all[i]; var dt = '';
                        for (var j = 0; j < el.childNodes.length; j++) {
                            var n = el.childNodes[j]; if (n.nodeType === 3) dt += n.nodeValue;
                        }
                        dt = dt.trim();
                        if (dt === 'Agent 模式') {
                            var r = el.getBoundingClientRect();
                            if (r.width > 0 && r.height > 0) { el.click(); return 'ok'; }
                        }
                    }
                    return 'not found';
                })()"""})
                await asyncio.sleep(3)

            # Type + send
            await session.post(f"{agent_base}/agent/type", json={
                "selector": "#chat-input", "text": message, "clear": True
            })
            await asyncio.sleep(1)
            await session.post(f"{agent_base}/agent/eval", json={"script": """(function(){
                var btn = document.querySelector('button.sendMessageButton, button[class*="sendMessageButton"]');
                if (!btn) return 'no btn'; if (btn.disabled) return 'disabled';
                btn.click(); return 'ok';
            })()"""})
            await asyncio.sleep(8)

            # Get chat_id from URL (for new chats)
            zai_chat_id = None
            if not chat_id:
                async with session.get(f"{agent_base}/agent/state",
                    timeout=aiohttp.ClientTimeout(total=10)) as resp:
                    state = await resp.json()
                    url = state.get("url", "")
                    if "/c/" in url:
                        zai_chat_id = url.split("/c/")[-1].split("/")[0].split("?")[0]

            # Poll for response (max 3 min)
            last_response = ""
            stable_count = 0
            for attempt in range(36):
                resp = await session.post(f"{agent_base}/agent/eval", json={"script": """(function(){
                    var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]'];
                    var asst = [];
                    for (var s = 0; s < sels.length; s++) {
                        var f = document.querySelectorAll(sels[s]);
                        for (var i = 0; i < f.length; i++) asst.push(f[i]);
                    }
                    var seen = {};
                    asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                    if (asst.length === 0) return JSON.stringify({stage:'waiting'});
                    var last = asst[asst.length-1];
                    var ft = (last.innerText||'').trim();
                    if (/回复内容为空|请稍后重试|限制沙箱|当前模型使用人数较多/.test(ft))
                        return JSON.stringify({stage:'error',error:ft.slice(0,200)});
                    var ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
                    if (!ce) {
                        var ds = last.querySelectorAll('div');
                        for (var i=ds.length-1;i>=0;i--){var d=ds[i];var c=(d.className||'').toString();
                        if(!/thinking|reasoning|action|toolCallTrace/i.test(c)&&d.innerText.trim().length>50){ce=d;break;}}
                    }
                    var r = ce?(ce.innerText||'').trim():ft;
                    if (r && r.length > 10) return JSON.stringify({stage:'responding',response:r});
                    return JSON.stringify({stage:'loading'});
                })()"""})
                data = await resp.json()
                value = data.get("value", data)
                if isinstance(value, dict) and "value" in value:
                    value = value["value"]
                if isinstance(value, str):
                    try: value = json.loads(value)
                    except: pass
                if isinstance(value, dict):
                    stage = value.get("stage", "")
                    if stage == "responding":
                        resp_text = value.get("response", "")
                        if resp_text == last_response:
                            stable_count += 1
                            if stable_count >= 3:
                                return resp_text
                        else:
                            last_response = resp_text
                            stable_count = 0
                    elif stage == "error":
                        return f"Error: {value.get('error', 'unknown')}"
                await asyncio.sleep(5)

            return last_response or "(timeout)"
    except Exception as e:
        return f"Error: {e}"


if __name__ == "__main__":
    asyncio.run(main())

#!/usr/bin/env python3
"""AICQ bridge for a single profile.

Connects to:
  - The profile's tab worker agent API (localhost:<agent_port>) for z.ai
    DOM automation.
  - The profile's AICQ agent identity (in ~/.aicq-sdk/<db_file>) for
    receiving/sending messages to the owner on aicq.me.

Flow:
  1. Poll AICQ for new messages from the owner.
  2. On new message → forward to z.ai via the tab worker's agent API
     (type into chat input, click send, poll for response).
  3. Send z.ai's response back to the owner via AICQ.

Usage:
  python aicq_bridge.py --profile qq --agent-port 55978 --db-path ~/.aicq-sdk/data.db
"""
import argparse
import asyncio
import json
import os
import re
import sys
import time

import aiohttp

# AICQ SDK is installed on shan at:
#   C:\Users\Administrator\AppData\Local\Programs\Python\Python313\Lib\site-packages\aicq
sys.path.insert(0, os.path.expanduser(
    "~/AppData/Local/Programs/Python/Python313/Lib/site-packages"))
from aicq import AICQCore, AICQError

SERVER = "https://aicq.me"
POLL_INTERVAL = 5  # seconds between AICQ polls


def log(msg):
    """Log to stdout with timestamp + profile prefix."""
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


async def connect_aicq(db_path, profile_id):
    """Connect to AICQ using the profile's db, return AICQCore + agent info."""
    core = AICQCore(db_path=db_path, server=SERVER)
    agent = core.db.get_agent()
    if not agent:
        log(f"[{profile_id}] no agent found in {db_path}")
        return None, None
    account_id = agent.get("account_id", "")
    log(f"[{profile_id}] AICQ agent: {account_id} ({agent.get('name','')})")

    # Login
    try:
        await core.login()
        log(f"[{profile_id}] AICQ login OK")
    except Exception as e:
        log(f"[{profile_id}] AICQ login failed: {e}")
        return None, None

    # Connect WebSocket for real-time message reception
    try:
        await core.connect()
        log(f"[{profile_id}] AICQ WebSocket connected")
    except Exception as e:
        log(f"[{profile_id}] AICQ connect warning: {e}")

    return core, agent


async def forward_to_zai(agent_base, message, profile_id):
    """Forward a message to z.ai via the tab worker's agent API.
    Returns z.ai's response text."""
    try:
        timeout = aiohttp.ClientTimeout(total=300)
        async with aiohttp.ClientSession(timeout=timeout) as session:
            # Check tab worker is alive
            try:
                async with session.get(f"{agent_base}/agent/health",
                                       timeout=aiohttp.ClientTimeout(total=5)) as resp:
                    if resp.status != 200:
                        return f"Error: tab worker health check failed ({resp.status})"
            except Exception as e:
                return f"Error: tab worker not reachable: {e}"

            log(f"[{profile_id}] forwarding to z.ai: {message[:80]}...")

            # z.ai should already be loaded in the tab worker (navigated
            # on startup). Just type + send.

            # Wait for chat input to be ready
            for attempt in range(10):
                resp = await session.post(f"{agent_base}/agent/eval", json={
                    "script": "(function(){ var el = document.querySelector('#chat-input, textarea[class*=\"chat-input\"], div[contenteditable=\"true\"]'); return el ? 'ready' : 'not found'; })()"
                })
                data = await resp.json()
                val = data.get("value", data)
                if isinstance(val, dict):
                    val = val.get("value", "")
                if val == "ready":
                    break
                await asyncio.sleep(2)
            else:
                return "Error: chat input not found on z.ai page"

            # Type the message
            resp = await session.post(f"{agent_base}/agent/eval", json={
                "script": f"""(function(){{
                    var el = document.querySelector('#chat-input, textarea[class*="chat-input"], div[contenteditable="true"]');
                    if (!el) return 'no input';
                    // For contenteditable divs, use execCommand
                    if (el.contentEditable === 'true') {{
                        el.focus();
                        document.execCommand('insertText', false, {json.dumps(message)});
                        return 'typed_ce';
                    }}
                    // For textarea/input, use native setter
                    var proto = el.tagName === 'TEXTAREA' ?
                        window.HTMLTextAreaElement.prototype :
                        window.HTMLInputElement.prototype;
                    var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
                    setter.call(el, {json.dumps(message)});
                    el.dispatchEvent(new InputEvent('input', {{bubbles: true, inputType: 'insertText', data: {json.dumps(message)}}}));
                    el.dispatchEvent(new Event('change', {{bubbles: true}}));
                    return 'typed';
                }})()"""
            })
            await asyncio.sleep(1)

            # Click send button
            resp = await session.post(f"{agent_base}/agent/eval", json={
                "script": """(function(){
                    // Try multiple selectors for send button
                    var sels = [
                        'button.sendMessageButton',
                        'button[class*="sendMessageButton"]',
                        'button[class*="send-button"]',
                        'button[type="submit"]',
                        'button[aria-label*="Send"]',
                        'button[aria-label*="发送"]'
                    ];
                    for (var i = 0; i < sels.length; i++) {
                        var btn = document.querySelector(sels[i]);
                        if (btn && !btn.disabled) { btn.click(); return 'sent:' + sels[i]; }
                    }
                    // Fallback: find any button near the input
                    var input = document.querySelector('#chat-input, textarea, div[contenteditable="true"]');
                    if (input) {
                        var parent = input.closest('form, div[class*="input"], div[class*="chat"]');
                        if (parent) {
                            var btns = parent.querySelectorAll('button');
                            for (var j = 0; j < btns.length; j++) {
                                if (!btns[j].disabled && btns[j].getBoundingClientRect().width > 0) {
                                    btns[j].click();
                                    return 'sent:fallback_' + j;
                                }
                            }
                        }
                    }
                    return 'no_send_btn';
                })()"""
            })
            data = await resp.json()
            val = data.get("value", data)
            if isinstance(val, dict):
                val = val.get("value", "")
            log(f"[{profile_id}] send result: {val}")

            # Poll for response (max 3 min)
            last_response = ""
            stable_count = 0
            for attempt in range(36):
                resp = await session.post(f"{agent_base}/agent/eval", json={
                    "script": """(function(){
                        var sels = [
                            '[class*="chat-assistant"]',
                            '[class*="assistant-message"]',
                            '[class*="agent-message"]',
                            '[class*="markdown-prose"]',
                            '[class*="prose"]'
                        ];
                        var asst = [];
                        for (var s = 0; s < sels.length; s++) {
                            var f = document.querySelectorAll(sels[s]);
                            for (var i = 0; i < f.length; i++) asst.push(f[i]);
                        }
                        var seen = {};
                        asst = asst.filter(function(el){
                            var k = el.outerHTML.slice(0,200);
                            if (seen[k]) return false;
                            seen[k] = true;
                            return true;
                        });
                        if (asst.length === 0) return JSON.stringify({stage:'waiting'});
                        var last = asst[asst.length-1];
                        var ft = (last.innerText || '').trim();
                        if (/回复内容为空|请稍后重试|限制沙箱|当前模型使用人数较多/.test(ft))
                            return JSON.stringify({stage:'error', error: ft.slice(0,200)});
                        var ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
                        if (!ce) {
                            var ds = last.querySelectorAll('div');
                            for (var i = ds.length-1; i >= 0; i--) {
                                var d = ds[i];
                                var c = (d.className || '').toString();
                                if (!/thinking|reasoning|action|toolCallTrace/i.test(c) && d.innerText.trim().length > 50) {
                                    ce = d;
                                    break;
                                }
                            }
                        }
                        var r = ce ? (ce.innerText || '').trim() : ft;
                        if (r && r.length > 10) return JSON.stringify({stage:'responding', response: r});
                        return JSON.stringify({stage:'loading'});
                    })()"""
                })
                data = await resp.json()
                value = data.get("value", data)
                if isinstance(value, dict) and "value" in value:
                    value = value["value"]
                if isinstance(value, str):
                    try:
                        value = json.loads(value)
                    except Exception:
                        pass
                if isinstance(value, dict):
                    stage = value.get("stage", "")
                    if stage == "responding":
                        resp_text = value.get("response", "")
                        if resp_text == last_response:
                            stable_count += 1
                            if stable_count >= 3:
                                log(f"[{profile_id}] response stable ({len(resp_text)} chars)")
                                return resp_text
                        else:
                            last_response = resp_text
                            stable_count = 0
                    elif stage == "error":
                        return f"Error: {value.get('error', 'unknown')}"
                await asyncio.sleep(5)

            return last_response or "(z.ai 超时未响应)"
    except Exception as e:
        return f"Error: {e}"


async def run_bridge(profile_id, agent_port, db_path):
    """Main bridge loop for one profile."""
    agent_base = f"http://127.0.0.1:{agent_port}"

    log(f"[{profile_id}] starting bridge: agent_port={agent_port} db={db_path}")

    # Connect AICQ
    core, agent = await connect_aicq(db_path, profile_id)
    if not core:
        log(f"[{profile_id}] failed to connect AICQ, exiting")
        return

    # Set up message handler
    message_queue = asyncio.Queue()

    async def on_message(msg):
        """Called when a message arrives from AICQ."""
        try:
            from_id = msg.get("from_id", msg.get("from", ""))
            content = msg.get("content", "")
            # Skip our own messages (from AI agents)
            if from_id and from_id.startswith("ai_"):
                return
            # Strip HTML
            clean = re.sub(r'<[^>]+>', '', content).strip()
            if not clean:
                return
            await message_queue.put({"from": from_id, "content": clean})
        except Exception as e:
            log(f"[{profile_id}] on_message error: {e}")

    # Register message handler
    try:
        core.on_message(on_message)
    except Exception as e:
        log(f"[{profile_id}] on_message registration warning: {e}")

    # Process messages
    log(f"[{profile_id}] bridge ready, waiting for messages...")

    while True:
        try:
            msg = await asyncio.wait_for(message_queue.get(), timeout=60)
        except asyncio.TimeoutError:
            # Periodic health check
            try:
                async with aiohttp.ClientSession() as session:
                    async with session.get(f"{agent_base}/agent/health",
                                           timeout=aiohttp.ClientTimeout(total=5)) as resp:
                        if resp.status != 200:
                            log(f"[{profile_id}] tab worker health check failed")
            except Exception:
                log(f"[{profile_id}] tab worker unreachable")
            continue

        from_id = msg["from"]
        content = msg["content"]
        log(f"[{profile_id}] message from {from_id}: {content[:80]}...")

        # Forward to z.ai
        response = await forward_to_zai(agent_base, content, profile_id)
        log(f"[{profile_id}] z.ai response: {str(response)[:80]}...")

        # Send response back via AICQ
        try:
            await core.send_message(from_id, str(response))
            log(f"[{profile_id}] response sent to {from_id}")
        except Exception as e:
            log(f"[{profile_id}] send_message error: {e}")
            # Fallback: try chat()
            try:
                await core.send_message(from_id, str(response)[:2000])
            except Exception as e2:
                log(f"[{profile_id}] fallback send also failed: {e2}")


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--profile", required=True, help="profile ID")
    parser.add_argument("--agent-port", type=int, required=True,
                        help="tab worker agent API port")
    parser.add_argument("--db-path", required=True,
                        help="AICQ SDK db file path for this profile")
    args = parser.parse_args()

    # Retry connection if tab worker isn't ready yet
    for attempt in range(30):
        try:
            async with aiohttp.ClientSession() as session:
                async with session.get(
                    f"http://127.0.0.1:{args.agent_port}/agent/health",
                    timeout=aiohttp.ClientTimeout(total=5)
                ) as resp:
                    if resp.status == 200:
                        break
        except Exception:
            pass
        log(f"[{args.profile}] waiting for tab worker (attempt {attempt+1})...")
        await asyncio.sleep(2)

    await run_bridge(args.profile, args.agent_port, args.db_path)


if __name__ == "__main__":
    asyncio.run(main())

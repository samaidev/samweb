#!/usr/bin/env python3
"""AICQ bridge for a single profile.

Connects to:
  - The profile's tab worker agent API (localhost:<agent_port>) for z.ai
    DOM automation.
  - The profile's AICQ agent identity (in ~/.aicq-sdk/<db_file>) for
    receiving/sending messages to the owner on aicq.me.

Flow (on startup):
  1. Switch z.ai to Agent mode.
  2. Clean up all existing Agent-mode chats (delete them).
  3. Wait for AICQ messages from the owner.

Flow (on each AICQ message):
  4. Create a new z.ai Agent-mode chat.
  5. Type the message, click send.
  6. Poll for z.ai's response.
  7. Send the response back to the owner via AICQ.
  8. Subsequent messages from the same AICQ friend continue in the same
     z.ai chat (so context is retained). New friends get new chats.

Usage:
  python aicq_bridge.py --profile qq --agent-port 55978 --db-path ~/.aicq-sdk/data.db
"""
import argparse
import asyncio
import base64
import json
import os
import re
import sys
import time

import aiohttp

sys.path.insert(0, os.path.expanduser(
    "~/AppData/Local/Programs/Python/Python313/Lib/site-packages"))
from aicq import AICQCore, AICQError

SERVER = "https://aicq.me"


def log(profile_id, msg):
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}] [{profile_id}] {msg}", flush=True)


# ---------- z.ai DOM automation helpers ----------

async def zai_eval(session, agent_base, script, timeout=30):
    """Run a JS eval on the tab worker's z.ai page via CDP Runtime.evaluate.
    Uses /agent/cdp-eval (not /agent/eval) because tab workers don't have
    the samweb UI bootstrap JS injected, so the dispatch-based eval times out.
    """
    try:
        resp = await session.post(f"{agent_base}/agent/cdp-eval",
                                  json={"script": script},
                                  timeout=aiohttp.ClientTimeout(total=timeout))
        data = await resp.json()
        value = data.get("value", data)
        if isinstance(value, dict) and "value" in value:
            value = value["value"]
        if isinstance(value, str):
            try:
                value = json.loads(value)
            except Exception:
                pass
        return value
    except Exception as e:
        log("?", f"zai_eval error: {e}")
        return None


async def zai_switch_to_agent_mode(session, agent_base, profile_id):
    """Switch z.ai from Chat mode to Agent mode."""
    log(profile_id, "switching to Agent mode...")
    # z.ai has "Chat 模式" and "Agent 模式" as clickable DIVs in the sidebar.
    # We click "Agent 模式" to switch. The element has class containing "flex-1"
    # and text exactly "Agent 模式".
    result = await zai_eval(session, agent_base, """(function(){
        // Find the Agent 模式 button — it's a DIV with direct text node "Agent 模式"
        var all = document.querySelectorAll('div');
        for (var i = 0; i < all.length; i++) {
            var el = all[i];
            // Check direct text content (not including children)
            var directText = '';
            for (var j = 0; j < el.childNodes.length; j++) {
                var n = el.childNodes[j];
                if (n.nodeType === 3) directText += n.nodeValue;
            }
            directText = directText.trim();
            if (directText === 'Agent 模式' || directText === 'Agent Mode') {
                var r = el.getBoundingClientRect();
                if (r.width > 0 && r.height > 0) {
                    el.click();
                    return 'clicked';
                }
            }
        }
        // Fallback: check if already in Agent mode (look for Agent-mode-specific UI)
        var body = document.body ? document.body.innerText : '';
        if (body.indexOf('深度思考') >= 0 || body.indexOf('最高') >= 0) {
            // These are Agent-mode-only features
            return 'already_agent';
        }
        return 'not_found';
    })()""")
    log(profile_id, f"Agent mode switch: {result}")
    await asyncio.sleep(3)
    return result


async def zai_delete_all_chats(session, agent_base, profile_id):
    """Delete all existing chats in the z.ai sidebar.

    z.ai sidebar structure:
      <button class="w-full flex justify-between...">  ← chat row (clickable)
        <div class="text-left...truncate">今天星期几？</div>  ← chat title
        <button class="chatItemMenu invisible group-hover:visible">  ← menu btn
          <button title="Chat Menu">...</button>
        </button>
      </button>

    Delete flow: hover chat row → click chatItemMenu → click "删除" in popup.
    """
    log(profile_id, "deleting all existing chats...")
    deleted = 0
    for attempt in range(30):  # max 30 chats
        # Step 1: Find the first chat row, hover it, click its menu button
        result = await zai_eval(session, agent_base, """(function(){
            // Chat rows are buttons with class "w-full flex justify-between"
            // that contain a child with class "chatItemMenu"
            var menuBtns = document.querySelectorAll('.chatItemMenu');
            if (menuBtns.length === 0) return JSON.stringify({done: true});

            // Get the first chat row (parent of the menu button)
            var firstMenu = menuBtns[0];
            var row = firstMenu.closest('button');
            if (!row) return JSON.stringify({error: 'no row'});

            // Hover the row to make the menu button visible/clickable
            row.dispatchEvent(new MouseEvent('mouseenter', {bubbles: true}));
            row.dispatchEvent(new MouseEvent('mouseover', {bubbles: true}));
            row.dispatchEvent(new MouseEvent('mousemove', {bubbles: true, clientX: 100, clientY: 100}));

            return JSON.stringify({
                chat_text: row.innerText.trim().slice(0, 40),
                remaining: menuBtns.length,
            });
        })()""")
        if isinstance(result, str):
            try:
                result = json.loads(result)
            except Exception:
                pass
        if not isinstance(result, dict):
            result = {}

        if result.get("done"):
            log(profile_id, f"deleted {deleted} chats, none remaining")
            return deleted

        remaining = result.get("remaining", 0)
        if remaining == 0:
            log(profile_id, f"deleted {deleted} chats, none remaining")
            return deleted

        chat_text = result.get("chat_text", "?")
        log(profile_id, f"deleting chat: {chat_text} ({remaining} remaining)")

        # Step 2: Click the menu button (now visible after hover)
        await asyncio.sleep(0.5)
        click_result = await zai_eval(session, agent_base, """(function(){
            var menuBtns = document.querySelectorAll('.chatItemMenu');
            if (menuBtns.length === 0) return 'no_menu';
            // Force-click the first menu button (it may have 'invisible' class
            // but we can still click it programmatically)
            var btn = menuBtns[0];
            btn.click();
            // Also try clicking the inner "Chat Menu" button
            var inner = btn.querySelector('button');
            if (inner) inner.click();
            return 'clicked';
        })()""")

        # Step 3: Wait for popup menu, find and click "删除"
        await asyncio.sleep(0.5)
        del_result = await zai_eval(session, agent_base, """(function(){
            // Look for delete option in any popup/dropdown/menu
            var allEls = document.querySelectorAll('div, button, span, li, a');
            for (var i = 0; i < allEls.length; i++) {
                var el = allEls[i];
                var t = (el.innerText || '').trim();
                if (t === '删除' || t === 'Delete' || t === '删除会话' || t === '删除对话') {
                    var r = el.getBoundingClientRect();
                    if (r.width > 0 && r.height > 0) {
                        el.click();
                        return 'deleted';
                    }
                }
            }
            return 'no_delete_option';
        })()""")

        if del_result == 'deleted':
            deleted += 1
            # Confirm dialog if any
            await asyncio.sleep(0.5)
            await zai_eval(session, agent_base, """(function(){
                var btns = document.querySelectorAll('button');
                for (var b of btns) {
                    var t = (b.innerText || '').trim();
                    if (t === '确认' || t === '确定' || t === 'Confirm' || t === 'OK' || t === '删除') {
                        b.click(); return 'confirmed';
                    }
                }
                return 'no_confirm';
            })()""")
            await asyncio.sleep(1)
        else:
            log(profile_id, f"  delete option not found ({del_result}), trying next")
            await asyncio.sleep(1)
            if attempt > 10:
                log(profile_id, f"deleted {deleted} chats, stopping (can't delete more)")
                return deleted

    log(profile_id, f"deleted {deleted} chats (max attempts)")
    return deleted


async def zai_new_chat(session, agent_base, profile_id):
    """Click '新聊天' to start a new chat. Returns the chat_id from URL."""
    log(profile_id, "creating new chat...")
    result = await zai_eval(session, agent_base, """(function(){
        // Find "新聊天" button
        var all = document.querySelectorAll('button, a, div');
        for (var el of all) {
            var t = (el.innerText || '').trim();
            if (t === '新聊天' || t === 'New Chat' || t === '新建对话') {
                var r = el.getBoundingClientRect();
                if (r.width > 0 && r.height > 0) { el.click(); return 'clicked'; }
            }
        }
        return 'not_found';
    })()""")
    log(profile_id, f"new chat: {result}")
    await asyncio.sleep(3)

    # Get chat_id from URL
    state = await zai_eval(session, agent_base, "window.location.href")
    chat_id = None
    if isinstance(state, str) and "/c/" in state:
        chat_id = state.split("/c/")[-1].split("/")[0].split("?")[0]
    log(profile_id, f"new chat_id: {chat_id}")
    return chat_id


async def zai_dismiss_high_traffic_popup(session, agent_base, profile_id):
    """Check if z.ai is showing a high-traffic popup. If so, click '取消'
    to dismiss it. Returns True if a popup was found and dismissed."""
    result = await zai_eval(session, agent_base, """(function(){
        // Look for the high-traffic modal
        var modals = document.querySelectorAll('[class*="modal"], [role="dialog"]');
        for (var i = 0; i < modals.length; i++) {
            var el = modals[i];
            var text = el.innerText || '';
            if (text.indexOf('高峰') >= 0 || text.indexOf('人数较多') >= 0 ||
                text.indexOf('稍后再试') >= 0 || text.indexOf('繁忙') >= 0) {
                // Found the high-traffic popup — click "取消"
                var btns = el.querySelectorAll('button');
                for (var j = 0; j < btns.length; j++) {
                    var t = (btns[j].innerText || '').trim();
                    if (t === '取消' || t === 'Cancel') {
                        btns[j].click();
                        return 'dismissed';
                    }
                }
                return 'found_but_no_cancel';
            }
        }
        return 'no_popup';
    })()""")
    if result == 'dismissed':
        log(profile_id, "high-traffic popup dismissed (clicked 取消)")
        await asyncio.sleep(1)
        return True
    return False


async def zai_type_and_send(session, agent_base, profile_id, message, core=None, from_id=None):
    """Type a message into z.ai chat input and click send.
    Handles high-traffic popups: if z.ai shows "高峰时段" popup after
    sending, clicks "取消", waits 20s, and retries. Up to 20 retries.
    """
    # Wait for chat input
    for attempt in range(10):
        ready = await zai_eval(session, agent_base, """(function(){
            var el = document.querySelector('#chat-input, textarea[class*="chat-input"], div[contenteditable="true"]');
            return el ? 'ready' : 'not_found';
        })()""")
        if ready == "ready":
            break
        await asyncio.sleep(2)
    else:
        return False, "chat input not found"

    # Type the message
    await zai_eval(session, agent_base, f"""(function(){{
        var el = document.querySelector('#chat-input, textarea[class*="chat-input"], div[contenteditable="true"]');
        if (!el) return 'no_input';
        if (el.contentEditable === 'true') {{
            el.focus();
            document.execCommand('insertText', false, {json.dumps(message)});
            return 'typed_ce';
        }}
        var proto = el.tagName === 'TEXTAREA' ?
            window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
        var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
        setter.call(el, {json.dumps(message)});
        el.dispatchEvent(new InputEvent('input', {{bubbles: true, inputType: 'insertText', data: {json.dumps(message)}}}));
        el.dispatchEvent(new Event('change', {{bubbles: true}}));
        return 'typed';
    }})()""")
    await asyncio.sleep(1)

    # Click send — with high-traffic retry (max 20 attempts, 20s apart)
    for send_attempt in range(20):
        # First dismiss any existing popup
        await zai_dismiss_high_traffic_popup(session, agent_base, profile_id)

        # Re-type if input was cleared (z.ai may clear on failed send)
        if send_attempt > 0:
            await zai_eval(session, agent_base, f"""(function(){{
                var el = document.querySelector('#chat-input, textarea[class*="chat-input"], div[contenteditable="true"]');
                if (!el) return 'no_input';
                if (el.contentEditable === 'true') {{
                    el.focus();
                    document.execCommand('insertText', false, {json.dumps(message)});
                    return 'retyped_ce';
                }}
                var proto = el.tagName === 'TEXTAREA' ?
                    window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
                var setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
                setter.call(el, {json.dumps(message)});
                el.dispatchEvent(new InputEvent('input', {{bubbles: true}}));
                return 'retyped';
            }})()""")
            await asyncio.sleep(1)

        # Click send
        send_result = await zai_eval(session, agent_base, """(function(){
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
            var input = document.querySelector('#chat-input, textarea, div[contenteditable="true"]');
            if (input) {
                var parent = input.closest('form, div[class*="input"], div[class*="chat"]');
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

        if send_result and send_result.startswith('sent'):
            log(profile_id, f"send attempt {send_attempt+1}: {send_result}")
            # Wait a moment, then check for high-traffic popup
            await asyncio.sleep(3)
            if await zai_dismiss_high_traffic_popup(session, agent_base, profile_id):
                # Popup appeared — update AICQ status bar + wait 20s + retry
                if core and from_id:
                    try:
                        await core.send_stream_chunk(from_id, "thinking",
                            f"高峰期，正在重试... ({send_attempt+1}/20)")
                    except Exception:
                        pass
                log(profile_id, f"high-traffic popup detected, waiting 20s before retry ({send_attempt+1}/20)")
                await asyncio.sleep(20)
                continue
            else:
                # No popup — send succeeded
                return True, send_result
        else:
            log(profile_id, f"send attempt {send_attempt+1} failed: {send_result}")
            await asyncio.sleep(5)

    return False, "max retries exceeded (high traffic)"


async def zai_wait_for_response(session, agent_base, profile_id, max_wait=180, core=None, from_id=None):
    """Poll for z.ai's response. Returns the response text.

    If core + from_id are provided, checks is_stream_cancelled on each
    poll — if the user clicked the stop button on aicq.me, clicks z.ai's
    stop button and returns whatever response was received so far.
    """
    last_response = ""
    stable_count = 0
    for attempt in range(max_wait // 3):
        result = await zai_eval(session, agent_base, """(function(){
            // Check if z.ai is still thinking/executing (loading indicators)
            var body = document.body ? document.body.innerText : '';
            var isThinking = /思考中|生成中|正在|loading|思考过程/.test(body) &&
                document.querySelector('[class*="loading"], [class*="spinner"], [class*="thinking"], [class*="generating"]');
            // Check for stop button visibility (z.ai shows stop btn while generating)
            var stopBtns = document.querySelectorAll('button[class*="stop"], button[title*="停止"], button[title*="Stop"], button[aria-label*="stop"]');
            var zaiGenerating = false;
            for (var i = 0; i < stopBtns.length; i++) {
                var r = stopBtns[i].getBoundingClientRect();
                if (r.width > 0 && r.height > 0) { zaiGenerating = true; break; }
            }

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
            if (asst.length === 0) return JSON.stringify({stage:'waiting', generating: zaiGenerating});
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
                        ce = d; break;
                    }
                }
            }
            var r = ce ? (ce.innerText || '').trim() : ft;
            if (r && r.length > 10) return JSON.stringify({stage:'responding', response: r, generating: zaiGenerating});
            return JSON.stringify({stage:'loading', generating: zaiGenerating});
        })()""")
        if isinstance(result, str):
            try:
                result = json.loads(result)
            except Exception:
                pass
        if isinstance(result, dict):
            stage = result.get("stage", "")
            generating = result.get("generating", False)
            if stage == "responding":
                resp_text = result.get("response", "")
                # Only count as stable if z.ai is NOT still generating
                if resp_text == last_response and not generating:
                    stable_count += 1
                    if stable_count >= 2:
                        log(profile_id, f"response ready ({len(resp_text)} chars)")
                        return resp_text
                else:
                    last_response = resp_text
                    stable_count = 0
                    # If z.ai is still generating, update status bar
                    if generating and core and from_id:
                        try:
                            await core.send_stream_chunk(from_id, "thinking",
                                f"z.ai 正在执行... ({len(resp_text)} 字)")
                        except Exception:
                            pass
            elif stage == "error":
                return f"Error: {result.get('error', 'unknown')}"

        # Check if user clicked the stop button on aicq.me
        if core and from_id:
            try:
                if await core.is_stream_cancelled(from_id):
                    log(profile_id, "stream cancelled by user — stopping z.ai")
                    await core.clear_stream_cancel(from_id)
                    # Click z.ai's stop button (if visible)
                    await zai_eval(session, agent_base, """(function(){
                        // z.ai stop button — look for stop/cancel buttons
                        var btns = document.querySelectorAll('button');
                        for (var i = 0; i < btns.length; i++) {
                            var b = btns[i];
                            var t = (b.innerText || '').trim();
                            var title = (b.getAttribute('title') || '').toLowerCase();
                            var r = b.getBoundingClientRect();
                            if (r.width > 0 && r.height > 0 &&
                                (t === '停止' || t === 'Stop' || t === '停止生成' ||
                                 title.indexOf('stop') >= 0 || title.indexOf('停止') >= 0)) {
                                b.click();
                                return 'stopped';
                            }
                        }
                        return 'no_stop_btn';
                    })()""")
                    return last_response + "\n\n*[已停止]*" if last_response else "*[已停止]*"
            except Exception:
                pass

        await asyncio.sleep(3)
    return last_response or "(z.ai 超时未响应)"


# ---------- AICQ connection ----------

async def connect_aicq(db_path, profile_id):
    core = AICQCore(db_path=db_path, server=SERVER)
    agent = core.db.get_agent()
    if not agent:
        log(profile_id, f"no agent found in {db_path}")
        return None, None
    log(profile_id, f"AICQ agent: {agent.get('account_id','')} ({agent.get('name','')})")
    try:
        await core.login()
        log(profile_id, "AICQ login OK")
    except Exception as e:
        log(profile_id, f"AICQ login failed: {e}")
        return None, None
    try:
        await core.connect()
        log(profile_id, "AICQ WebSocket connected")
    except Exception as e:
        log(profile_id, f"AICQ connect warning: {e}")
    return core, agent


# ---------- Main bridge ----------

async def run_bridge(profile_id, agent_port, db_path):
    agent_base = f"http://127.0.0.1:{agent_port}"
    log(profile_id, f"starting bridge: agent_port={agent_port} db={db_path}")

    # Connect AICQ
    core, agent = await connect_aicq(db_path, profile_id)
    if not core:
        return

    # Connect to tab worker's agent API
    timeout = aiohttp.ClientTimeout(total=300)
    session = aiohttp.ClientSession(timeout=timeout)

    try:
        # Step 1: Switch to Agent mode
        await zai_switch_to_agent_mode(session, agent_base, profile_id)

        # Step 2: Delete all existing chats
        await zai_delete_all_chats(session, agent_base, profile_id)

        # Step 3: Set up message queue + handler
        message_queue = asyncio.Queue()
        # Map: AICQ friend_id → z.ai chat_id (for context retention).
        # Default behavior: subsequent messages from the same friend
        # continue in the same z.ai chat. "点加号" (new chat) is a
        # separate action not yet implemented via AICQ.
        chat_map = {}

        async def on_message(msg):
            try:
                from_id = msg.get("from_id", msg.get("from", ""))
                content = msg.get("content", "")
                if from_id and from_id.startswith("ai_"):
                    return
                clean = re.sub(r'<[^>]+>', '', content).strip()
                if not clean:
                    return
                await message_queue.put({"from": from_id, "content": clean})
            except Exception as e:
                log(profile_id, f"on_message error: {e}")

        try:
            core.on_message(on_message)
        except Exception as e:
            log(profile_id, f"on_message registration warning: {e}")

        log(profile_id, "bridge ready, waiting for AICQ messages...")

        # Step 4: Message loop (serial — processes one message at a time,
        # new messages queue up while z.ai is responding)
        while True:
            try:
                msg = await asyncio.wait_for(message_queue.get(), timeout=60)
            except asyncio.TimeoutError:
                # Periodic health check
                try:
                    async with session.get(f"{agent_base}/agent/health",
                                           timeout=aiohttp.ClientTimeout(total=5)) as resp:
                        if resp.status != 200:
                            log(profile_id, "tab worker health check failed")
                except Exception:
                    log(profile_id, "tab worker unreachable")
                continue

            from_id = msg["from"]
            content = msg["content"]
            queue_size = message_queue.qsize()
            if queue_size > 0:
                log(profile_id, f"message from {from_id}: {content[:80]}... ({queue_size} more in queue)")
                # Notify user that previous messages are still processing
                try:
                    await core.send_stream_chunk(from_id, "thinking",
                        f"正在处理... 队列中还有 {queue_size} 条消息")
                except Exception:
                    pass
            else:
                log(profile_id, f"message from {from_id}: {content[:80]}...")

            # "/new" command: delete old chats + create a new z.ai chat,
            # then wait for the next message (don't send "/new" to z.ai).
            if content.strip().lower() == "/new":
                log(profile_id, f"/new command — creating new z.ai chat")
                # Delete all existing chats
                await zai_delete_all_chats(session, agent_base, profile_id)
                # Create a new chat
                new_id = await zai_new_chat(session, agent_base, profile_id)
                if new_id:
                    chat_map[from_id] = new_id
                    await core.send_message(from_id, "✅ 已新建会话，请发送消息")
                else:
                    await core.send_message(from_id, "⚠️ 新建会话失败，请重试")
                continue

            # Default: continue in the same z.ai chat for this friend
            # (context retention). Only create a new chat if we don't
            # have one yet for this friend.
            chat_id = chat_map.get(from_id)
            if chat_id:
                log(profile_id, f"continuing chat {chat_id} for {from_id}")
            else:
                # First message from this friend — create a new chat
                chat_id = await zai_new_chat(session, agent_base, profile_id)
                if chat_id:
                    chat_map[from_id] = chat_id

            # Type + send (with high-traffic retry)
            ok, send_result = await zai_type_and_send(session, agent_base, profile_id, content, core, from_id)
            if not ok:
                log(profile_id, f"failed to send to z.ai: {send_result}")
                await core.send_message(from_id, f"发送失败: {send_result}")
                continue

            # Update AICQ status bar: waiting for z.ai response
            if core and from_id:
                try:
                    await core.send_stream_chunk(from_id, "thinking", "正在等待 z.ai 响应...")
                except Exception:
                    pass

            # Wait for response (poll every 3s, max 180s)
            response = await zai_wait_for_response(session, agent_base, profile_id, core=core, from_id=from_id)
            log(profile_id, f"z.ai response: {str(response)[:80]}...")

            # Send response back via AICQ as a stream (so the status bar
            # is properly cleared and the message renders correctly).
            # 1. Send the response text as a stream chunk
            # 2. End the stream (this hides the status bar + finalizes msg)
            if core and from_id:
                try:
                    await core.send_stream_chunk(from_id, "text", str(response))
                    await core.send_stream_end(from_id)
                    log(profile_id, f"response streamed to {from_id}")
                except Exception as e:
                    log(profile_id, f"stream send failed ({e}), falling back to send_message")
                    try:
                        await core.send_message(from_id, str(response))
                        log(profile_id, f"response sent to {from_id}")
                    except Exception as e2:
                        log(profile_id, f"send_message error: {e2}")
    finally:
        await session.close()


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--profile", required=True)
    parser.add_argument("--agent-port", type=int, required=True)
    parser.add_argument("--db-path", required=True)
    args = parser.parse_args()

    # Wait for tab worker to be ready
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
        log(args.profile, f"waiting for tab worker (attempt {attempt+1})...")
        await asyncio.sleep(2)

    await run_bridge(args.profile, args.agent_port, args.db_path)


if __name__ == "__main__":
    asyncio.run(main())

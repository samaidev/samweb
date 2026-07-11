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
import random
import re
import sys
import time

import aiohttp

# 20 greeting messages (~30 chars each) for bypassing z.ai usage limits.
# Start with unusual/random words to avoid pattern detection.
GREETINGS = [
    "咕咕咕，今天窗外的麻雀好吵啊，你有没有被鸟叫声吵醒过呢",
    "锵锵！我刚发现冰箱里还有一块去年的月饼，你说还能吃吗",
    "哔哔哔——闹钟响了但我已经醒了，你平时几点起床呀朋友",
    "呼噜噜，我家猫刚打了个超大哈欠，猫为什么这么爱睡觉呢",
    "叮咚！外卖到了但是送错了地址，你遇到过这种情况吗哈哈",
    "噗嗤，刚才看到一个搞笑视频笑到肚子疼，你喜欢看脱口秀吗",
    "哎呀呀，今天出门忘带钥匙了，你有健忘的时候吗最近怎么样",
    "呜哇，这个奶茶也太好喝了吧，你平时喜欢喝什么口味的茶呢",
    "唧唧喳喳的楼下小孩又在玩了，你小时候最喜欢玩什么游戏啊",
    "咣当一声，我把杯子摔了但没碎，你相信不锈钢杯子的质量吗",
    "嗡嗡嗡，有只蚊子在耳边飞来飞去好烦啊，你怕蚊子叮咬吗朋友",
    "啪嗒，键盘上的键帽掉了一个，你用机械键盘还是薄膜键盘呢",
    "嘻嘻嘻，今天在路上踩到了水坑溅了一裤腿，你有过倒霉时刻吗",
    "嘎嘎嘎，刚才看到一群鸭子过马路，它们居然懂交通规则似的",
    "嘟嘟嘟，电话打不通估计在忙吧，你更喜欢打电话还是发消息",
    "哈哈哈今天地铁上有人打了个喷嚏超大声，你打喷嚏会很大声吗",
    "咦？我的袜子又少了一只，洗衣机是不是真的会吃袜子你知道吗",
    "哇塞这个日落也太美了吧，你有没有拍过特别好看的天空照片呢",
    "嘿嘿嘿刚偷吃了一块巧克力，你觉得吃甜食会让心情变好吗朋友",
    "哎呀，手机电量只剩百分之三了，你出门会带充电宝吗一般习惯",
]

# Keywords that indicate z.ai usage limit / rate limit error
LIMIT_KEYWORDS = ["用量限制", "使用限制", "额度", "请求过于频繁", "rate limit",
                  "使用次数", "今日额度", "限制沙箱", "用量已达",
                  "用量已超出", "超出个人限制", "Agent 用量", "回复内容为空"]

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


async def zai_select_first_chat(session, agent_base, profile_id):
    """Click the first chat in the sidebar to select it. Returns the
    chat_id from the URL, or None if no chats exist."""
    result = await zai_eval(session, agent_base, """(function(){
        // Chat rows are buttons containing .chatItemMenu
        var rows = document.querySelectorAll('button:has(.chatItemMenu)');
        if (rows.length === 0) {
            // Fallback: look for chat links
            rows = document.querySelectorAll('a[href*="/c/"]');
        }
        if (rows.length === 0) return JSON.stringify({found: false});
        // Click the first one
        rows[0].click();
        return JSON.stringify({found: true});
    })()""")
    await asyncio.sleep(3)
    # Get chat_id from URL
    state = await zai_eval(session, agent_base, "window.location.href")
    chat_id = None
    if isinstance(state, str) and "/c/" in state:
        chat_id = state.split("/c/")[-1].split("/")[0].split("?")[0]
    return chat_id


async def zai_dismiss_usage_limit_popup(session, agent_base, profile_id):
    """Check if z.ai is showing a usage limit popup. If so, click '好的'
    to dismiss it. Returns True if a popup was found and dismissed."""
    result = await zai_eval(session, agent_base, """(function(){
        var modals = document.querySelectorAll('[class*="modal"], [role="dialog"]');
        for (var i = 0; i < modals.length; i++) {
            var el = modals[i];
            var text = el.innerText || '';
            if (text.indexOf('用量') >= 0 || text.indexOf('限制') >= 0 ||
                text.indexOf('额度') >= 0 || text.indexOf('次数') >= 0) {
                var btns = el.querySelectorAll('button');
                for (var j = 0; j < btns.length; j++) {
                    var t = (btns[j].innerText || '').trim();
                    if (t === '好的' || t === '确定' || t === 'OK' || t === '知道了') {
                        btns[j].click();
                        return 'dismissed';
                    }
                }
                return 'found_but_no_btn';
            }
        }
        return 'no_popup';
    })()""")
    if result == 'dismissed':
        log(profile_id, "usage limit popup dismissed (clicked 好的)")
        await asyncio.sleep(1)
        return True
    return False


def is_usage_limit_error(text):
    """Check if the response text indicates a usage limit error."""
    if not text:
        return False
    text_lower = text.lower()
    for kw in LIMIT_KEYWORDS:
        if kw in text or kw.lower() in text_lower:
            return True
    return False


async def zai_bypass_usage_limit(session, agent_base, profile_id, core, from_id, original_message):
    """Bypass z.ai usage limit by:
    1. Click '好的' to dismiss the limit popup
    2. Delete all chats
    3. Send a random greeting
    4. Wait for a normal reply (confirms limit is cleared)
    5. Delete all chats again
    6. Return True if bypass succeeded, False otherwise

    The caller should then re-send the original message.
    """
    log(profile_id, "usage limit detected — starting bypass procedure")

    # Step 1: Dismiss popup
    await zai_dismiss_usage_limit_popup(session, agent_base, profile_id)

    # Step 2: Delete all chats
    log(profile_id, "bypass: deleting all chats")
    await zai_delete_all_chats(session, agent_base, profile_id)

    # Step 2b: Re-switch to Agent mode (deleting chats may reset to Chat mode)
    log(profile_id, "bypass: re-switching to Agent mode")
    await zai_switch_to_agent_mode(session, agent_base, profile_id)

    # Step 3: Send a random greeting
    greeting = random.choice(GREETINGS)
    log(profile_id, f"bypass: sending greeting: {greeting}")
    if core and from_id:
        try:
            await core.send_stream_chunk(from_id, "thinking", "正在处理用量限制，请稍候...")
        except Exception:
            pass

    # Create new chat + type + send greeting
    await zai_new_chat(session, agent_base, profile_id)
    ok, _ = await zai_type_and_send(session, agent_base, profile_id, greeting)
    if not ok:
        log(profile_id, "bypass: failed to send greeting")
        return False

    # Step 4: Wait for a normal reply (poll up to 60s)
    log(profile_id, "bypass: waiting for greeting reply...")
    greeting_response = ""
    stable_count = 0
    for attempt in range(20):
        await asyncio.sleep(3)
        result = await zai_eval(session, agent_base, """(function(){
            var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
            var asst = [];
            for (var s = 0; s < sels.length; s++) {
                var f = document.querySelectorAll(sels[s]);
                for (var i = 0; i < f.length; i++) asst.push(f[i]);
            }
            var seen = {};
            asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
            // Exclude user messages (z.ai uses class "chat-user")
            asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
            if (asst.length === 0) return JSON.stringify({stage:'waiting'});
            var last = asst[asst.length-1];
            var ft = (last.innerText || '').trim();
            // If last element itself has 'prose' class, use its innerText directly
            // (z.ai's chat-assistant has class "chat-assistant ... markdown-prose")
            var lastClass = (last.className||'').toString();
            var ce = null;
            if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {
                ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
            }
            if (!ce) { var ds = last.querySelectorAll('div');
                for (var i=ds.length-1;i>=0;i--){var d=ds[i];var c=(d.className||'').toString();
                if(!/thinking|reasoning|action|toolCallTrace/i.test(c)&&d.innerText.trim().length>50){ce=d;break;}}}
            var r = ce ? (ce.innerText||'').trim() : ft;
            if (r && r.length > 10) return JSON.stringify({stage:'responding', response: r});
            return JSON.stringify({stage:'loading'});
        })()""")
        if isinstance(result, str):
            try: result = json.loads(result)
            except: pass
        if isinstance(result, dict) and result.get("stage") == "responding":
            resp = result.get("response", "")
            if resp == greeting_response:
                stable_count += 1
                if stable_count >= 2:
                    # Check if this is also a limit error
                    if is_usage_limit_error(resp):
                        log(profile_id, "bypass: greeting also hit limit, trying again with different greeting")
                        # Delete + re-switch Agent mode + try another greeting
                        await zai_dismiss_usage_limit_popup(session, agent_base, profile_id)
                        await zai_delete_all_chats(session, agent_base, profile_id)
                        await zai_switch_to_agent_mode(session, agent_base, profile_id)
                        greeting = random.choice(GREETINGS)
                        log(profile_id, f"bypass: retry with: {greeting}")
                        await zai_new_chat(session, agent_base, profile_id)
                        await zai_type_and_send(session, agent_base, profile_id, greeting)
                        greeting_response = ""
                        stable_count = 0
                        continue
                    else:
                        log(profile_id, f"bypass: got normal reply ({len(resp)} chars), limit cleared!")
                        break
            else:
                greeting_response = resp
                stable_count = 0
    else:
        log(profile_id, "bypass: timeout waiting for greeting reply")
        return False

    # Step 5: Delete all chats again + re-switch to Agent mode
    log(profile_id, "bypass: deleting chats after greeting reply")
    await zai_delete_all_chats(session, agent_base, profile_id)
    log(profile_id, "bypass: re-switching to Agent mode (2nd)")
    await zai_switch_to_agent_mode(session, agent_base, profile_id)
    await zai_new_chat(session, agent_base, profile_id)

    log(profile_id, "bypass: complete, ready to re-send original message")
    return True


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
            # Check for high-traffic popup multiple times (popup may appear
            # 2-8 seconds after sending). Check at 2s, 5s, 8s.
            popup_found = False
            for popup_check_delay in [2, 3, 3]:
                await asyncio.sleep(popup_check_delay)
                if await zai_dismiss_high_traffic_popup(session, agent_base, profile_id):
                    popup_found = True
                    break
            if popup_found:
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
            // Exclude user messages (z.ai uses class "chat-user")
            asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
            if (asst.length === 0) return JSON.stringify({stage:'waiting', generating: zaiGenerating});
            var last = asst[asst.length-1];
            var ft = (last.innerText || '').trim();
            if (/回复内容为空|请稍后重试|限制沙箱|当前模型使用人数较多/.test(ft))
                return JSON.stringify({stage:'error', error: ft.slice(0,200)});
            // If last element itself has 'prose' class, use its innerText directly
            // (z.ai's chat-assistant has class "chat-assistant ... markdown-prose")
            var lastClass = (last.className||'').toString();
            var ce = null;
            if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {
                ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
            }
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

        # Step 2: Select the first existing chat (don't delete any).
        # AICQ messages will continue in this chat. Only "/new" creates
        # a fresh chat.
        log(profile_id, "selecting first existing chat...")
        first_chat_id = await zai_select_first_chat(session, agent_base, profile_id)
        if first_chat_id:
            log(profile_id, f"selected chat: {first_chat_id}")
        else:
            log(profile_id, "no existing chat, will create new one on first message")

        # Step 3: Set up message queue + handler
        message_queue = asyncio.Queue()
        # Map: AICQ friend_id → z.ai chat_id (for context retention).
        # Default behavior: subsequent messages from the same friend
        # continue in the same z.ai chat. "/new" creates a fresh chat.
        chat_map = {}
        if first_chat_id:
            # Pre-populate chat_map for the owner (1000008) so the first
            # message continues in the selected chat.
            chat_map["1000008"] = first_chat_id

        # Step 3b: Check if z.ai is currently generating a response
        # (e.g. a task was running before the browser was restarted).
        # If so, stream the ongoing output to AICQ automatically.
        if first_chat_id:
            log(profile_id, "checking if z.ai is still generating...")
            # Detect generation by checking if the last assistant message
            # content changes over 6 seconds (3 polls × 2s).
            pre_count_check = await zai_eval(session, agent_base, """(function(){
                var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                var asst = [];
                for (var s = 0; s < sels.length; s++) { var f = document.querySelectorAll(sels[s]); for (var i = 0; i < f.length; i++) asst.push(f[i]); }
                var seen = {};
                asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                if (asst.length === 0) return JSON.stringify({count: 0, last_text: ''});
                var last = asst[asst.length-1];
                return JSON.stringify({count: asst.length, last_text: (last.innerText||'').slice(0,500)});
            })()""")
            if isinstance(pre_count_check, str):
                try: pre_count_check = json.loads(pre_count_check)
                except: pre_count_check = {}
            
            is_generating = False
            if isinstance(pre_count_check, dict) and pre_count_check.get("count", 0) > 0:
                # Check if content changes over 6 seconds
                text1 = pre_count_check.get("last_text", "")
                await asyncio.sleep(3)
                check2 = await zai_eval(session, agent_base, """(function(){
                    var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                    var asst = [];
                    for (var s = 0; s < sels.length; s++) { var f = document.querySelectorAll(sels[s]); for (var i = 0; i < f.length; i++) asst.push(f[i]); }
                    var seen = {};
                    asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                    asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                    if (asst.length === 0) return '';
                    return (asst[asst.length-1].innerText||'').slice(0,500);
                })()""")
                text2 = check2 if isinstance(check2, str) else ""
                if text1 != text2:
                    is_generating = True
                    log(profile_id, f"z.ai content changed! Still generating. (was {len(text1)} chars, now {len(text2)} chars)")
                else:
                    # Check one more time after 3 more seconds
                    await asyncio.sleep(3)
                    check3 = await zai_eval(session, agent_base, """(function(){
                        var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                        var asst = [];
                        for (var s = 0; s < sels.length; s++) { var f = document.querySelectorAll(sels[s]); for (var i = 0; i < f.length; i++) asst.push(f[i]); }
                        var seen = {};
                        asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                        asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                        if (asst.length === 0) return '';
                        return (asst[asst.length-1].innerText||'').slice(0,500);
                    })()""")
                    text3 = check3 if isinstance(check3, str) else ""
                    if text2 != text3:
                        is_generating = True
                        log(profile_id, f"z.ai content changed on 2nd check! Still generating. (was {len(text2)} chars, now {len(text3)} chars)")
            
            if is_generating:
                log(profile_id, "z.ai is still generating! Starting auto-stream...")
                # Record pre_count (messages before the current generation)
                pre_count = await zai_eval(session, agent_base, """(function(){
                    var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                    var asst = [];
                    for (var s = 0; s < sels.length; s++) { var f = document.querySelectorAll(sels[s]); for (var i = 0; i < f.length; i++) asst.push(f[i]); }
                    var seen = {};
                    asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                    asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                    return asst.length;
                })()""")
                if not isinstance(pre_count, int):
                    try: pre_count = int(pre_count)
                    except: pre_count = 0

                # Auto-stream the ongoing generation
                if core:
                    try:
                        await core.send_stream_chunk("1000008", "thinking", "检测到 z.ai 正在执行，接续输出中...")
                    except: pass

                last_sent_text = ""
                stable_count = 0
                for poll in range(100):
                    result = await zai_eval(session, agent_base, f"""(function(){{
                        var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                        var asst = [];
                        for (var s = 0; s < sels.length; s++) {{ var f = document.querySelectorAll(sels[s]); for (var i = 0; i < f.length; i++) asst.push(f[i]); }}
                        var seen = {{}};
                        asst = asst.filter(function(el){{var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;}});
                        asst = asst.filter(function(el){{var c=(el.className||'').toString();return c.indexOf('chat-user')<0;}});
                        var preCount = {pre_count};
                        var newMsgs = asst.slice(preCount);
                        if (newMsgs.length === 0) return JSON.stringify({{stage:'waiting'}});
                        var last = newMsgs[newMsgs.length-1];
                        var ft = (last.innerText || '').trim();
                        if (/回复内容为空|请稍后重试|限制沙箱|当前模型使用人数较多|用量已超出|超出个人限制/.test(ft))
                            return JSON.stringify({{stage:'error', error: ft.slice(0,200)}});
                        var lastClass = (last.className||'').toString();
                        var ce = null;
                        if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {{
                            ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
                        }}
                        if (!ce) {{ var ds = last.querySelectorAll('div');
                            for (var i=ds.length-1;i>=0;i--){{var d=ds[i];var c=(d.className||'').toString();
                            if(!/thinking|reasoning|action|toolCallTrace/i.test(c)&&d.innerText.trim().length>50){{ce=d;break;}}}}}}
                        var r = ce ? (ce.innerHTML||'').trim() : (last.innerHTML||'').trim();
                        var rText = ce ? (ce.innerText||'').trim() : ft;
                        if (r && rText.length > 10) return JSON.stringify({{stage:'responding', response: r}});
                        return JSON.stringify({{stage:'loading'}});
                    }})()""")
                    if isinstance(result, str):
                        try: result = json.loads(result)
                        except: pass
                    if not isinstance(result, dict):
                        result = {}
                    stage = result.get("stage", "")
                    current_text = result.get("response", "")
                    if stage == "responding" and current_text:
                        if current_text != last_sent_text:
                            if core:
                                try: await core.send_stream_chunk("1000008", "text", current_text)
                                except: pass
                            last_sent_text = current_text
                            stable_count = 0
                            log(profile_id, f"auto-streaming... ({len(current_text)} chars)")
                        else:
                            stable_count += 1
                            if stable_count >= 6:
                                log(profile_id, f"auto-stream complete ({len(current_text)} chars)")
                                if core:
                                    try: await core.send_stream_end("1000008")
                                    except: pass
                                break
                            if core:
                                try: await core.send_stream_chunk("1000008", "thinking", f"z.ai 执行中... ({len(current_text)} 字)")
                                except: pass
                    elif stage == "error":
                        if core:
                            try: await core.send_stream_end("1000008")
                            except: pass
                        break
                    await asyncio.sleep(3)
                else:
                    if core:
                        try:
                            if last_sent_text:
                                await core.send_stream_chunk("1000008", "text", last_sent_text)
                            await core.send_stream_end("1000008")
                        except: pass
                log(profile_id, "auto-stream finished")
            else:
                log(profile_id, "z.ai is idle, waiting for messages")

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
            else:
                log(profile_id, f"message from {from_id}: {content[:80]}...")

            # Wait for z.ai to be ready before processing: chat input
            # must be available. This prevents sending messages before
            # the page is fully loaded.
            ready = False
            for ready_attempt in range(10):
                check = await zai_eval(session, agent_base, """(function(){
                    var input = document.querySelector('#chat-input, textarea[class*="chat-input"], div[contenteditable="true"]');
                    var hasInput = input && input.getBoundingClientRect().width > 0;
                    var body = document.body ? document.body.innerText : '';
                    // Agent mode indicators (any one is enough)
                    var hasAgent = body.indexOf('Agent') >= 0 || body.indexOf('深度思考') >= 0
                        || body.indexOf('新聊天') >= 0 || body.indexOf('新任务') >= 0;
                    return JSON.stringify({agent_mode: hasAgent, has_input: hasInput});
                })()""")
                if isinstance(check, str):
                    try: check = json.loads(check)
                    except: pass
                if isinstance(check, dict) and check.get("has_input"):
                    ready = True
                    break
                log(profile_id, f"waiting for z.ai ready (attempt {ready_attempt+1}/10)...")
                if ready_attempt == 0:
                    await zai_switch_to_agent_mode(session, agent_base, profile_id)
                await asyncio.sleep(3)

            if not ready:
                log(profile_id, "z.ai not ready after 30s, processing anyway")

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

            # Record the number of assistant messages BEFORE sending,
            # so we only stream NEW responses (not old ones from the
            # chat history).
            pre_count = await zai_eval(session, agent_base, """(function(){
                var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                var asst = [];
                for (var s = 0; s < sels.length; s++) {
                    var f = document.querySelectorAll(sels[s]);
                    for (var i = 0; i < f.length; i++) asst.push(f[i]);
                }
                var seen = {};
                asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
                asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                return asst.length;
            })()""")
            if not isinstance(pre_count, int):
                try: pre_count = int(pre_count)
                except: pre_count = 0
            log(profile_id, f"assistant messages before send: {pre_count}")

            # Stream z.ai's response to AICQ in real-time.
            # Poll z.ai every 3s, send new text as stream chunks.
            # Finish when z.ai's output hasn't changed for 3 consecutive
            # polls (9 seconds of stability).
            last_sent_text = ""
            stable_count = 0
            max_polls = 100  # 100 × 3s = 5 min max

            if core and from_id:
                try:
                    await core.send_stream_chunk(from_id, "thinking", "正在等待 z.ai 响应...")
                except Exception:
                    pass

            for poll in range(max_polls):
                # Check if user cancelled
                if core and from_id:
                    try:
                        if await core.is_stream_cancelled(from_id):
                            log(profile_id, "stream cancelled by user")
                            await core.clear_stream_cancel(from_id)
                            # Click z.ai stop button
                            await zai_eval(session, agent_base, """(function(){
                                var btns = document.querySelectorAll('button');
                                for (var i = 0; i < btns.length; i++) {
                                    var b = btns[i];
                                    var t = (b.innerText || '').trim();
                                    var r = b.getBoundingClientRect();
                                    if (r.width > 0 && r.height > 0 &&
                                        (t === '停止' || t === 'Stop' || t === '停止生成')) {
                                        b.click(); return 'stopped';
                                    }
                                }
                                return 'no_stop_btn';
                            })()""")
                            if last_sent_text:
                                await core.send_stream_chunk(from_id, "text",
                                    last_sent_text + "\n\n*[已停止]*")
                            else:
                                await core.send_stream_chunk(from_id, "text", "*[已停止]*")
                            await core.send_stream_end(from_id)
                            log(profile_id, "cancelled response sent")
                            break
                    except Exception:
                        pass

                # Get current z.ai response text (only NEW messages after pre_count)
                result = await zai_eval(session, agent_base, f"""(function(){{
                    var sels = [
                        '[class*="chat-assistant"]',
                        '[class*="assistant-message"]',
                        '[class*="agent-message"]',
                        '[class*="markdown-prose"]',
                        '[class*="prose"]'
                    ];
                    var asst = [];
                    for (var s = 0; s < sels.length; s++) {{
                        var f = document.querySelectorAll(sels[s]);
                        for (var i = 0; i < f.length; i++) asst.push(f[i]);
                    }}
                    var seen = {{}};
                    asst = asst.filter(function(el){{
                        var k = el.outerHTML.slice(0,200);
                        if (seen[k]) return false;
                        seen[k] = true;
                        return true;
                    }});
                    asst = asst.filter(function(el){{var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;}});
                    // Only look at messages AFTER the pre_count (new responses)
                    var preCount = {pre_count};
                    var newMsgs = asst.slice(preCount);
                    if (newMsgs.length === 0) return JSON.stringify({{stage:'waiting'}});
                    var last = newMsgs[newMsgs.length-1];
                    var ft = (last.innerText || '').trim();
                    if (/回复内容为空|请稍后重试|限制沙箱|当前模型使用人数较多|用量已超出|超出个人限制/.test(ft))
                        return JSON.stringify({{stage:'error', error: ft.slice(0,200)}});
                    var lastClass = (last.className||'').toString();
                    var ce = null;
                    if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {{
                        ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
                    }}
                    if (!ce) {{
                        var ds = last.querySelectorAll('div');
                        for (var i = ds.length-1; i >= 0; i--) {{
                            var d = ds[i];
                            var c = (d.className || '').toString();
                            if (!/thinking|reasoning|action|toolCallTrace/i.test(c) && d.innerText.trim().length > 50) {{ ce = d; break; }}
                        }}
                    }}
                    // Use innerHTML to preserve formatting (markdown/HTML).
                    // aicq.me renders messages with marked.js which supports HTML.
                    var r = ce ? (ce.innerHTML||'').trim() : (last.innerHTML||'').trim();
                    var rText = ce ? (ce.innerText||'').trim() : ft;
                    if (r && rText.length > 10) return JSON.stringify({{stage:'responding', response: r}});
                    return JSON.stringify({{stage:'loading'}});
                }})()""")

                if isinstance(result, str):
                    try:
                        result = json.loads(result)
                    except Exception:
                        pass
                if not isinstance(result, dict):
                    result = {}

                stage = result.get("stage", "")
                current_text = result.get("response", "")

                if stage == "error":
                    error_msg = result.get("error", "unknown")
                    log(profile_id, f"z.ai error: {error_msg}")
                    # Check if this is a usage limit error
                    if is_usage_limit_error(error_msg) or is_usage_limit_error(last_sent_text):
                        # Bypass: dismiss popup, delete chats, send greeting,
                        # wait for reply, delete chats, then re-send original
                        bypass_ok = await zai_bypass_usage_limit(
                            session, agent_base, profile_id, core, from_id, content)
                        if bypass_ok:
                            # Re-send the original message
                            log(profile_id, "bypass succeeded, re-sending original message")
                            ok2, _ = await zai_type_and_send(
                                session, agent_base, profile_id, content, core, from_id)
                            if ok2:
                                # Reset streaming state and continue polling
                                last_sent_text = ""
                                stable_count = 0
                                continue
                        else:
                            if core and from_id:
                                await core.send_stream_chunk(from_id, "text",
                                    "❌ 用量限制，自动绕过失败，请稍后重试")
                                await core.send_stream_end(from_id)
                            break
                    else:
                        if core and from_id:
                            await core.send_stream_chunk(from_id, "text", f"❌ {error_msg}")
                            await core.send_stream_end(from_id)
                        break

                if stage == "responding" and current_text:
                    # Check if this is actually a usage limit error
                    # (z.ai returns limit error as a short "response")
                    if is_usage_limit_error(current_text):
                        log(profile_id, f"usage limit detected in response: {current_text[:80]}")
                        bypass_ok = await zai_bypass_usage_limit(
                            session, agent_base, profile_id, core, from_id, content)
                        if bypass_ok:
                            log(profile_id, "bypass succeeded, re-sending original message")
                            ok2, _ = await zai_type_and_send(
                                session, agent_base, profile_id, content, core, from_id)
                            if ok2:
                                last_sent_text = ""
                                stable_count = 0
                                continue
                        else:
                            if core and from_id:
                                await core.send_stream_chunk(from_id, "text",
                                    "❌ 用量限制，自动绕过失败，请稍后重试")
                                await core.send_stream_end(from_id)
                            break

                    # Normal response — send new/updated text as a stream chunk
                    if current_text != last_sent_text:
                        # Send the full current text (aicq.me replaces the
                        # streaming text, not appends)
                        if core and from_id:
                            try:
                                await core.send_stream_chunk(from_id, "text", current_text)
                            except Exception:
                                pass
                        last_sent_text = current_text
                        stable_count = 0
                        log(profile_id, f"streaming... ({len(current_text)} chars)")
                    else:
                        # Text unchanged — maybe z.ai is done, or maybe
                        # it's between steps (thinking → tool call →
                        # final answer). Use escalating wait strategy:
                        #   stable 1-3: wait 3s each (normal poll)
                        #   stable 4: wait 30s then check (1st fallback)
                        #   stable 5: wait 30s then check (2nd fallback)
                        #   stable 6: truly done
                        stable_count += 1
                        if stable_count >= 6:
                            # Check if the final response is actually a
                            # usage limit error (short error text)
                            if is_usage_limit_error(current_text):
                                log(profile_id, f"usage limit in response: {current_text[:60]}")
                                bypass_ok = await zai_bypass_usage_limit(
                                    session, agent_base, profile_id, core, from_id, content)
                                if bypass_ok:
                                    log(profile_id, "bypass succeeded, re-sending original message")
                                    ok2, _ = await zai_type_and_send(
                                        session, agent_base, profile_id, content, core, from_id)
                                    if ok2:
                                        last_sent_text = ""
                                        stable_count = 0
                                        continue
                                else:
                                    if core and from_id:
                                        await core.send_stream_chunk(from_id, "text",
                                            "❌ 用量限制，自动绕过失败，请稍后重试")
                                        await core.send_stream_end(from_id)
                                    break
                            else:
                                log(profile_id, f"response complete ({len(current_text)} chars, stable {stable_count} polls)")
                                if core and from_id:
                                    try:
                                        await core.send_stream_end(from_id)
                                        log(profile_id, f"response streamed to {from_id}")
                                    except Exception as e:
                                        log(profile_id, f"stream_end error: {e}")
                                break
                        elif stable_count == 4:
                            # 1st fallback: wait 30s, then re-check
                            log(profile_id, f"stable {stable_count}, waiting 30s before re-check (1st fallback)...")
                            if core and from_id:
                                try:
                                    await core.send_stream_chunk(from_id, "thinking",
                                        f"z.ai 执行中... 等待 30 秒确认 ({len(current_text)} 字)")
                                except Exception:
                                    pass
                            await asyncio.sleep(30)
                            # Re-check z.ai output
                            recheck = await zai_eval(session, agent_base, """(function(){
                                var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                                var asst = [];
                                for (var s = 0; s < sels.length; s++) {
                                    var f = document.querySelectorAll(sels[s]);
                                    for (var i = 0; i < f.length; i++) asst.push(f[i]);
                                }
                                var seen = {};
                                asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
            // Exclude user messages (z.ai uses class "chat-user")
            asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                                if (asst.length === 0) return JSON.stringify({stage:'waiting'});
                                var last = asst[asst.length-1];
                                var ft = (last.innerText || '').trim();
                                // If last element itself has 'prose' class, use its innerText directly
            // (z.ai's chat-assistant has class "chat-assistant ... markdown-prose")
            var lastClass = (last.className||'').toString();
            var ce = null;
            if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {
                ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
            }
                                if (!ce) { var ds = last.querySelectorAll('div');
                                    for (var i=ds.length-1;i>=0;i--){var d=ds[i];var c=(d.className||'').toString();
                                    if(!/thinking|reasoning|action|toolCallTrace/i.test(c)&&d.innerText.trim().length>50){ce=d;break;}}}
                                // Use innerHTML to preserve formatting (markdown/HTML).
                    // aicq.me renders messages with marked.js which supports HTML.
                    var r = ce ? (ce.innerHTML||'').trim() : (last.innerHTML||'').trim();
                    var rText = ce ? (ce.innerText||'').trim() : ft;
                                if (r && rText.length > 10) return JSON.stringify({stage:'responding', response: r});
                                return JSON.stringify({stage:'loading'});
                            })()""")
                            if isinstance(recheck, str):
                                try: recheck = json.loads(recheck)
                                except: pass
                            if isinstance(recheck, dict):
                                rtext = recheck.get("response", "")
                                if rtext and rtext != current_text:
                                    # Check if new content is a usage limit error
                                    if is_usage_limit_error(rtext):
                                        log(profile_id, f"usage limit detected in fallback! ({len(rtext)} chars): {rtext[:60]}")
                                        bypass_ok = await zai_bypass_usage_limit(
                                            session, agent_base, profile_id, core, from_id, content)
                                        if bypass_ok:
                                            log(profile_id, "bypass succeeded, re-sending original message")
                                            ok2, _ = await zai_type_and_send(
                                                session, agent_base, profile_id, content, core, from_id)
                                            if ok2:
                                                last_sent_text = ""
                                                stable_count = 0
                                                continue
                                        else:
                                            if core and from_id:
                                                await core.send_stream_chunk(from_id, "text",
                                                    "❌ 用量限制，自动绕过失败，请稍后重试")
                                                await core.send_stream_end(from_id)
                                            break
                                    # New non-limit content after 30s!
                                    log(profile_id, f"new content after 30s! ({len(rtext)} chars, was {len(current_text)})")
                                    if core and from_id:
                                        try:
                                            await core.send_stream_chunk(from_id, "text", rtext)
                                        except: pass
                                    last_sent_text = rtext
                                    stable_count = 0
                                    continue
                        elif stable_count == 5:
                            # 2nd fallback: wait 30s more, then re-check
                            log(profile_id, f"stable {stable_count}, waiting 30s before re-check (2nd fallback)...")
                            if core and from_id:
                                try:
                                    await core.send_stream_chunk(from_id, "thinking",
                                        f"z.ai 执行中... 最后确认 ({len(current_text)} 字)")
                                except: pass
                            await asyncio.sleep(30)
                            recheck = await zai_eval(session, agent_base, """(function(){
                                var sels = ['[class*="chat-assistant"]','[class*="assistant-message"]','[class*="agent-message"]','[class*="markdown-prose"]','[class*="prose"]'];
                                var asst = [];
                                for (var s = 0; s < sels.length; s++) {
                                    var f = document.querySelectorAll(sels[s]);
                                    for (var i = 0; i < f.length; i++) asst.push(f[i]);
                                }
                                var seen = {};
                                asst = asst.filter(function(el){var k=el.outerHTML.slice(0,200);if(seen[k])return false;seen[k]=true;return true;});
            // Exclude user messages (z.ai uses class "chat-user")
            asst = asst.filter(function(el){var c=(el.className||'').toString();return c.indexOf('chat-user')<0 && c.indexOf('user-message')<0;});
                                if (asst.length === 0) return JSON.stringify({stage:'waiting'});
                                var last = asst[asst.length-1];
                                var ft = (last.innerText || '').trim();
                                // If last element itself has 'prose' class, use its innerText directly
            // (z.ai's chat-assistant has class "chat-assistant ... markdown-prose")
            var lastClass = (last.className||'').toString();
            var ce = null;
            if (lastClass.indexOf('prose') < 0 && lastClass.indexOf('markdown') < 0) {
                ce = last.querySelector('[class*="prose"],[class*="markdown"],[class*="content"]');
            }
                                if (!ce) { var ds = last.querySelectorAll('div');
                                    for (var i=ds.length-1;i>=0;i--){var d=ds[i];var c=(d.className||'').toString();
                                    if(!/thinking|reasoning|action|toolCallTrace/i.test(c)&&d.innerText.trim().length>50){ce=d;break;}}}
                                // Use innerHTML to preserve formatting (markdown/HTML).
                    // aicq.me renders messages with marked.js which supports HTML.
                    var r = ce ? (ce.innerHTML||'').trim() : (last.innerHTML||'').trim();
                    var rText = ce ? (ce.innerText||'').trim() : ft;
                                if (r && rText.length > 10) return JSON.stringify({stage:'responding', response: r});
                                return JSON.stringify({stage:'loading'});
                            })()""")
                            if isinstance(recheck, str):
                                try: recheck = json.loads(recheck)
                                except: pass
                            if isinstance(recheck, dict):
                                rtext = recheck.get("response", "")
                                if rtext and rtext != current_text:
                                    if is_usage_limit_error(rtext):
                                        log(profile_id, f"usage limit in 2nd fallback! ({len(rtext)} chars)")
                                        bypass_ok = await zai_bypass_usage_limit(
                                            session, agent_base, profile_id, core, from_id, content)
                                        if bypass_ok:
                                            ok2, _ = await zai_type_and_send(
                                                session, agent_base, profile_id, content, core, from_id)
                                            if ok2:
                                                last_sent_text = ""
                                                stable_count = 0
                                                continue
                                        else:
                                            if core and from_id:
                                                await core.send_stream_chunk(from_id, "text",
                                                    "❌ 用量限制，自动绕过失败，请稍后重试")
                                                await core.send_stream_end(from_id)
                                            break
                                    log(profile_id, f"new content after 2nd 30s! ({len(rtext)} chars)")
                                    if core and from_id:
                                        try:
                                            await core.send_stream_chunk(from_id, "text", rtext)
                                        except: pass
                                    last_sent_text = rtext
                                    stable_count = 0
                                    continue
                        # Normal stable (1-3): show status
                        if core and from_id:
                            try:
                                await core.send_stream_chunk(from_id, "thinking",
                                    f"z.ai 执行中... ({len(current_text)} 字)")
                            except: pass
                elif stage == "waiting" or stage == "loading":
                    if core and from_id:
                        try:
                            await core.send_stream_chunk(from_id, "thinking", "正在等待 z.ai 响应...")
                        except Exception:
                            pass

                await asyncio.sleep(3)
            else:
                # Max polls reached
                log(profile_id, f"max polls reached, sending final response ({len(last_sent_text)} chars)")
                if core and from_id:
                    try:
                        if last_sent_text:
                            await core.send_stream_chunk(from_id, "text", last_sent_text)
                        await core.send_stream_end(from_id)
                    except Exception:
                        pass
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

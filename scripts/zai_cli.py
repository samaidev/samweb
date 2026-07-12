#!/usr/bin/env python3
"""z.ai CLI - 命令行工具，通过 samweb 控制 z.ai 浏览器会话。

功能：
  - 会话管理：列表、创建、删除、详情、搜索
  - 文件夹管理：列表、创建、删除
  - 标签管理：列表
  - 模型列表、配置查看
  - 用户信息、设置
  - 发送消息（浏览器内）
  - 网络请求捕获与分析
  - Cookie 管理
  - 截图

用法：
  python zai_cli.py chats list
  python zai_cli.py chats create --title "新会话"
  python zai_cli.py chats delete <chat_id>
  python zai_cli.py chats info <chat_id>
  python zai_cli.py chats search "关键词"
  python zai_cli.py send "你好" --model GLM-5.2
  python zai_cli.py models
  python zai_cli.py config
  python zai_cli.py user info
  python zai_cli.py folders list
  python zai_cli.py folders create "文件夹名"
  python zai_cli.py folders delete <folder_id>
  python zai_cli.py tags list
  python zai_cli.py screenshot [--full]
  python zai_cli.py network capture [--wait 10]
  python zai_cli.py cookies list [--domain z.ai]
  python zai_cli.py navigate <url>
  python zai_cli.py eval "JavaScript代码"

连接方式（通过环境变量或参数）：
  --host     samweb 地址（默认 127.0.0.1）
  --port     samweb 端口（默认 7777）
  --ssh      通过 SSH 连接（格式: user:pass@host:port）
"""

import argparse
import json
import os
import sys
import time
import urllib.request
import urllib.error
import urllib.parse


# ─── HTTP 客户端 ───────────────────────────────────────────────

class SamwebClient:
    """轻量 samweb agent server HTTP 客户端。"""

    def __init__(self, host='127.0.0.1', port=7777, timeout=30):
        self.base = f'http://{host}:{port}'
        self.timeout = timeout

    def _url(self, path):
        return f'{self.base}{path}'

    def get(self, path, params=None, timeout=None):
        url = self._url(path)
        if params:
            url += '?' + urllib.parse.urlencode(params)
        req = urllib.request.Request(url)
        return self._do(req, timeout)

    def post(self, path, data=None, timeout=None):
        url = self._url(path)
        body = json.dumps(data, ensure_ascii=False).encode('utf-8') if data else b'{}'
        req = urllib.request.Request(url, data=body, headers={'Content-Type': 'application/json'})
        return self._do(req, timeout)

    def _do(self, req, timeout=None):
        t = timeout or self.timeout
        try:
            resp = urllib.request.urlopen(req, timeout=t)
            ct = resp.headers.get('Content-Type', '')
            raw = resp.read()
            if 'image/' in ct or 'application/octet-stream' in ct:
                return {'_binary': True, 'content_type': ct, 'data': raw}
            text = raw.decode('utf-8', errors='replace')
            try:
                return json.loads(text)
            except:
                return {'_text': text}
        except urllib.error.HTTPError as e:
            body = e.read().decode('utf-8', errors='replace')
            try:
                return {'_error': True, 'status': e.code, 'body': json.loads(body)}
            except:
                return {'_error': True, 'status': e.code, 'body': body}
        except Exception as e:
            return {'_error': True, 'exception': str(e)}


# ─── z.ai API 封装 ─────────────────────────────────────────────

class ZaiAPI:
    """通过浏览器内 JS fetch 调用 z.ai API（复用浏览器的 cookie/auth）。"""

    def __init__(self, client: SamwebClient):
        self.c = client

    def _eval(self, js_code):
        r = self.c.post('/agent/eval', {'script': js_code})
        val = r
        if isinstance(val, dict) and 'value' in val:
            val = val['value']
        return val

    def _fetch(self, path, method='GET', body=None, timeout=30):
        # Build JS: body as JSON string (fetch will send it as-is with Content-Type)
        if body:
            body_json = json.dumps(body, ensure_ascii=False)
            body_js = ',headers:{"Content-Type":"application/json"},body:' + json.dumps(body_json)
        else:
            body_js = ''
        js = (
            '(function(){return fetch("' + path + '",'
            '{method:"' + method + '"' + body_js + '}'
            ').then(function(r){return r.text().then(function(t){'
            'return JSON.stringify({status:r.status,body:t});'
            '});}).catch(function(e){return JSON.stringify({error:e.message});});'
            '})()'
        )
        r = self._eval(js)
        if isinstance(r, dict) and 'value' in r:
            r = r['value']
        if isinstance(r, dict) and 'value' in r:
            r = r['value']
        try:
            return json.loads(str(r))
        except:
            return {'_raw': str(r)}

    # ─── 会话管理 ────────────────────────

    def list_chats(self, page=1, chat_type='default'):
        """获取会话列表"""
        r = self._fetch(f'/api/v1/chats/?page={page}&type={chat_type}')
        if r.get('error'):
            return r
        try:
            data = json.loads(r.get('body', '[]'))
            if isinstance(data, dict):
                return data
            return {'data': data, 'total': len(data)}
        except:
            return r

    def get_chat(self, chat_id):
        """获取会话详情"""
        r = self._fetch(f'/api/v1/chats/{chat_id}')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    def create_chat(self, title='新聊天', model='GLM-5-Turbo'):
        """创建新会话"""
        body = {
            'chat': {
                'id': '', 'title': title,
                'models': [model], 'params': {},
                'history': {'messages': {}, 'currentId': ''},
                'tags': [], 'flags': [], 'features': [],
                'enable_thinking': False, 'auto_web_search': False,
                'message_version': 1, 'extra': {},
                'timestamp': int(time.time() * 1000), 'type': 'default'
            }
        }
        r = self._fetch('/api/v1/chats/new', 'POST', body)
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    def delete_chat(self, chat_id):
        """删除会话"""
        r = self._fetch(f'/api/v1/chats/{chat_id}', 'DELETE')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    def rename_chat(self, chat_id, new_title):
        """重命名会话（通过 POST 覆盖）"""
        chat = self.get_chat(chat_id)
        if isinstance(chat, dict) and 'chat' in chat:
            chat['chat']['title'] = new_title
            r = self._fetch(f'/api/v1/chats/{chat_id}', 'POST', chat)
            try:
                return json.loads(r.get('body', '{}'))
            except:
                return r
        return {'error': 'Failed to get chat for rename'}

    def search_chats(self, keyword):
        """搜索会话（客户端过滤）"""
        result = self.list_chats(page=1)
        chats = []
        if isinstance(result, dict):
            chats = result.get('data', [])
            if isinstance(chats, dict):
                chats = list(chats.values()) if not isinstance(chats, list) else chats

        matched = []
        for c in chats:
            if isinstance(c, dict):
                title = c.get('title', '')
                if keyword.lower() in title.lower():
                    matched.append(c)
        return matched

    # ─── 文件夹管理 ─────────────────────

    def list_folders(self):
        r = self._fetch('/api/v1/folders/')
        try:
            return json.loads(r.get('body', '[]'))
        except:
            return r

    def create_folder(self, name):
        r = self._fetch('/api/v1/folders/', 'POST', {'name': name})
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    def delete_folder(self, folder_id):
        r = self._fetch(f'/api/v1/folders/{folder_id}', 'DELETE')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    # ─── 标签管理 ────────────────────────

    def list_tags(self):
        r = self._fetch('/api/v1/chats/all/tags')
        try:
            return json.loads(r.get('body', '[]'))
        except:
            return r

    # ─── 模型 ───────────────────────────

    def list_models(self):
        r = self._fetch('/api/models')
        try:
            data = json.loads(r.get('body', '{}'))
            if isinstance(data, dict) and 'data' in data:
                return data
            return data
        except:
            return r

    # ─── 配置 ───────────────────────────

    def get_config(self):
        r = self._fetch('/api/config')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    # ─── 用户 ───────────────────────────

    def get_user_info(self):
        r = self._fetch('/api/v1/auths/')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    def get_settings(self):
        r = self._fetch('/api/v1/users/user/settings')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    # ─── 场景配置 ───────────────────────

    def get_scene_config(self):
        r = self._fetch('/api/v1/scene-cfg/')
        try:
            return json.loads(r.get('body', '{}'))
        except:
            return r

    # ─── 长期任务 ──────────────────────

    def get_long_term_tasks(self, chat_id):
        r = self._fetch(f'/api/agent/chats/{chat_id}/long-term-tasks')
        try:
            return json.loads(r.get('body', '[]'))
        except:
            return r


# ─── 格式化输出 ─────────────────────────────────────────────────

def fmt(data, raw=False):
    """格式化输出。"""
    if raw or isinstance(data, str):
        print(data)
        return
    if isinstance(data, dict):
        if data.get('_binary'):
            print(f"[Binary data: {data['content_type']}, {len(data['data'])} bytes]")
            return
        if data.get('_error'):
            print(f"ERROR: {data.get('status', '')} {data.get('body', data.get('exception', ''))}")
            return
        if data.get('_text'):
            print(data['_text'])
            return
    print(json.dumps(data, ensure_ascii=False, indent=2))


def fmt_table(rows, columns=None):
    """格式化表格输出。"""
    if not rows:
        print("(empty)")
        return
    if not isinstance(rows[0], dict):
        for r in rows:
            print(r)
        return
    if columns is None:
        columns = list(rows[0].keys())
    # 计算列宽
    widths = {}
    for c in columns:
        widths[c] = max(len(str(c)), max(len(str(r.get(c, ''))) for r in rows))
    # 打印表头
    header = ' | '.join(str(c).ljust(widths[c]) for c in columns)
    print(header)
    print('-' * len(header))
    # 打印行
    for r in rows:
        line = ' | '.join(str(r.get(c, ''))[:widths[c]].ljust(widths[c]) for c in columns)
        print(line)


# ─── 命令处理 ───────────────────────────────────────────────────

def cmd_chats_list(args, zai, client):
    r = zai.list_chats(page=args.page, chat_type=args.type)
    if isinstance(r, dict) and 'data' in r:
        data = r['data']
        total = r.get('total', len(data))
        if isinstance(data, list) and data:
            fmt_table(data, ['id', 'title', 'type', 'created_at', 'updated_at', 'pinned', 'archived'])
            print(f"\nTotal: {total}")
        else:
            fmt(r)
    else:
        fmt(r)


def cmd_chats_create(args, zai, client):
    r = zai.create_chat(title=args.title, model=args.model)
    fmt(r)


def cmd_chats_delete(args, zai, client):
    for cid in args.chat_id:
        r = zai.delete_chat(cid)
        print(f"Delete {cid}: {r.get('id', r.get('status', r))}")


def cmd_chats_info(args, zai, client):
    r = zai.get_chat(args.chat_id)
    fmt(r)


def cmd_chats_search(args, zai, client):
    results = zai.search_chats(args.keyword)
    if results:
        fmt_table(results, ['id', 'title', 'type', 'created_at', 'updated_at'])
    else:
        print(f"No chats found matching '{args.keyword}'")


def cmd_chats_rename(args, zai, client):
    r = zai.rename_chat(args.chat_id, args.title)
    fmt(r)


def cmd_send(args, zai, client):
    """在浏览器中发送消息。"""
    # 导航到 z.ai
    print("Navigating to z.ai...")
    client.post('/agent/navigate-direct', {'url': 'https://chat.z.ai/'})
    time.sleep(5)

    # 如果指定了 chat_id，导航到该聊天
    if args.chat_id:
        client.post('/agent/navigate-direct', {'url': f'https://chat.z.ai/c/{args.chat_id}'})
        time.sleep(3)

    # 加载 cookies
    client.post('/agent/load-cookies')

    # 输入消息
    client.post('/agent/type', {
        'selector': '#chat-input',
        'text': args.message,
        'clear': True
    })
    time.sleep(1)

    # 点击发送
    client.post('/agent/eval', {
        'script': "(function(){var b=document.querySelector('button.sendMessageButton');if(b&&!b.disabled){b.click();return 'sent';}return 'fail';})()"
    })

    if args.wait > 0:
        print(f"Waiting {args.wait}s for response...")
        time.sleep(args.wait)

        # 获取最后一条 AI 回复
        r = client.post('/agent/eval', {
            'script': r"""(function(){
                var msgs = document.querySelectorAll('[class*="chat-assistant"]');
                if(msgs.length > 0){
                    var last = msgs[msgs.length-1];
                    var t = last.innerText.replace(/^正在思考\s*跳过\s*/, '').replace(/^思考过程\s*/, '');
                    return t.substring(0, 2000);
                }
                return '(no response yet)';
            })()"""
        })
        val = r
        if isinstance(val, dict) and 'value' in val:
            val = val['value']
        if isinstance(val, dict) and 'value' in val:
            val = val['value']
        print(f"\nAI Response:\n{val}")
    else:
        print("Message sent. Use --wait N to wait for response.")


def cmd_models(args, zai, client):
    r = zai.list_models()
    if isinstance(r, dict) and 'data' in r:
        models = r['data']
        if isinstance(models, list):
            rows = []
            for m in models:
                info = m.get('info', {})
                params = info.get('params', {}) if isinstance(info, dict) else {}
                rows.append({
                    'name': m.get('name', ''),
                    'display_name': m.get('display_name', m.get('name', '')),
                    'max_tokens': params.get('max_tokens', ''),
                    'owned_by': m.get('owned_by', ''),
                })
            fmt_table(rows, ['name', 'display_name', 'max_tokens', 'owned_by'])
        else:
            fmt(r)
    else:
        fmt(r)


def cmd_config(args, zai, client):
    r = zai.get_config()
    fmt(r)


def cmd_user_info(args, zai, client):
    r = zai.get_user_info()
    # 隐藏敏感字段
    if isinstance(r, dict):
        r.pop('profile_image_url', None)
    fmt(r)


def cmd_user_settings(args, zai, client):
    r = zai.get_settings()
    fmt(r)


def cmd_folders_list(args, zai, client):
    r = zai.list_folders()
    if isinstance(r, list):
        if r:
            fmt_table(r, ['id', 'name', 'created_at', 'updated_at'])
        else:
            print("(no folders)")
    else:
        fmt(r)


def cmd_folders_create(args, zai, client):
    r = zai.create_folder(args.name)
    fmt(r)


def cmd_folders_delete(args, zai, client):
    for fid in args.folder_id:
        r = zai.delete_folder(fid)
        print(f"Delete folder {fid}: {r.get('id', r.get('status', r))}")


def cmd_tags_list(args, zai, client):
    r = zai.list_tags()
    fmt(r)


def cmd_screenshot(args, zai, client):
    print("Taking screenshot...")
    r = client.post('/agent/screenshot-trusted', {'fullPage': args.full})
    if isinstance(r, dict) and '_binary' in r:
        out_path = args.output or 'screenshot.png'
        with open(out_path, 'wb') as f:
            f.write(r['data'])
        print(f"Saved to {out_path} ({len(r['data'])} bytes)")
    elif isinstance(r, bytes):
        out_path = args.output or 'screenshot.png'
        with open(out_path, 'wb') as f:
            f.write(r)
        print(f"Saved to {out_path} ({len(r)} bytes)")
    else:
        print(f"Error: {r}")


def cmd_network(args, zai, client):
    if args.action == 'enable':
        r = client.post('/agent/network/enable')
        print(f"Network capture: {r}")

    elif args.action == 'disable':
        r = client.post('/agent/network/disable')
        print(f"Network capture: {r}")

    elif args.action == 'clear':
        r = client.post('/agent/network/clear')
        print(f"Cleared: {r}")

    elif args.action == 'capture':
        # Enable + clear + wait + show
        client.post('/agent/network/enable')
        client.post('/agent/network/clear')
        wait = args.wait or 10
        print(f"Capturing for {wait}s...")
        time.sleep(wait)
        r = client.get('/agent/network/requests')
        reqs = r if isinstance(r, list) else r.get('requests', [])

        # Filter
        filtered = reqs
        if args.filter_url:
            filtered = [req for req in filtered if args.filter_url in req.get('url', '')]
        if args.filter_type:
            filtered = [req for req in filtered if req.get('resourceType', '') == args.filter_type]

        print(f"Captured {len(filtered)} requests")
        if args.with_body:
            fmt(filtered)
        else:
            # Summary only
            for req in filtered:
                url = req.get('url', '')
                method = req.get('method', '')
                rtype = req.get('resourceType', '')
                status = req.get('statusCode', '')
                has_post = bool(req.get('postData'))
                resp_size = req.get('responseSize', 0)
                print(f"  [{method:4s}] {rtype:8s} s={status} post={has_post} size={resp_size} {url[:120]}")

        if args.save:
            with open(args.save, 'w') as f:
                json.dump(filtered, f, ensure_ascii=False, indent=2)
            print(f"\nSaved to {args.save}")

    elif args.action == 'list':
        r = client.get('/agent/network/requests')
        reqs = r if isinstance(r, list) else r.get('requests', [])
        print(f"Total: {len(reqs)}")
        for req in reqs:
            url = req.get('url', '')
            method = req.get('method', '')
            rtype = req.get('resourceType', '')
            status = req.get('statusCode', '')
            print(f"  [{method:4s}] {rtype:8s} s={status} {url[:120]}")


def cmd_cookies(args, zai, client):
    params = {}
    if args.domain:
        params['domain'] = args.domain
    r = client.get('/agent/cookies', params)
    if isinstance(r, dict) and 'cookies' in r:
        cookies = r['cookies']
        print(f"Cookies: {len(cookies)}")
        for c in cookies:
            print(f"  {c.get('name', '?'):30s} = {str(c.get('value', ''))[:50]}  domain={c.get('domain', '')}  secure={c.get('secure', '')}")
    else:
        fmt(r)


def cmd_navigate(args, zai, client):
    r = client.post('/agent/navigate-direct', {'url': args.url})
    print(f"Navigate: {r}")
    if args.wait:
        time.sleep(args.wait)


def cmd_eval(args, zai, client):
    r = client.post('/agent/eval', {'script': args.code})
    fmt(r)


def cmd_health(args, zai, client):
    r = client.get('/agent/health')
    fmt(r)


def cmd_state(args, zai, client):
    r = client.get('/agent/state')
    fmt(r)


# ─── SSH 隧道支持 ───────────────────────────────────────────────

class SSHTunnel:
    """通过 SSH 建立到 samweb 的隧道（简化版，仅用于说明）。"""

    @staticmethod
    def parse(ssh_str):
        """解析 SSH 连接字符串: user:pass@host:port"""
        import re
        m = re.match(r'([^:]+):([^@]+)@([^:]+):(\d+)', ssh_str)
        if not m:
            return None
        return {
            'user': m.group(1),
            'password': m.group(2),
            'host': m.group(3),
            'port': int(m.group(4)),
        }


# ─── 主入口 ─────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(
        description='z.ai CLI - 通过 samweb 控制 z.ai',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    parser.add_argument('--host', default='127.0.0.1', help='samweb 地址')
    parser.add_argument('--port', type=int, default=7777, help='samweb 端口')
    parser.add_argument('--raw', action='store_true', help='原始 JSON 输出')

    sub = parser.add_subparsers(dest='command')

    # ── chats ──
    p_chats = sub.add_parser('chats', help='会话管理')
    p_chats_sub = p_chats.add_subparsers(dest='subcmd')

    p_list = p_chats_sub.add_parser('list', help='列出会话')
    p_list.add_argument('--page', type=int, default=1)
    p_list.add_argument('--type', default='default')

    p_create = p_chats_sub.add_parser('create', help='创建会话')
    p_create.add_argument('--title', default='新聊天')
    p_create.add_argument('--model', default='GLM-5-Turbo')

    p_delete = p_chats_sub.add_parser('delete', help='删除会话')
    p_delete.add_argument('chat_id', nargs='+')

    p_info = p_chats_sub.add_parser('info', help='会话详情')
    p_info.add_argument('chat_id')

    p_search = p_chats_sub.add_parser('search', help='搜索会话')
    p_search.add_argument('keyword')

    p_rename = p_chats_sub.add_parser('rename', help='重命名会话')
    p_rename.add_argument('chat_id')
    p_rename.add_argument('title')

    # ── send ──
    p_send = sub.add_parser('send', help='发送消息')
    p_send.add_argument('message', help='消息内容')
    p_send.add_argument('--chat-id', default=None, help='指定会话 ID')
    p_send.add_argument('--model', default=None, help='模型')
    p_send.add_argument('--wait', type=int, default=0, help='等待响应秒数')

    # ── models ──
    sub.add_parser('models', help='模型列表')

    # ── config ──
    sub.add_parser('config', help='查看配置')

    # ── user ──
    p_user = sub.add_parser('user', help='用户相关')
    p_user_sub = p_user.add_subparsers(dest='subcmd')
    p_user_sub.add_parser('info', help='用户信息')
    p_user_sub.add_parser('settings', help='用户设置')

    # ── folders ──
    p_folders = sub.add_parser('folders', help='文件夹管理')
    p_folders_sub = p_folders.add_subparsers(dest='subcmd')

    p_fl = p_folders_sub.add_parser('list', help='列出文件夹')
    p_fc = p_folders_sub.add_parser('create', help='创建文件夹')
    p_fc.add_argument('name')
    p_fd = p_folders_sub.add_parser('delete', help='删除文件夹')
    p_fd.add_argument('folder_id', nargs='+')

    # ── tags ──
    p_tags = sub.add_parser('tags', help='标签管理')
    p_tags_sub = p_tags.add_subparsers(dest='subcmd')
    p_tags_sub.add_parser('list', help='列出标签')

    # ── screenshot ──
    p_ss = sub.add_parser('screenshot', help='截图')
    p_ss.add_argument('--full', action='store_true', help='全页面截图')
    p_ss.add_argument('--output', '-o', default=None, help='输出文件路径')

    # ── network ──
    p_net = sub.add_parser('network', help='网络捕获')
    p_net.add_argument('action', choices=['enable', 'disable', 'clear', 'capture', 'list'])
    p_net.add_argument('--wait', type=int, default=10, help='捕获等待秒数')
    p_net.add_argument('--filter-url', help='URL 过滤')
    p_net.add_argument('--filter-type', help='资源类型过滤 (XHR/Fetch/Document/...)')
    p_net.add_argument('--with-body', action='store_true', help='包含完整请求体')
    p_net.add_argument('--save', help='保存到文件')

    # ── cookies ──
    p_ck = sub.add_parser('cookies', help='Cookie 管理')
    p_ck.add_argument('--domain', default=None, help='按域名过滤')

    # ── navigate ──
    p_nav = sub.add_parser('navigate', help='导航到 URL')
    p_nav.add_argument('url')
    p_nav.add_argument('--wait', type=int, default=0, help='等待秒数')

    # ── eval ──
    p_eval = sub.add_parser('eval', help='执行 JavaScript')
    p_eval.add_argument('code')

    # ── health ──
    sub.add_parser('health', help='健康检查')

    # ── state ──
    sub.add_parser('state', help='浏览器状态')

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        return

    client = SamwebClient(host=args.host, port=args.port)
    zai = ZaiAPI(client)

    dispatch = {
        'chats': {
            'list': cmd_chats_list,
            'create': cmd_chats_create,
            'delete': cmd_chats_delete,
            'info': cmd_chats_info,
            'search': cmd_chats_search,
            'rename': cmd_chats_rename,
        },
        'send': cmd_send,
        'models': cmd_models,
        'config': cmd_config,
        'user': {
            'info': cmd_user_info,
            'settings': cmd_user_settings,
        },
        'folders': {
            'list': cmd_folders_list,
            'create': cmd_folders_create,
            'delete': cmd_folders_delete,
        },
        'tags': {
            'list': cmd_tags_list,
        },
        'screenshot': cmd_screenshot,
        'network': cmd_network,
        'cookies': cmd_cookies,
        'navigate': cmd_navigate,
        'eval': cmd_eval,
        'health': cmd_health,
        'state': cmd_state,
    }

    handler = dispatch.get(args.command)
    if handler is None:
        parser.print_help()
        return

    if isinstance(handler, dict):
        subcmd = getattr(args, 'subcmd', None)
        if not subcmd:
            # Print subcommand help
            subparsers = {
                'chats': p_chats,
                'user': p_user,
                'folders': p_folders,
                'tags': p_tags,
            }
            sp = subparsers.get(args.command)
            if sp:
                sp.print_help()
            return
        fn = handler.get(subcmd)
        if fn:
            fn(args, zai, client)
        else:
            print(f"Unknown subcommand: {subcmd}")
    else:
        handler(args, zai, client)


if __name__ == '__main__':
    main()
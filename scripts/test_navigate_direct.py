#!/usr/bin/env python3
"""Test navigate-direct to z.ai + verify agent API works on z.ai page."""
import os, sys, socket, threading, select, time, asyncio, json

os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run
from shan_lib.agent import Agent

def main():
    # Start tunnel
    client, proc, _ = open_ssh(verbose=False)
    try:
        transport = client.get_transport()
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(('127.0.0.1', 9222))
        listener.listen(10)
        listener.settimeout(1)
        def forward(src, dst):
            try:
                src.settimeout(60)
                while True:
                    r, _, _ = select.select([src], [], [], 1)
                    if r:
                        data = src.recv(8192)
                        if not data: break
                        dst.sendall(data)
            except: pass
            finally:
                try: src.close()
                except: pass
                try: dst.close()
                except: pass
        stop = threading.Event()
        def accept_loop():
            while not stop.is_set():
                try:
                    ls, _ = listener.accept()
                    rc2 = transport.open_channel('direct-tcpip', ('127.0.0.1', 9222), ('127.0.0.1', 0))
                    threading.Thread(target=forward, args=(ls, rc2), daemon=True).start()
                    threading.Thread(target=forward, args=(rc2, ls), daemon=True).start()
                except socket.timeout: continue
                except: break
        threading.Thread(target=accept_loop, daemon=True).start()
        time.sleep(1)
        
        # Test 1: Use agent API to navigate-direct to z.ai
        print('=== Test 1: navigate-direct to z.ai via agent API ===')
        a = Agent(verbose=False)
        try:
            print('health:', a.get('/agent/health').get('status'))
            # navigate-direct uses window.location.href via WindowExecJS
            a.post('/agent/navigate-direct', {'url': 'https://chat.z.ai/'}, timeout=30)
            print('navigate-direct sent')
            time.sleep(10)
            
            # Check if agent API still works on the new page
            try:
                _, v = a.eval('(function(){return JSON.stringify({url: location.href, title: document.title, hasToken: !!localStorage.getItem("token")});})()', timeout=15)
                print(f'eval result: {v}')
            except Exception as e:
                print(f'eval failed: {e}')
            
            # Try state
            try:
                s = a.req('GET', '/agent/state', timeout=15)
                print(f'state: url={s.get("url")} title={s.get("title")}')
            except Exception as e:
                print(f'state failed: {e}')
        finally:
            a.close()
        
        # Test 2: Use Playwright to verify the page loaded
        print('\n=== Test 2: Verify via Playwright ===')
        from playwright.async_api import async_playwright
        async def pw_test():
            async with async_playwright() as p:
                browser = await p.chromium.connect_over_cdp('http://127.0.0.1:9222')
                page = browser.contexts[0].pages[0]
                print(f'page URL: {page.url}')
                print(f'page title: {await page.title()}')
                
                # Check if z.ai loaded
                url = page.url
                if 'z.ai' in url:
                    print('✅ z.ai is loaded!')
                    # Check login state
                    has_token = await page.evaluate('!!localStorage.getItem("token")')
                    print(f'logged in: {has_token}')
                    # Check if page has content
                    body_len = await page.evaluate('(document.body||{}).innerText?.length || 0')
                    print(f'body text length: {body_len}')
                    
                    # Take screenshot
                    await page.screenshot(path='/home/z/my-project/download/zai_navigate_direct.png')
                    print('screenshot saved')
                else:
                    print(f'❌ not on z.ai, URL is {url}')
                
                await browser.close()
        
        asyncio.run(pw_test())
        stop.set()
        listener.close()
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

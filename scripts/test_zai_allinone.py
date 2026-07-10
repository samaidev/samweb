#!/usr/bin/env python3
"""All-in-one: SSH tunnel + Playwright test for z.ai proxy."""
import os, sys, socket, threading, select, time, asyncio, json

os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run

def start_tunnel(client):
    """Start SSH tunnel in background thread."""
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
    
    def accept_loop():
        while True:
            try:
                local_sock, _ = listener.accept()
                remote_chan = transport.open_channel('direct-tcpip', ('127.0.0.1', 9222), ('127.0.0.1', 0))
                threading.Thread(target=forward, args=(local_sock, remote_chan), daemon=True).start()
                threading.Thread(target=forward, args=(remote_chan, local_sock), daemon=True).start()
            except socket.timeout:
                continue
            except:
                break
    
    t = threading.Thread(target=accept_loop, daemon=True)
    t.start()
    return listener

async def playwright_test():
    from playwright.async_api import async_playwright
    async with async_playwright() as p:
        browser = await p.chromium.connect_over_cdp('http://127.0.0.1:9222')
        page = browser.contexts[0].pages[0]
        
        # Check proxy response
        result = await page.evaluate('''() => {
            return new Promise(async (resolve) => {
                try {
                    var resp = await fetch('/proxy?url=https://chat.z.ai');
                    var text = await resp.text();
                    resolve({status: resp.status, len: text.length, hasInterceptor: text.indexOf('proxyPrefix') >= 0, first300: text.slice(0, 300)});
                } catch (e) { resolve({error: e.message}); }
            });
        }''')
        print(f'Proxy: status={result.get("status")} len={result.get("len")} interceptor={result.get("hasInterceptor")}')
        print(f'  first200: {result.get("first300","")[:200]}')
        
        # Navigate to z.ai
        await page.click('#omnibox')
        await page.fill('#omnibox', 'chat.z.ai')
        await page.press('#omnibox', 'Enter')
        
        # Poll for loading
        for i in range(15):
            await asyncio.sleep(5)
            for f in page.frames:
                if 'proxy' in f.url:
                    try:
                        ready = await f.evaluate('document.readyState')
                        if ready == 'complete':
                            body = await f.evaluate('(document.body||{}).innerText||""')
                            has_200 = '200: An unexpected' in body
                            print(f'[{(i+1)*5}s] complete! len={len(body)} has200={has_200}')
                            print(f'  body: {body[:300]}')
                            await page.screenshot(path='/home/z/my-project/download/zai_final.png')
                            print('screenshot saved')
                            await browser.close()
                            return
                        else:
                            print(f'[{(i+1)*5}s] ready={ready}')
                    except Exception as e:
                        print(f'[{(i+1)*5}s] eval: {e}')
                    break
        
        print('timeout')
        await browser.close()

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # Start tunnel
        listener = start_tunnel(client)
        print('tunnel ready', flush=True)
        time.sleep(1)
        
        # Run Playwright test
        asyncio.run(playwright_test())
        
        listener.close()
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    main()

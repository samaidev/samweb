#!/usr/bin/env python3
"""Upload proxy.go + build + restart + test."""
import os, sys, time, socket, threading, select, asyncio, json

os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # Upload
        sftp = client.open_sftp()
        with open('/home/z/my-project/samweb/internal/proxy/proxy.go', 'rb') as f:
            data = f.read()
        with sftp.open('/C:/samweb/internal/proxy/proxy.go', 'wb') as f:
            f.write(data)
        print(f'uploaded proxy.go ({len(data)} bytes)')
        sftp.close()
        
        # Build
        print('building...', flush=True)
        rc, out, err = run(client, 'cd /d C:\\samweb && go build -tags desktop,production -ldflags "-w -s -H windowsgui" -o samweb.exe ./cmd/samweb 2>&1', timeout=300)
        print(f'build rc={rc}')
        if rc != 0: print(out[:500]); return 1
        
        # Restart
        run(client, 'taskkill /F /IM samweb.exe 2>nul')
        run(client, 'taskkill /F /IM msedgewebview2.exe 2>nul')
        time.sleep(5)
        run(client, 'schtasks /Run /TN RestartSamweb')
        time.sleep(20)
        
        rc, out, err = run(client, 'netstat -an | findstr :9222 | findstr LISTENING')
        print(f'CDP: {"LISTENING" if "LISTENING" in out else "not listening"}')
        
        # Start tunnel
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
        
        t = threading.Thread(target=accept_loop, daemon=True)
        t.start()
        time.sleep(1)
        
        # Playwright test
        from playwright.async_api import async_playwright
        
        async def pw_test():
            async with async_playwright() as p:
                browser = await p.chromium.connect_over_cdp('http://127.0.0.1:9222')
                page = browser.contexts[0].pages[0]
                
                await page.click('#omnibox')
                await page.fill('#omnibox', 'chat.z.ai')
                await page.press('#omnibox', 'Enter')
                
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
        
        asyncio.run(pw_test())
        stop.set()
        listener.close()
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

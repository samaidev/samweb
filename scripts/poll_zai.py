#!/usr/bin/env python3
"""Poll z.ai loading via CDP."""
import asyncio, json
from playwright.async_api import async_playwright

async def main():
    async with async_playwright() as p:
        browser = await p.chromium.connect_over_cdp('http://127.0.0.1:9222')
        page = browser.contexts[0].pages[0]
        
        await page.click('#omnibox')
        await page.fill('#omnibox', 'chat.z.ai')
        await page.press('#omnibox', 'Enter')
        
        for i in range(15):
            await asyncio.sleep(5)
            proxy_frame = None
            for f in page.frames:
                if 'proxy' in f.url:
                    proxy_frame = f
                    break
            
            if not proxy_frame:
                print(f'[{(i+1)*5}s] no proxy frame', flush=True)
                continue
            
            try:
                ready = await proxy_frame.evaluate('document.readyState')
                if ready == 'complete':
                    body = await proxy_frame.evaluate('(document.body||{}).innerText||""')
                    has_200 = '200: An unexpected' in body
                    print(f'[{(i+1)*5}s] complete! len={len(body)} has200={has_200}', flush=True)
                    print(f'  body: {body[:300]}', flush=True)
                    await page.screenshot(path='/home/z/my-project/download/zai_final.png')
                    print('screenshot saved', flush=True)
                    await browser.close()
                    return
                else:
                    print(f'[{(i+1)*5}s] ready={ready}', flush=True)
            except Exception as e:
                print(f'[{(i+1)*5}s] eval error: {e}', flush=True)
        
        print('timeout', flush=True)
        await browser.close()

asyncio.run(main())

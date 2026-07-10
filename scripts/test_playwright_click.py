#!/usr/bin/env python3
"""Test samweb left-click using Playwright via CDP tunnel."""
import asyncio
import json
import sys
from playwright.async_api import async_playwright

CDP_URL = "http://127.0.0.1:9222"

async def main():
    async with async_playwright() as p:
        # Connect to the running WebView2 via CDP
        browser = await p.chromium.connect_over_cdp(CDP_URL)
        
        # Get the existing page (samweb UI)
        contexts = browser.contexts
        if not contexts:
            print("No browser contexts found")
            return 1
        
        pages = contexts[0].pages
        if not pages:
            print("No pages found")
            return 1
        
        page = pages[0]
        print(f"Connected to: {page.url}")
        print(f"Title: {await page.title()}")
        
        # Check DOM state
        state = await page.evaluate("""() => {
            return {
                url: location.href,
                hasTabStrip: !!document.getElementById('tab-strip'),
                hasNewTabBtn: !!document.getElementById('new-tab-btn'),
                hasOmnibox: !!document.getElementById('omnibox'),
                tabCount: document.querySelectorAll('.tab').length,
                bodyPointerEvents: getComputedStyle(document.body).pointerEvents,
                bodyUserSelect: getComputedStyle(document.body).userSelect,
            };
        }""")
        print(f"\nDOM state: {json.dumps(state, indent=2)}")
        
        # Test 1: JS click (should work)
        print("\n=== Test 1: JS click on new-tab button ===")
        tabs_before = await page.evaluate("document.querySelectorAll('.tab').length")
        await page.evaluate("document.getElementById('new-tab-btn').click()")
        tabs_after = await page.evaluate("document.querySelectorAll('.tab').length")
        print(f"  tabs before={tabs_before} after={tabs_after} worked={tabs_after > tabs_before}")
        
        # Test 2: Playwright click (real mouse click)
        print("\n=== Test 2: Playwright click on new-tab button ===")
        tabs_before = await page.evaluate("document.querySelectorAll('.tab').length")
        try:
            await page.click("#new-tab-btn", timeout=5000)
            tabs_after = await page.evaluate("document.querySelectorAll('.tab').length")
            print(f"  tabs before={tabs_before} after={tabs_after} worked={tabs_after > tabs_before}")
        except Exception as e:
            print(f"  click failed: {e}")
        
        # Test 3: Playwright click on tab close button
        print("\n=== Test 3: Playwright click on tab close button ===")
        tabs_before = await page.evaluate("document.querySelectorAll('.tab').length")
        try:
            await page.click(".tab-close", timeout=5000)
            tabs_after = await page.evaluate("document.querySelectorAll('.tab').length")
            print(f"  tabs before={tabs_before} after={tabs_after} worked={tabs_after < tabs_before}")
        except Exception as e:
            print(f"  click failed: {e}")
        
        # Test 4: Playwright click on omnibox and type
        print("\n=== Test 4: Click omnibox and type ===")
        try:
            await page.click("#omnibox", timeout=5000)
            await page.fill("#omnibox", "baidu.com")
            value = await page.evaluate("document.getElementById('omnibox').value")
            print(f"  omnibox value: {value}")
            print(f"  typed successfully: {value == 'baidu.com'}")
        except Exception as e:
            print(f"  omnibox test failed: {e}")
        
        # Test 5: Check console errors
        print("\n=== Test 5: Check for JS console errors ===")
        errors = await page.evaluate("""() => {
            return window.__samwebErrors || 'no error tracking';
        }""")
        print(f"  errors: {errors}")
        
        # Take screenshot
        await page.screenshot(path="/home/z/my-project/download/samweb_playwright_test.png")
        print("\n=== Screenshot saved ===")
        
        await browser.close()
        return 0

asyncio.run(main())

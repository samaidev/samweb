#!/usr/bin/env python3
"""Check if z.ai cookies are still in samweb's CDP cookie store
(meaning the user was previously logged in). Also check what cookies
exist for z.ai / chatglm.cn domains.
"""
import json
import sys

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        # /agent/cookies returns all cookies
        print("[1] /agent/cookies ...")
        try:
            r = a.get("/agent/cookies", timeout=30)
            if isinstance(r, dict) and "cookies" in r:
                cookies = r["cookies"]
            elif isinstance(r, list):
                cookies = r
            else:
                cookies = []
            print(f"   total cookies: {len(cookies)}")
            # Filter z.ai / chatglm.cn
            zai_cookies = [c for c in cookies if "z.ai" in c.get("domain", "") or "chatglm" in c.get("domain", "")]
            print(f"   z.ai / chatglm cookies: {len(zai_cookies)}")
            for c in zai_cookies[:30]:
                print(f"     {c.get('domain')}: {c.get('name')}={c.get('value','')[:40]}...")
        except Exception as e:
            print("   ERR:", e)
    finally:
        a.close()


if __name__ == "__main__":
    main()

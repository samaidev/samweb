#!/usr/bin/env python3
"""Test the new /agent/cdp-navigate-top endpoint.
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        # 1) Call cdp-navigate-top to go to z.ai
        print("[1] /agent/cdp-navigate-top https://chat.z.ai ...")
        try:
            r = a.post("/agent/cdp-navigate-top", {"url": "https://chat.z.ai"}, timeout=20)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

        # 2) Wait for load
        print("\n[2] waiting 6s ...")
        time.sleep(6)

        # 3) Take screenshot
        print("\n[3] /agent/screenshot-trusted ...")
        try:
            r = a.post("/agent/screenshot-trusted", {"fullPage": False}, timeout=30)
            if isinstance(r, (bytes, bytearray)):
                path = "/home/z/my-project/download/samweb_zai_top.png"
                with open(path, "wb") as f:
                    f.write(r)
                print(f"   saved to {path} ({len(r)} bytes)")
        except Exception as e:
            print("   ERR:", e)

        # 4) Navigate back to samweb UI
        print("\n[4] /agent/cdp-navigate-top http://wails.localhost/ ...")
        try:
            r = a.post("/agent/cdp-navigate-top", {"url": "http://wails.localhost/"}, timeout=20)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

    finally:
        a.close()


if __name__ == "__main__":
    main()

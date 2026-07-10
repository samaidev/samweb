#!/usr/bin/env python3
"""Use navigate-direct to load z.ai directly in the iframe (bypass proxy),
wait, then check state + take a screenshot.
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        # 1) navigate-direct to z.ai
        print("[1] /agent/navigate-direct https://chat.z.ai ...")
        try:
            r = a.post("/agent/navigate-direct", {"url": "https://chat.z.ai"}, timeout=15)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

        # wait for load
        print("\n[2] waiting 8s for z.ai to load ...")
        time.sleep(8)

        # 3) state
        print("\n[3] /agent/state ...")
        try:
            st = a.state()
            print(f"   URL   : {st.get('url','(空)')}")
            print(f"   Title : {st.get('title','(空)')}")
            print(f"   Tabs  : {st.get('tabs')}")
            print(f"   iframe_src    : {st.get('iframe_src','(空)')}")
            print(f"   omnibox_value : {st.get('omnibox_value','(空)')}")
        except Exception as e:
            print("   ERR:", e)

        # 4) screenshot
        print("\n[4] /agent/screenshot-trusted ...")
        try:
            r = a.post("/agent/screenshot-trusted", {"fullPage": False}, timeout=30)
            if isinstance(r, (bytes, bytearray)):
                path = "/home/z/my-project/download/samweb_zai_direct.png"
                with open(path, "wb") as f:
                    f.write(r)
                print(f"   saved to {path} ({len(r)} bytes)")
        except Exception as e:
            print("   ERR:", e)

    finally:
        a.close()


if __name__ == "__main__":
    main()

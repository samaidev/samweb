#!/usr/bin/env python3
"""Now that the dispatch pipeline is fixed, query /agent/state properly
via the SSH-tunneled Agent client. Also navigate to z.ai and report
the URL back to the user.
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        # 1) /agent/state — current state
        print("[1] /agent/state ...")
        try:
            st = a.state()
            print("   ->", json.dumps(st, ensure_ascii=False, indent=2))
            print()
            print(f"=== 当前 URL   : {st.get('url','(空)')}")
            print(f"=== 当前 Title : {st.get('title','(空)')}")
        except Exception as e:
            print("   ERR:", e)

        # 2) /agent/eval with a script that doesn't need iframe
        print("\n[2] /agent/eval (window.location.href) ...")
        try:
            r = a.post("/agent/eval", {"script": "window.location.href"}, timeout=15)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

        # 3) Navigate to z.ai via /agent/navigate (uses proxy iframe)
        print("\n[3] /agent/navigate https://chat.z.ai ...")
        try:
            r = a.post("/agent/navigate", {"url": "https://chat.z.ai"}, timeout=30)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

        time.sleep(3)  # let the page load

        # 4) /agent/state again
        print("\n[4] /agent/state after navigate ...")
        try:
            st = a.state()
            print("   ->", json.dumps(st, ensure_ascii=False, indent=2))
            print()
            print(f"=== 当前 URL   : {st.get('url','(空)')}")
            print(f"=== 当前 Title : {st.get('title','(空)')}")
        except Exception as e:
            print("   ERR:", e)

        # 5) Try /agent/navigate-direct to bypass proxy
        print("\n[5] /agent/navigate-direct https://chat.z.ai ...")
        try:
            r = a.post("/agent/navigate-direct", {"url": "https://chat.z.ai"}, timeout=30)
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

        time.sleep(5)

        # 6) /agent/state once more
        print("\n[6] /agent/state after navigate-direct ...")
        try:
            st = a.state()
            print("   ->", json.dumps(st, ensure_ascii=False, indent=2))
            print()
            print(f"=== 当前 URL   : {st.get('url','(空)')}")
            print(f"=== 当前 Title : {st.get('title','(空)')}")
        except Exception as e:
            print("   ERR:", e)

    finally:
        a.close()


if __name__ == "__main__":
    main()

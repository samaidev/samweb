#!/usr/bin/env python3
"""Take a screenshot of the current samweb state via /agent/screenshot-trusted
(CDP-based, bypasses the dispatch layer entirely). Save to download/.
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        # Try trusted screenshot (CDP-based)
        print("[1] /agent/screenshot-trusted ...")
        try:
            r = a.post("/agent/screenshot-trusted", {"fullPage": False}, timeout=30)
            print(f"   -> type={type(r).__name__}, len={len(r) if hasattr(r,'__len__') else '?'}")
            if isinstance(r, (bytes, bytearray)):
                path = "/home/z/my-project/download/samweb_current.png"
                with open(path, "wb") as f:
                    f.write(r)
                print(f"   saved to {path}")
            elif isinstance(r, dict):
                print("   response:", json.dumps(r, ensure_ascii=False)[:300])
        except Exception as e:
            print("   ERR:", e)

        # Also try the non-trusted version (uses dispatch)
        print("\n[2] /agent/screenshot (dispatch) ...")
        try:
            r = a.post("/agent/screenshot", {"fullPage": False}, timeout=15)
            if isinstance(r, dict) and r.get("data"):
                import base64
                png = base64.b64decode(r["data"])
                path = "/home/z/my-project/download/samweb_current_dispatch.png"
                with open(path, "wb") as f:
                    f.write(png)
                print(f"   saved to {path}")
            else:
                print("   response:", json.dumps(r, ensure_ascii=False)[:300])
        except Exception as e:
            print("   ERR:", e)

        # Get state
        print("\n[3] /agent/state ...")
        st = a.state()
        print(f"   URL   : {st.get('url','(空)')}")
        print(f"   Title : {st.get('title','(空)')}")
        print(f"   iframe_src    : {st.get('iframe_src','(空)')}")
        print(f"   omnibox_value : {st.get('omnibox_value','(空)')}")

    finally:
        a.close()


if __name__ == "__main__":
    main()

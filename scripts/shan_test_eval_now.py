#!/usr/bin/env python3
"""Test whether /agent/eval is finally working (the front-end should be
ready now that samweb has been up for a while).
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    a = Agent(verbose=False)
    try:
        print("[1] /agent/health")
        r = a.get("/agent/health")
        print("   ->", r)

        # Try a very simple eval
        print("\n[2] /agent/eval (1+1) ...")
        try:
            r = a.post("/agent/eval", {"script": "1+1"}, timeout=15)
            print("   ->", r)
        except Exception as e:
            print("   ERR:", e)

        # Try reading omnibox directly
        print("\n[3] /agent/eval (omnibox + iframe.src) ...")
        script = (
            "(function(){"
            "  var omnibox = parent.document.getElementById('omnibox');"
            "  var view = parent.document.getElementById('view');"
            "  return JSON.stringify({"
            "    omnibox_value: omnibox ? omnibox.value : null,"
            "    iframe_src: view ? view.src : null,"
            "    parent_title: parent.document.title,"
            "    has_methods: !!(parent.window.__samwebAgent && parent.window.__samwebAgent.methods),"
            "    method_names: parent.window.__samwebAgent ? Object.keys(parent.window.__samwebAgent.methods) : []"
            "  });"
            "})()"
        )
        try:
            r = a.post("/agent/eval", {"script": script}, timeout=15)
            print("   ->", json.dumps(r, ensure_ascii=False)[:600])
        except Exception as e:
            print("   ERR:", e)

        # Try /agent/state
        print("\n[4] /agent/state ...")
        try:
            r = a.state()
            print("   ->", json.dumps(r, ensure_ascii=False))
        except Exception as e:
            print("   ERR:", e)

    finally:
        a.close()


if __name__ == "__main__":
    main()

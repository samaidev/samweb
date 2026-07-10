#!/usr/bin/env python3
"""Use the SSH-tunneled Agent client to call /agent/eval directly.

This avoids PowerShell quoting hell. The /agent/state endpoint is broken
(state method missing from wails bootstrapJS); we /agent/eval a snippet
to read omnibox + iframe.src, then install a state method.
"""
import json
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    verbose = "-v" in sys.argv
    a = Agent(verbose=verbose)
    try:
        # 1) sanity: simple eval
        print("[1] eval 1+1 ...")
        try:
            r = a.post("/agent/eval", {"script": "1+1"}, timeout=30)
            print("   ->", json.dumps(r, ensure_ascii=False)[:200])
        except Exception as e:
            print("   ERR:", e)
            return

        # 2) read omnibox + iframe.src from parent window
        print("\n[2] reading current omnibox + iframe.src ...")
        script = (
            "(function(){"
            "  try {"
            "    var omnibox = parent.document.getElementById('omnibox');"
            "    var view = parent.document.getElementById('view');"
            "    return JSON.stringify({"
            "      omnibox_value: omnibox ? omnibox.value : null,"
            "      iframe_src: view ? view.src : null,"
            "      iframe_srcdoc_present: view ? (!!view.srcdoc) : null,"
            "      parent_title: parent.document.title,"
            "      has_methods: !!(parent.window.__samwebAgent && parent.window.__samwebAgent.methods),"
            "      method_names: parent.window.__samwebAgent ? Object.keys(parent.window.__samwebAgent.methods) : []"
            "    });"
            "  } catch(e) { return 'EXC:' + e.message; }"
            "})()"
        )
        try:
            r = a.post("/agent/eval", {"script": script}, timeout=30)
            print("   ->", json.dumps(r, ensure_ascii=False)[:600])
        except Exception as e:
            print("   ERR:", e)

        # 3) install state method + fix navigateDirect
        print("\n[3] installing state + navigateDirect methods ...")
        install_script = (
            "(function(){"
            "  parent.window.__samwebAgent.methods.state = function() {"
            "    var omnibox = parent.document.getElementById('omnibox');"
            "    var view = parent.document.getElementById('view');"
            "    var url = omnibox ? omnibox.value : '';"
            "    var title = '';"
            "    try {"
            "      var d = view ? view.contentDocument : null;"
            "      if (d && d.title) title = d.title;"
            "    } catch(e) {}"
            "    return { url: url, title: title, tabs: [], activeTab: 0, canBack: false, canForward: false };"
            "  };"
            "  parent.window.__samwebAgent.methods.navigateDirect = function(p) {"
            "    var view = parent.document.getElementById('view');"
            "    if (view) { view.src = p.url; }"
            "    return { ok: true };"
            "  };"
            "  return JSON.stringify({installed: true, methods: Object.keys(parent.window.__samwebAgent.methods)});"
            "})()"
        )
        try:
            r = a.post("/agent/eval", {"script": install_script}, timeout=30)
            print("   ->", json.dumps(r, ensure_ascii=False)[:400])
        except Exception as e:
            print("   ERR:", e)

        # 4) now hit /agent/state
        print("\n[4] GET /agent/state ...")
        try:
            st = a.state()
            print("   ->", json.dumps(st, ensure_ascii=False)[:600])
            print()
            print(f"=== 当前 URL   : {st.get('url','(空)')}")
            print(f"=== 当前 Title : {st.get('title','(空)')}")
        except Exception as e:
            print("   ERR:", e)

    finally:
        a.close()


if __name__ == "__main__":
    main()

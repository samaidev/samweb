#!/usr/bin/env python3
"""Use /agent/eval to query samweb's current state and install a state method.

The /agent/state endpoint is broken (the `state` method is missing from
the wails bootstrapJS methods dict). We /agent/eval a small JS snippet
that reads omnibox + iframe.src from the parent window, then installs a
state method so future /agent/state calls work.
"""
import base64
import json
import sys

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run

TOKEN = "test-token-2026"


def eval_via_shan(client, script, timeout=30):
    """POST /agent/eval from shan itself via PowerShell.

    Writes the request body to a temp file on shan to avoid quoting hell,
    then has PowerShell POST that file's contents.
    """
    body_json = json.dumps({"script": script})
    # Build a PS command that writes the JSON to disk then POSTs it.
    ps_lines = [
        "$body = @'",
        body_json,
        "'@",
        "$body | Out-File -Encoding utf8 -FilePath C:\\samweb\\eval_body.json",
        "try {",
        "  $r = Invoke-WebRequest -Uri 'http://127.0.0.1:7777/agent/eval'",
        f"    -Method Post -Body (Get-Content C:\\samweb\\eval_body.json -Raw)",
        f"    -TimeoutSec {timeout} -ContentType 'application/json' -UseBasicParsing",
        f"    -Headers @{{Authorization='Bearer {TOKEN}'}}",
        "  Write-Output $r.Content",
        "} catch {",
        "  Write-Output ('ERR:' + $_.Exception.Message)",
        "}",
    ]
    ps_script = "; ".join(ps_lines)
    cmd = f'powershell -Command "{ps_script}"'
    rc, out, err = run(client, cmd, timeout=timeout + 20)
    return out.strip()


def get_state_via_shan(client, timeout=15):
    ps = (
        "powershell -Command \"try {"
        f" (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/state -TimeoutSec {timeout}"
        f" -UseBasicParsing -Headers @{{Authorization='Bearer {TOKEN}'}}).Content"
        " } catch { Write-Output ('ERR:' + $_.Exception.Message) }\""
    )
    rc, out, err = run(client, ps, timeout=timeout + 15)
    return out.strip()


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) sanity: eval simple expression
        print("[1] eval 1+1 ...")
        out = eval_via_shan(client, "1+1")
        print("   ->", out[:200])

        # 2) read omnibox value + iframe.src from parent window
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
        out = eval_via_shan(client, script)
        print("   ->", out[:600])

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
        out = eval_via_shan(client, install_script)
        print("   ->", out[:400])

        # 4) now hit /agent/state
        print("\n[4] GET /agent/state ...")
        out = get_state_via_shan(client)
        print("   ->", out[:600])
        try:
            st = json.loads(out)
            print()
            print(f"=== 当前 URL   : {st.get('url','(空)')}")
            print(f"=== 当前 Title : {st.get('title','(空)')}")
        except Exception:
            pass
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

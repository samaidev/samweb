#!/usr/bin/env python3
"""Wait for samweb's /agent/state to be ready and print the current URL.

Runs everything from shan itself (no SSH tunnel needed for HTTP), polling
every 3s for up to 60s, then dumps the state JSON.
"""
import json
import sys
import time
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


CMD = (
    'powershell -Command "'
    "try {"
    "  $r = Invoke-WebRequest -Uri 'http://127.0.0.1:7777/agent/state' "
    "    -TimeoutSec 10 -UseBasicParsing -Headers @{Authorization='Bearer test-token-2026'};"
    "  Write-Output $r.Content"
    "} catch {"
    "  Write-Output ('ERR:' + $_.Exception.Message)"
    "}"
    '"'
)


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        for i in range(20):
            rc, out, err = run(client, CMD, timeout=20)
            out = out.strip()
            if out and not out.startswith("ERR:"):
                print(f"[ok after ~{i*3}s]")
                try:
                    st = json.loads(out)
                except Exception:
                    print("raw:", out)
                    return
                print(json.dumps(st, ensure_ascii=False, indent=2))
                print()
                print(f"=== 当前 URL   : {st.get('url','(空)')}")
                print(f"=== 当前 Title : {st.get('title','(空)')}")
                tabs = st.get("tabs", [])
                if tabs:
                    print(f"\n=== 全部标签页（active={st.get('activeTab')}）===")
                    for t in tabs:
                        mark = "*" if t.get("id") == st.get("activeTab") else " "
                        print(f" {mark} [{t.get('id')}] {t.get('title','')}")
                        print(f"     {t.get('url','')}")
                return
            print(f"[{i*3}s] {out[:100]}")
            time.sleep(3)
        print("[timeout] /agent/state never became ready")
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

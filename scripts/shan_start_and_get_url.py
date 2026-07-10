#!/usr/bin/env python3
"""Start samweb on shan via schtasks and poll /agent/health until ready."""
import sys
import time
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run
from shan_lib.agent import Agent


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) Run the schtask
        print("[1] triggering RestartSamweb...")
        rc, out, err = run(client, 'schtasks /Run /TN RestartSamweb', timeout=15)
        print(out, err)

        # 2) Poll tasklist for ~30s
        for i in range(15):
            time.sleep(2)
            rc, out, _ = run(client, 'tasklist /FI "IMAGENAME eq samweb.exe" /FO CSV /NH', timeout=10)
            if "samweb.exe" in out:
                print(f"[2] samweb.exe is running after ~{(i+1)*2}s")
                break
            print(f"[2] not yet ({(i+1)*2}s)...")
        else:
            print("[2] samweb.exe did NOT start")
            return

        # 3) Wait for agent API
        print("[3] waiting for /agent/health...")
        for i in range(15):
            time.sleep(2)
            rc, out, _ = run(client,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR:\' + $_.Exception.Message }"',
                timeout=15)
            out = out.strip()
            if out and not out.startswith("ERR:"):
                print(f"[3] health OK after ~{(i+1)*2}s: {out}")
                break
            print(f"[3] still not ready ({(i+1)*2}s): {out[:80]}")
        else:
            print("[3] agent API never came up")
            return
    finally:
        client.close()
        if proc:
            proc.terminate()

    # 4) Now query state via SSH-tunneled Agent client
    print("\n[4] querying /agent/state via tunnel...")
    a = Agent(verbose=False)
    try:
        st = a.state()
        import json
        print(json.dumps(st, ensure_ascii=False, indent=2))
        print()
        print(f"=== 当前 URL   : {st.get('url','(空)')}")
        print(f"=== 当前 Title : {st.get('title','(空)')}")
    finally:
        a.close()


if __name__ == "__main__":
    main()

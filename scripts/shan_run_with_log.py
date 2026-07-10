#!/usr/bin/env python3
"""Run samweb.exe from SSH with stdout/stderr captured, to see if
OnDomReady fires and what errors the wails app prints.

We start it as a background process (no /wait), redirect output to a
log file, wait ~10s, then dump the log.
"""
import os
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) Kill any running instance
        print("[1] killing samweb.exe ...")
        rc, out, _ = run(client, 'taskkill /F /IM samweb.exe', timeout=10)
        print(out)
        time.sleep(2)

        # 2) Launch with output redirection, detached
        print("\n[2] launching samweb.exe with logging ...")
        # Use cmd /c start to detach, redirect to log file
        launch = (
            'cmd /c "cd /d C:\\samweb && '
            'set WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222 --remote-allow-origins=* && '
            'start /B "" samweb.exe > C:\\samweb\\run.log 2>&1"'
        )
        rc, out, err = run(client, launch, timeout=15)
        print(f"   rc={rc} out={out!r} err={err!r}")

        # 3) Wait for log file to populate
        print("\n[3] waiting for log output ...")
        for i in range(10):
            time.sleep(2)
            rc, out, _ = run(client, 'if exist C:\\samweb\\run.log (type C:\\samweb\\run.log) else (echo NO_LOG)', timeout=10)
            if "NO_LOG" not in out and out.strip():
                print(f"--- after ~{(i+1)*2}s ---")
                print(out)
                if "[browser]" in out or "wails" in out.lower() or "error" in out.lower():
                    break
        else:
            print("--- final log ---")
            print(out)

        # 4) Check tasklist + health
        rc, out, _ = run(client, 'tasklist /FI "IMAGENAME eq samweb.exe" /FO CSV /NH', timeout=10)
        print("\n[4] samweb.exe:", out.strip())

        rc, out, _ = run(client,
            'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 5 -UseBasicParsing).Content } catch { \'ERR:\' + $_.Exception.Message }"',
            timeout=15)
        print("[5] /agent/health:", out.strip())
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

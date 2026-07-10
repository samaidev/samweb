#!/usr/bin/env python3
"""Upload the patched browser.go to C:\\samweb\\internal\\browser\\, run
go build to produce samweb.exe, swap it in, and restart samweb.
"""
import os
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


LOCAL_BROWSER_GO = "/home/z/my-project/samweb/internal/browser/browser.go"
REMOTE_BROWSER_GO = "C:/samweb/internal/browser/browser.go"


def main():
    verbose = "-v" in sys.argv
    client, proc, _ = open_ssh(verbose=verbose)
    try:
        # 1) Backup + upload new browser.go
        print("[1] backing up + uploading browser.go ...")
        rc, out, _ = run(client, f'copy /Y {REMOTE_BROWSER_GO} {REMOTE_BROWSER_GO}.bak', timeout=10)
        print(out)

        sftp = client.open_sftp()
        with open(LOCAL_BROWSER_GO, "rb") as f:
            content = f.read()
        with sftp.file(REMOTE_BROWSER_GO, "wb") as f:
            f.write(content)
        sftp.close()
        print(f"   uploaded {len(content)} bytes")

        # Verify the new file is in place
        rc, out, _ = run(client, f'findstr /N "navigateDirect" {REMOTE_BROWSER_GO}', timeout=10)
        print("   verify navigateDirect:", out[:200])

        # 2) Kill running samweb (so the exe can be replaced)
        print("\n[2] killing samweb.exe ...")
        rc, out, _ = run(client, 'taskkill /F /IM samweb.exe', timeout=10)
        print(out)
        time.sleep(2)

        # 3) Build
        print("\n[3] go build (this may take 30-90s) ...")
        # Use cmd /c "cd /d C:\samweb && go build ..." to set working dir
        build_cmd = (
            'cmd /c "cd /d C:\\samweb && '
            'set GOOS=windows&& set GOARCH=amd64&& set CGO_ENABLED=1&& '
            'go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb 2>&1"'
        )
        rc, out, err = run(client, build_cmd, timeout=180)
        print(f"   rc={rc}")
        print(out[-3000:] if out else "(no stdout)")
        if err.strip():
            print("STDERR:", err[-1000:])

        # 4) Check if samweb.exe.new was created
        rc, out, _ = run(client, 'dir C:\\samweb\\samweb.exe.new', timeout=10)
        print("\n[4] samweb.exe.new:")
        print(out)
        if "samweb.exe.new" not in out:
            print("   BUILD FAILED — new exe not found")
            return

        # 5) Swap in the new exe
        print("\n[5] swapping in new exe ...")
        rc, out, _ = run(client,
            'move /Y C:\\samweb\\samweb.exe C:\\samweb\\samweb.exe~ && '
            'move /Y C:\\samweb\\samweb.exe.new C:\\samweb\\samweb.exe',
            timeout=15)
        print(out)

        # 6) Restart via schtask
        print("\n[6] restarting samweb via schtask ...")
        rc, out, _ = run(client, 'schtasks /Run /TN RestartSamweb', timeout=10)
        print(out)

        # 7) Wait for /agent/health
        print("\n[7] waiting for /agent/health ...")
        for i in range(20):
            time.sleep(2)
            rc, out, _ = run(client,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR\' }"',
                timeout=10)
            if "ok" in out:
                print(f"   ready after ~{(i+1)*2}s: {out.strip()}")
                break
        else:
            print("   NEVER ready")
            return

    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

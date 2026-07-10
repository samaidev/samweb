#!/usr/bin/env python3
"""Sync all changed samweb source files to C:\\samweb\\, rebuild, deploy.

Uploads the patched files, runs `go build`, swaps in the new exe, and
restarts the schtask.
"""
import os
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run

# (local path, remote path)
FILES = [
    ("/home/z/my-project/cmd/samweb/main.go",                 "C:/samweb/cmd/samweb/main.go"),
    ("/home/z/my-project/internal/browser/browser.go",        "C:/samweb/internal/browser/browser.go"),
    ("/home/z/my-project/internal/browser/wails_backend.go",  "C:/samweb/internal/browser/wails_backend.go"),
    ("/home/z/my-project/internal/browser/profiles.go",       "C:/samweb/internal/browser/profiles.go"),
    ("/home/z/my-project/internal/browser/tab_worker.go",     "C:/samweb/internal/browser/tab_worker.go"),
    ("/home/z/my-project/internal/agent/server.go",           "C:/samweb/internal/agent/server.go"),
    ("/home/z/my-project/internal/agent/backend.go",          "C:/samweb/internal/agent/backend.go"),
    ("/home/z/my-project/internal/agent/mock_backend.go",     "C:/samweb/internal/agent/mock_backend.go"),
    ("/home/z/my-project/internal/agent/types.go",            "C:/samweb/internal/agent/types.go"),
    ("/home/z/my-project/internal/cdp/client.go",             "C:/samweb/internal/cdp/client.go"),
    ("/home/z/my-project/internal/browser/ui/index.html",     "C:/samweb/internal/browser/ui/index.html"),
    ("/home/z/my-project/internal/browser/ui/app.js",         "C:/samweb/internal/browser/ui/app.js"),
    ("/home/z/my-project/scripts/aicq_bridge.py",             "C:/samweb/scripts/aicq_bridge.py"),
]


def main():
    verbose = "-v" in sys.argv
    # Force aitun (direct SSH to shan.aitun.cc:22 is unreliable).
    os.environ.setdefault('AITUN_PATH', '/home/z/.venv/bin/aitun')
    client, proc, _ = open_ssh(verbose=verbose, use_aitun=True)
    try:
        # 1) Upload all files
        print(f"[1] uploading {len(FILES)} files ...")
        sftp = client.open_sftp()
        for local, remote in FILES:
            with open(local, "rb") as f:
                content = f.read()
            with sftp.file(remote, "wb") as f:
                f.write(content)
            print(f"   {os.path.basename(local):25s} -> {remote}  ({len(content)} bytes)")
        sftp.close()

        # 2) Kill running samweb
        print("\n[2] killing samweb.exe ...")
        rc, out, _ = run(client, 'taskkill /F /IM samweb.exe', timeout=10)
        print(out)
        time.sleep(2)

        # 3) Build
        print("\n[3] go build ...")
        build_cmd = (
            'cmd /c "cd /d C:\\samweb && '
            'set GOOS=windows&& set GOARCH=amd64&& set CGO_ENABLED=1&& '
            'go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb 2>&1"'
        )
        rc, out, err = run(client, build_cmd, timeout=180)
        print(f"   rc={rc}")
        if out.strip(): print(out[-2000:])
        if err.strip(): print("STDERR:", err[-500:])

        # 4) Check
        rc, out, _ = run(client, 'dir C:\\samweb\\samweb.exe.new', timeout=10)
        if "samweb.exe.new" not in out:
            print("   BUILD FAILED")
            return
        print("   built OK")

        # 5) Swap
        print("\n[5] swapping in new exe ...")
        rc, out, _ = run(client,
            'move /Y C:\\samweb\\samweb.exe C:\\samweb\\samweb.exe~ && '
            'move /Y C:\\samweb\\samweb.exe.new C:\\samweb\\samweb.exe',
            timeout=15)
        print(out)

        # 6) Restart
        print("\n[6] restarting samweb ...")
        rc, out, _ = run(client, 'schtasks /Run /TN RestartSamweb', timeout=10)
        print(out)

        # 7) Wait for health
        print("\n[7] waiting for /agent/health ...")
        for i in range(20):
            time.sleep(2)
            rc, out, _ = run(client,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR\' }"',
                timeout=10)
            if "ok" in out:
                print(f"   ready after ~{(i+1)*2}s: {out.strip()}")
                return
        print("   NEVER ready")
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

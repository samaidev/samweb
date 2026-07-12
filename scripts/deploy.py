#!/usr/bin/env python3
"""Deploy samweb source changes from local to shan, rebuild, restart.

Usage (from local machine):
    python3 scripts/deploy.py

Prerequisites:
    - SSH forwarder running (ssh_forward.py) OR direct SSH to shan.aitun.cc:22
    - aitun installed locally
    - Local source tree at /home/z/my-project/samweb_code/ (or edit LOCAL_ROOT)

Steps:
    1. Upload changed .go and .py files via SFTP
    2. taskkill running samweb.exe (and child tab workers + bridges)
    3. go build -tags "desktop,production" -> samweb.exe.new
    4. Swap: samweb.exe -> samweb.exe~, samweb.exe.new -> samweb.exe
    5. schtasks /Run /TN RestartSamweb  (launches via start_samweb.vbs in RDP session)
    6. Poll http://127.0.0.1:7777/agent/health until "ok"
    7. Wait 30s for tab workers + AICQ bridges to auto-spawn
"""
import os, sys, time, paramiko

HOST = "shan.aitun.cc"
PORT = "22"
USER = "Administrator"
PASS = "dongshan168"

# Local source root (where you edit files)
LOCAL_ROOT = os.environ.get("SAMWEB_LOCAL_ROOT", "/home/z/my-project/samweb_code")

# (local relative path, remote absolute path)
FILES = [
    ("cdp_client.go",           "C:/samweb/internal/cdp/client.go"),
    ("wails_backend.go",        "C:/samweb/internal/browser/wails_backend.go"),
    ("browser.go",              "C:/samweb/internal/browser/browser.go"),
    ("tab_worker.go",           "C:/samweb/internal/browser/tab_worker.go"),
    ("agent_server.go",         "C:/samweb/internal/agent/server.go"),
    ("agent_backend.go",        "C:/samweb/internal/agent/backend.go"),
    ("agent_mock_backend.go",   "C:/samweb/internal/agent/mock_backend.go"),
    ("agent_types.go",          "C:/samweb/internal/agent/types.go"),
    ("main.go",                 "C:/samweb/cmd/samweb/main.go"),
    ("aicq_bridge.py",          "C:/samweb/scripts/aicq_bridge.py"),
]


def open_transport():
    proxy = paramiko.ProxyCommand(f"aitun ssh-proxy {HOST} {PORT}")
    t = paramiko.Transport(proxy)
    t.set_keepalive(30)
    t.connect(username=USER, password=PASS)
    return t


def run(t, cmd, timeout=300):
    chan = t.open_session()
    chan.settimeout(timeout)
    chan.exec_command(cmd)
    out = b""
    err = b""
    while True:
        if chan.recv_ready(): out += chan.recv(65536)
        if chan.recv_stderr_ready(): err += chan.recv_stderr(65536)
        if chan.exit_status_ready() and not chan.recv_ready() and not chan.recv_stderr_ready():
            break
    while chan.recv_ready(): out += chan.recv(65536)
    while chan.recv_stderr_ready(): err += chan.recv_stderr(65536)
    code = chan.recv_exit_status()
    chan.close()
    return code, out.decode("utf-8","replace"), err.decode("utf-8","replace")


def main():
    t = open_transport()
    try:
        # 1) Upload
        print(f"[1] uploading {len(FILES)} files ...")
        sftp = paramiko.SFTPClient.from_transport(t)
        for rel, remote in FILES:
            local = os.path.join(LOCAL_ROOT, rel)
            if not os.path.exists(local):
                print(f"   SKIP (not found locally): {rel}")
                continue
            with open(local, "rb") as f: content = f.read()
            with sftp.file(remote, "wb") as f: f.write(content)
            print(f"   {rel:30s} -> {remote}  ({len(content)} bytes)")
        sftp.close()

        # 2) Kill
        print("\n[2] killing samweb.exe ...")
        rc, out, _ = run(t, 'taskkill /F /IM samweb.exe /T', timeout=15)
        print(out.strip())
        time.sleep(2)

        # 3) Build
        print("\n[3] go build ...")
        build_cmd = (
            'cmd /c "cd /d C:\\samweb && '
            'set GOOS=windows&& set GOARCH=amd64&& set CGO_ENABLED=1&& '
            'go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb 2>&1"'
        )
        rc, out, err = run(t, build_cmd, timeout=300)
        print(f"   rc={rc}")
        if out.strip(): print(out[-2000:])
        if err.strip(): print("STDERR:", err[-500:])

        # 4) Check
        rc, out, _ = run(t, 'dir C:\\samweb\\samweb.exe.new', timeout=10)
        if "samweb.exe.new" not in out:
            print("   BUILD FAILED")
            return 1
        print("   built OK")

        # 5) Swap
        print("\n[5] swapping in new exe ...")
        rc, out, _ = run(t,
            'move /Y C:\\samweb\\samweb.exe C:\\samweb\\samweb.exe~ && '
            'move /Y C:\\samweb\\samweb.exe.new C:\\samweb\\samweb.exe',
            timeout=15)
        print(out.strip())

        # 6) Restart via RestartSamweb schtask
        print("\n[6] restarting via RestartSamweb schtask ...")
        rc, out, _ = run(t, 'schtasks /Run /TN RestartSamweb', timeout=10)
        print(out.strip())

        # 7) Wait for health
        print("\n[7] waiting for /agent/health ...")
        for i in range(30):
            time.sleep(2)
            rc, out, _ = run(t,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR\' }"',
                timeout=10)
            if "ok" in out.lower():
                print(f"   ready after ~{(i+1)*2}s: {out.strip()}")
                print("   waiting 30s for tab workers + bridges ...")
                time.sleep(30)
                rc, out, _ = run(t, 'tasklist /FI "IMAGENAME eq samweb.exe" /FO CSV & echo === & tasklist /FI "IMAGENAME eq python.exe" /FO CSV', timeout=10)
                print(out)
                return 0
        print("   NEVER ready")
        return 1
    finally:
        t.close()


if __name__ == "__main__":
    sys.exit(main())

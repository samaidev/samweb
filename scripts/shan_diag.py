#!/usr/bin/env python3
"""Diagnose whether samweb is running on shan and what its windows look like."""
import sys
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) Is samweb running?
        rc, out, err = run(client, 'tasklist /FI "IMAGENAME eq samweb.exe" /FO CSV', timeout=15)
        print("=== tasklist samweb.exe ===")
        print(out)
        if err.strip():
            print("STDERR:", err)

        # 2) Which port is the agent API on?
        rc, out, err = run(client, 'netstat -ano | findstr ":7777"', timeout=15)
        print("\n=== netstat :7777 ===")
        print(out)

        rc, out, err = run(client, 'netstat -ano | findstr ":9222"', timeout=15)
        print("\n=== netstat :9222 (CDP) ===")
        print(out)

        # 3) schtask status
        rc, out, err = run(client, 'schtasks /Query /TN RestartSamweb /FO LIST', timeout=15)
        print("\n=== schtasks RestartSamweb ===")
        print(out)
        if err.strip():
            print("STDERR:", err)

        # 4) Last 30 lines of samweb log if exists
        rc, out, err = run(client,
            'powershell -Command "if (Test-Path C:\\samweb\\samweb.log) { Get-Content C:\\samweb\\samweb.log -Tail 40 } else { Write-Host \'no log file\' }"',
            timeout=15)
        print("\n=== last 40 lines of C:\\samweb\\samweb.log ===")
        print(out)

        # 5) Test the agent API from shan itself (loopback)
        rc, out, err = run(client,
            'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 5 -UseBasicParsing).Content } catch { Write-Host $_.Exception.Message }"',
            timeout=20)
        print("\n=== curl http://127.0.0.1:7777/agent/health from shan ===")
        print(out)
        if err.strip():
            print("STDERR:", err)
    finally:
        client.close()
        if proc:
            proc.terminate()

if __name__ == "__main__":
    main()

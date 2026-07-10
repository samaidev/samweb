#!/usr/bin/env python3
"""Check what command-line args the running samweb.exe + msedgewebview2.exe
actually got, and verify the env var is correctly propagated."""
import sys
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) samweb.exe command line
        rc, out, _ = run(client,
            'powershell -Command "Get-CimInstance Win32_Process -Filter \\"Name=\'samweb.exe\'\\" | Select-Object ProcessId, CommandLine | Format-List"',
            timeout=15)
        print("=== samweb.exe CommandLine ===")
        print(out)

        # 2) msedgewebview2.exe command line (the real Chromium)
        rc, out, _ = run(client,
            'powershell -Command "Get-CimInstance Win32_Process -Filter \\"Name=\'msedgewebview2.exe\'\\" | Select-Object ProcessId, CommandLine | Format-List"',
            timeout=15)
        print("=== msedgewebview2.exe CommandLine ===")
        # Each process has a long command line; show the first one in full
        print(out[:6000])

        # 3) Check env var of samweb
        rc, out, _ = run(client,
            'powershell -Command "Get-Process samweb -ErrorAction SilentlyContinue | ForEach-Object { $p = $_; try { [System.Diagnostics.Process]::GetProcessById($p.Id).StartInfo | Format-List } catch {} }"',
            timeout=15)
        print("=== samweb StartInfo (limited) ===")
        print(out)
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Check ~/.samweb/profiles.json on shan to see if any profile has z.ai
cookies saved.
"""
import json
import sys

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # Try common locations
        paths = [
            'C:\\Users\\Administrator\\.samweb\\profiles.json',
            'C:\\Users\\Administrator\\.samweb\\cookies.json',
            'C:\\Users\\Administrator\\AppData\\Roaming\\samweb.exe\\profiles.json',
            'C:\\samweb\\profiles.json',
        ]
        for p in paths:
            rc, out, _ = run(client, f'if exist "{p}" (type "{p}") else (echo NO)', timeout=10)
            if "NO" not in out[:10]:
                print(f"=== {p} ===")
                print(out[:2000])
                print()
            else:
                print(f"   (not found) {p}")

        # List the .samweb dir
        rc, out, _ = run(client, 'dir C:\\Users\\Administrator\\.samweb\\ 2>nul', timeout=10)
        print("=== ~/.samweb/ ===")
        print(out)

        # Also list samweb.exe data dir
        rc, out, _ = run(client, 'dir C:\\Users\\Administrator\\AppData\\Roaming\\samweb.exe\\ 2>nul', timeout=10)
        print("\n=== %AppData%\\samweb.exe\\ ===")
        print(out[:1500])
    finally:
        client.close()
        if proc: proc.terminate()


if __name__ == "__main__":
    main()

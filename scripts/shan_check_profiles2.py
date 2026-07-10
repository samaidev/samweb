#!/usr/bin/env python3
"""Read ~/.samweb/profiles.json + cdp-cookies.json on shan and look for
z.ai login cookies (session tokens).
"""
import json
import sys

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # profiles.json
        rc, out, _ = run(client, 'type C:\\Users\\Administrator\\.samweb\\profiles.json', timeout=10)
        print("=== profiles.json ===")
        try:
            prof = json.loads(out)
            if isinstance(prof, dict) and "profiles" in prof:
                profiles = prof["profiles"]
            elif isinstance(prof, list):
                profiles = prof
            else:
                profiles = []
            print(f"  {len(profiles)} profile(s)")
            for p in profiles:
                print(f"  - id={p.get('id')} name={p.get('name')}")
                cookies = p.get("cookies", [])
                zai = [c for c in cookies if "z.ai" in c.get("domain", "") or "chatglm" in c.get("domain", "")]
                print(f"    total cookies: {len(cookies)}, z.ai cookies: {len(zai)}")
                for c in zai:
                    v = c.get("value", "")
                    v_show = v[:50] + "..." if len(v) > 50 else v
                    print(f"      {c.get('domain')}: {c.get('name')}={v_show}")
        except Exception as e:
            print(f"  parse error: {e}")
            print(out[:1000])

        # cdp-cookies.json
        print("\n=== cdp-cookies.json ===")
        rc, out, _ = run(client, 'type C:\\Users\\Administrator\\.samweb\\cdp-cookies.json', timeout=10)
        try:
            cookies = json.loads(out)
            if isinstance(cookies, list):
                print(f"  {len(cookies)} cookie(s)")
                zai = [c for c in cookies if "z.ai" in c.get("domain", "") or "chatglm" in c.get("domain", "")]
                print(f"  z.ai cookies: {len(zai)}")
                for c in zai:
                    v = c.get("value", "")
                    v_show = v[:50] + "..." if len(v) > 50 else v
                    print(f"    {c.get('domain')}: {c.get('name')}={v_show}")
        except Exception as e:
            print(f"  parse error: {e}")
            print(out[:500])
    finally:
        client.close()
        if proc: proc.terminate()


if __name__ == "__main__":
    main()

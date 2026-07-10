#!/usr/bin/env python3
import json, sys
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run
client, proc, _ = open_ssh(verbose=False)
try:
    rc, out, _ = run(client, 'type C:\\Users\\Administrator\\.samweb\\profiles.json', timeout=10)
    try:
        data = json.loads(out)
        profiles = data.get("profiles", []) if isinstance(data, dict) else data
        print(f"total profiles: {len(profiles)}")
        for p in profiles:
            ls = p.get("local_storage", {})
            ls_count = sum(len(v) for v in ls.values())
            print(f"  - id={p.get('id')} name={p.get('name')} cookies={len(p.get('cookies',[]))} ls_origins={list(ls.keys())} ls_entries={ls_count}")
    except Exception as e:
        print(f"parse error: {e}")
        print(out[:500])
finally:
    client.close()
    if proc: proc.terminate()

#!/usr/bin/env python3
"""Login each agent, decode the JWT to get the real account_id, update
the db + profiles.json.
"""
import asyncio
import base64
import json
import os
import sys

from aicq import AICQCore

SERVER = "https://aicq.me"
OWNER_ID = "1000008"

AGENTS = [
    ("qq",             "data.db"),
    ("139",            "data_139.db"),
    ("carterdong168",  "data_carterdong168.db"),
]


def decode_jwt_account_id(jwt_token):
    """Decode JWT payload to extract the 'sub' (real account_id)."""
    parts = jwt_token.split(".")
    if len(parts) < 2:
        return None
    payload = parts[1] + "=" * (4 - len(parts[1]) % 4)
    try:
        data = json.loads(base64.urlsafe_b64decode(payload))
        return data.get("sub", "")
    except Exception:
        return None


async def fix_agent(profile_id, db_filename, owner_id):
    db_path = os.path.join(os.path.expanduser("~"), ".aicq-sdk", db_filename)
    print(f"\n--- Profile: {profile_id} | DB: {db_filename} ---")

    core = AICQCore(db_path=db_path, server=SERVER)
    agent = core.db.get_agent()
    print(f"  before: account_id={agent.get('account_id')}")

    try:
        jwt_token = await core.login()
        real_id = decode_jwt_account_id(jwt_token)
        print(f"  JWT decoded real account_id: {real_id}")
        if real_id:
            # Update the db via direct SQL (no update_agent method exists)
            import sqlite3
            conn = sqlite3.connect(db_path)
            conn.execute("UPDATE agents SET account_id = ?, is_current = 1 WHERE signing_pub = ?",
                         (real_id, agent.get("signing_pub")))
            conn.commit()
            conn.close()
            print(f"  ✅ db updated: account_id → {real_id}")
        else:
            real_id = agent.get("account_id", "")
    except Exception as e:
        print(f"  login error: {e}")
        real_id = agent.get("account_id", "")

    # Save to profiles.json
    profiles_path = os.path.join(os.path.expanduser("~"), ".samweb", "profiles.json")
    with open(profiles_path, "r", encoding="utf-8") as f:
        data = json.load(f)
    for p in data.get("profiles", []):
        if p.get("id") == profile_id:
            p["aicq_identity"] = {
                "account_id":   real_id,
                "signing_pub":  agent.get("signing_pub", ""),
                "signing_sec":  agent.get("signing_sec", ""),
                "exchange_pub": agent.get("exchange_pub", ""),
                "exchange_sec": agent.get("exchange_sec", ""),
                "owner_id":     owner_id,
                "db_path":      db_path,
            }
            print(f"  ✅ profile {profile_id} → agent {real_id}")
            break
    with open(profiles_path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False)

    return real_id


async def main():
    print(f"=== Fix account_ids for {len(AGENTS)} agents ===")
    for profile_id, db_filename in AGENTS:
        try:
            await fix_agent(profile_id, db_filename, OWNER_ID)
        except Exception as e:
            print(f"  ❌ {profile_id}: {e}")

    print("\n=== Final ===")
    profiles_path = os.path.join(os.path.expanduser("~"), ".samweb", "profiles.json")
    with open(profiles_path, "r", encoding="utf-8") as f:
        data = json.load(f)
    for p in data.get("profiles", []):
        aicq = p.get("aicq_identity")
        if aicq:
            print(f"  profile {p['name']}: agent={aicq.get('account_id')}, owner={aicq.get('owner_id')}, db={os.path.basename(aicq.get('db_path',''))}")
        else:
            print(f"  profile {p['name']}: NO AICQ")


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

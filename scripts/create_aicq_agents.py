#!/usr/bin/env python3
"""Create 2 new AICQ agents, each in its own db file, all bound to owner 1000008.

Strategy: use AICQCore directly with a custom db_path per agent.
  - ~/.aicq-sdk/data.db          (existing, agent ai_79ab6146 → profile qq)
  - ~/.aicq-sdk/data_139.db      (new, for profile 139)
  - ~/.aicq-sdk/data_carterdong168.db  (new, for profile carterdong168)

Each db stores the full agent identity (keys + binding). The AICQ bridge
later loads each db independently to run multiple agents simultaneously.

After creation, each agent's identity is saved into the corresponding
profile in ~/.samweb/profiles.json.
"""
import asyncio
import json
import os
import sys

from aicq import AICQCore, AICQError

SERVER = "https://aicq.me"
OWNER_ID = "1000008"  # gasschina@gmail.com

# (profile_id, agent_name, db_filename)
TARGETS = [
    ("139",            "SamWeb-139",            "data_139.db"),
    ("carterdong168",  "SamWeb-carterdong168",  "data_carterdong168.db"),
]


async def create_agent(profile_id, agent_name, db_filename, owner_id):
    """Create a new AICQ agent in a separate db file and bind to owner."""
    db_path = os.path.join(os.path.expanduser("~"), ".aicq-sdk", db_filename)
    print(f"\n--- Profile: {profile_id} | Agent: {agent_name} | DB: {db_filename} ---")

    if os.path.exists(db_path) and os.path.getsize(db_path) > 8192:
        print(f"  db already exists with data, skipping creation")
    else:
        print(f"  creating new agent...")
        core = AICQCore(db_path=db_path, server=SERVER)
        try:
            # create_my_agent generates keys + registers + auto-logins.
            # No need to connect() first — create_my_agent handles it.
            agent = await core.create_my_agent(agent_name)
            print(f"  agent created: {json.dumps({k: v for k, v in agent.items() if k in ('account_id','name','signing_pub')}, indent=2)}")

            # Now connect + send friend request to owner
            print(f"  connecting to send friend request to owner {owner_id}...")
            try:
                await core.connect()
                await core.add_friend(owner_id, message=f"Hi! I'm {agent_name}, please accept so we can chat.")
                print(f"  ✅ friend request sent to owner {owner_id}")
                await core.disconnect()
            except Exception as e:
                print(f"  warning: add_friend failed (may already be friend, or connect error): {e}")

            agent_id = agent.get("account_id", "")
            print(f"  ✅ agent {agent_id} created and bound")
        except Exception as e:
            print(f"  ❌ error: {e}")
            import traceback
            traceback.print_exc()
            return None

    # Read the agent identity from the db
    core = AICQCore(db_path=db_path, server=SERVER)
    agent = core.db.get_agent()
    if not agent:
        print(f"  ❌ no agent found in db after creation")
        return None

    agent_id = agent.get("account_id", "")
    signing_pub = agent.get("signing_pub", "")
    signing_sec = agent.get("signing_sec", "")
    exchange_pub = agent.get("exchange_pub", "")
    exchange_sec = agent.get("exchange_sec", "")
    print(f"  agent identity: id={agent_id}, name={agent.get('name')}")

    # Save to profiles.json
    profiles_path = os.path.join(os.path.expanduser("~"), ".samweb", "profiles.json")
    with open(profiles_path, "r", encoding="utf-8") as f:
        data = json.load(f)

    for p in data.get("profiles", []):
        if p.get("id") == profile_id:
            p["aicq_identity"] = {
                "account_id":   agent_id,
                "signing_pub":  signing_pub,
                "signing_sec":  signing_sec,
                "exchange_pub": exchange_pub,
                "exchange_sec": exchange_sec,
                "owner_id":     owner_id,
                "db_path":      db_path,
            }
            print(f"  ✅ saved AICQ identity to profile {profile_id}")
            break
    else:
        print(f"  ❌ profile {profile_id} not found in profiles.json")
        return None

    with open(profiles_path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False)

    return agent_id


async def main():
    print(f"=== Creating {len(TARGETS)} AICQ agents (owner: {OWNER_ID}) ===")
    results = {}
    for profile_id, agent_name, db_filename in TARGETS:
        agent_id = await create_agent(profile_id, agent_name, db_filename, OWNER_ID)
        results[profile_id] = agent_id

    print("\n=== Summary ===")
    for pid, aid in results.items():
        status = f"✅ agent {aid}" if aid else "❌ failed"
        print(f"  profile {pid}: {status}")

    print("\n=== ~/.aicq-sdk/ files ===")
    sdk_dir = os.path.join(os.path.expanduser("~"), ".aicq-sdk")
    for f in os.listdir(sdk_dir):
        fpath = os.path.join(sdk_dir, f)
        print(f"  {f} ({os.path.getsize(fpath)} bytes)")

    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

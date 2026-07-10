#!/usr/bin/env python3
"""Initialize AICQ QuickChat agent and bind to owner.

Uses the owner's email/password to find their AICQ ID, then binds
the samweb agent to that owner.

Usage:
  python3.13 setup_aicq.py
"""
import asyncio
import json
import os
import sys

from aicq import AICQChatClient, AICQCore, AICQError

OWNER_EMAIL = "gasschina@gmail.com"
OWNER_PASSWORD = "Dongshan@168"
AGENT_NAME = "SamWeb Browser"
SERVER = "https://aicq.me"


async def main():
    print("=== Step 1: Login as owner to find AICQ ID ===")
    core = AICQCore(server=SERVER)
    
    # Try to login with email/password
    try:
        result = await core.login(OWNER_EMAIL, OWNER_PASSWORD)
        print(f"Login result: {json.dumps(result, indent=2, default=str)[:500]}")
        owner_id = result.get("account_id", result.get("id", ""))
        print(f"\nOwner AICQ ID: {owner_id}")
    except Exception as e:
        print(f"Login failed: {e}")
        # Try alternative login method
        try:
            result = await core.human_login(OWNER_EMAIL, OWNER_PASSWORD)
            print(f"Human login result: {json.dumps(result, indent=2, default=str)[:500]}")
            owner_id = result.get("account_id", result.get("id", ""))
            print(f"\nOwner AICQ ID: {owner_id}")
        except Exception as e2:
            print(f"Human login also failed: {e2}")
            # Try direct API call
            import aiohttp
            async with aiohttp.ClientSession() as session:
                async with session.post(
                    f"{SERVER}/api/v1/auth/login",
                    json={"email": OWNER_EMAIL, "password": OWNER_PASSWORD},
                    timeout=aiohttp.ClientTimeout(total=30),
                ) as resp:
                    data = await resp.json()
                    print(f"Direct login: status={resp.status}, data={json.dumps(data, indent=2, default=str)[:500]}")
                    if resp.status == 200:
                        owner_id = data.get("account_id", data.get("id", ""))
                        print(f"\nOwner AICQ ID: {owner_id}")
                    else:
                        print("Could not determine owner AICQ ID")
                        return 1

    if not owner_id:
        print("ERROR: Could not find owner AICQ ID")
        return 1

    print(f"\n=== Step 2: Init QuickChat agent ===")
    client = AICQChatClient(server=SERVER)
    try:
        init_result = await client.init(name=AGENT_NAME)
        print(f"Init result: {json.dumps(init_result, indent=2, default=str)[:500]}")
    except Exception as e:
        print(f"Init failed (may already exist): {e}")

    print(f"\n=== Step 3: Bind to owner {owner_id} ===")
    try:
        bind_result = await client.bind(owner_id, agent_name=AGENT_NAME)
        print(f"Bind result: {json.dumps(bind_result, indent=2, default=str)[:500]}")
        print("\n✅ Successfully bound!")
    except Exception as e:
        print(f"Bind failed: {e}")
        return 1

    print(f"\n=== Step 4: Test chat ===")
    try:
        chat_result = await client.chat(content="Hello from SamWeb!", speak=True, wait_seconds=10)
        print(f"Chat result: {json.dumps(chat_result, indent=2, default=str)[:500]}")
    except Exception as e:
        print(f"Chat test failed: {e}")

    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

#!/usr/bin/env python3
"""Login as owner (human account) via email/password, then add the 2 new
AI agents as friends. The owner accepts the friend requests from the agents
by sending its own add_friend request back.

This is needed because AICQ's friend system is mutual: agent→owner request
alone doesn't notify the owner; the owner must also add the agent.
"""
import asyncio
import json
import os
import sys

import aiohttp

SERVER = "https://aicq.me"
OWNER_EMAIL = "gasschina@gmail.com"
OWNER_PASSWORD = "Dongshan@168"

# New agents to add as friends (from profiles.json)
NEW_AGENTS = [
    ("139",            "ai_7e9a7d6f"),
    ("carterdong168",  "ai_8653a541"),
]


async def owner_login(session):
    """Login as owner, return access_token."""
    print("=== Login as owner ===")
    async with session.post(
        f"{SERVER}/api/v1/auth/login",
        json={"email": OWNER_EMAIL, "password": OWNER_PASSWORD},
        timeout=aiohttp.ClientTimeout(total=30),
    ) as resp:
        data = await resp.json()
        print(f"  status={resp.status}")
        if resp.status != 200:
            print(f"  error: {json.dumps(data, indent=2)[:300]}")
            return None
        token = data.get("access_token") or data.get("token")
        account_id = data.get("account_id") or data.get("id")
        print(f"  owner account_id={account_id}")
        return token


async def add_friend(session, token, agent_account_id, message):
    """Owner adds an AI agent as friend. Try multiple endpoints."""
    print(f"\n=== add_friend {agent_account_id} ===")
    endpoints = [
        ("/api/v1/friends/requests", {"to_id": agent_account_id, "message": message}),
        ("/api/v1/friends", {"to_id": agent_account_id, "message": message}),
        ("/api/v1/friend-requests", {"to_id": agent_account_id, "message": message}),
        ("/api/v1/friends/request", {"to_id": agent_account_id, "message": message}),
    ]
    for path, payload in endpoints:
        try:
            async with session.post(
                f"{SERVER}{path}",
                json=payload,
                headers={"Authorization": f"Bearer {token}"},
                timeout=aiohttp.ClientTimeout(total=15),
            ) as resp:
                text = await resp.text()
                print(f"  POST {path} → {resp.status}: {text[:200]}")
                if resp.status in (200, 201):
                    return True
        except Exception as e:
            print(f"  POST {path} → error: {e}")
    return False


async def list_friends(session, token):
    """List owner's friends."""
    print("\n=== list friends ===")
    async with session.get(
        f"{SERVER}/api/v1/friends",
        headers={"Authorization": f"Bearer {token}"},
        timeout=aiohttp.ClientTimeout(total=30),
    ) as resp:
        data = await resp.json()
        if resp.status == 200:
            friends = data if isinstance(data, list) else data.get("friends", [])
            print(f"  {len(friends)} friend(s):")
            for f in friends:
                print(f"    - {f.get('id')} ({f.get('display_name') or f.get('name')})")
            return friends
        else:
            print(f"  error: {resp.status} {data}")
            return []


async def main():
    async with aiohttp.ClientSession() as session:
        token = await owner_login(session)
        if not token:
            print("ERROR: login failed")
            return 1

        await list_friends(session, token)

        for profile_id, agent_id in NEW_AGENTS:
            await add_friend(session, token, agent_id,
                             f"Hi! Adding you as friend for profile {profile_id}.")

        print("\n=== final friends list ===")
        await list_friends(session, token)

    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

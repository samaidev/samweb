#!/usr/bin/env python3
"""Bind samweb agent to AICQ owner 1000008."""
import asyncio
import json
import sys
from aicq import AICQChatClient

OWNER_ID = "1000008"
AGENT_NAME = "SamWeb Browser"
SERVER = "https://aicq.me"


async def main():
    print("=== Init QuickChat agent ===")
    client = AICQChatClient(server=SERVER)
    try:
        result = await client.init(name=AGENT_NAME)
        print(f"Init: {json.dumps(result, indent=2, default=str)[:500]}")
    except Exception as e:
        print(f"Init (may already exist): {e}")

    print(f"\n=== Bind to owner {OWNER_ID} ===")
    try:
        result = await client.bind(OWNER_ID, agent_name=AGENT_NAME)
        print(f"Bind: {json.dumps(result, indent=2, default=str)[:500]}")
        print("\n✅ Bound!")
    except Exception as e:
        print(f"Bind failed: {e}")
        return 1

    print(f"\n=== Test send ===")
    try:
        result = await client.send("Hello from SamWeb Browser! 🎉")
        print(f"Send: {json.dumps(result, indent=2, default=str)[:300]}")
    except Exception as e:
        print(f"Send failed: {e}")

    # Print the QuickChat config for samweb to use
    print(f"\n=== QuickChat config saved to ~/.aicq-sdk/quickchat.json ===")
    import os
    qc_path = os.path.expanduser("~/.aicq-sdk/quickchat.json")
    if os.path.exists(qc_path):
        with open(qc_path) as f:
            print(f.read()[:500])

    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))

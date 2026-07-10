#!/usr/bin/env python3
"""Test AICQ QuickChat — send messages and check context retention."""
import asyncio
import json
import sys
from aicq import AICQChatClient

async def main():
    client = AICQChatClient(server='https://aicq.me')
    await client.init(name='SamWeb Browser')
    
    status = await client.status()
    print(f"Status: bound={status.get('bound')}, owner={status.get('owner_display_name')}", flush=True)
    
    # Message 1
    print("\n=== Message 1: 今天星期几？ ===", flush=True)
    try:
        result = await client.chat(content='今天星期几？', speak=True, wait_seconds=90)
        # Extract reply
        if isinstance(result, dict):
            msgs = result.get('messages', [])
            for m in msgs:
                from_id = m.get('from_id', m.get('from', ''))
                content = m.get('content', '')
                if from_id and not from_id.startswith('ai_'):
                    print(f"  Owner: {content[:200]}", flush=True)
                elif content:
                    print(f"  Agent reply: {content[:200]}", flush=True)
            if not msgs:
                print(f"  Raw: {json.dumps(result, default=str)[:500]}", flush=True)
        else:
            print(f"  Result: {str(result)[:500]}", flush=True)
    except Exception as e:
        print(f"  Error: {e}", flush=True)
    
    # Message 2 — should have context
    print("\n=== Message 2: 那明天呢？ ===", flush=True)
    try:
        result = await client.chat(content='那明天呢？', speak=True, wait_seconds=90)
        if isinstance(result, dict):
            msgs = result.get('messages', [])
            for m in msgs:
                from_id = m.get('from_id', m.get('from', ''))
                content = m.get('content', '')
                if from_id and not from_id.startswith('ai_'):
                    print(f"  Owner: {content[:200]}", flush=True)
                elif content:
                    print(f"  Agent reply: {content[:200]}", flush=True)
            if not msgs:
                print(f"  Raw: {json.dumps(result, default=str)[:500]}", flush=True)
        else:
            print(f"  Result: {str(result)[:500]}", flush=True)
    except Exception as e:
        print(f"  Error: {e}", flush=True)
    
    await client.close()
    print("\nDone!", flush=True)

if __name__ == '__main__':
    asyncio.run(main())

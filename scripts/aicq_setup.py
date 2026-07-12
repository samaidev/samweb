#!/usr/bin/env python3
import asyncio
from aicq import AICQChatClient

async def main():
    c = AICQChatClient(server="https://aicq.me")
    await c.init(name="SamWeb Browser")
    s = await c.status()
    print(f"bound={s.get('bound')}, owner={s.get('owner_display_name')}")
    
    # Also accept any pending friend requests
    core = c._core
    if core:
        reqs = await core.list_friend_requests()
        if isinstance(reqs, dict):
            received = reqs.get('received', [])
            for r in received:
                if r.get('status') == 'pending':
                    req_id = r.get('id', '')
                    print(f"Accepting friend request {req_id}...")
                    try:
                        await core.accept_friend_request(req_id)
                        print("Accepted!")
                    except Exception as e:
                        print(f"Accept failed: {e}")
    
    await c.close()

asyncio.run(main())

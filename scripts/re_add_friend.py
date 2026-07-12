
import os, asyncio, json
from aicq import AICQCore

OWNER_ID = "1000008"

async def try_add(profile, db_file):
    print(f"\n=== {profile} ({db_file}) ===")
    db = os.path.join(os.path.expanduser("~"), ".aicq-sdk", db_file)
    core = AICQCore(db_path=db, server="https://aicq.me")
    try:
        await core.login()
        print(f"  login OK, agent={core.db.get_agent().get('account_id')}")
    except Exception as e:
        print(f"  login error: {e}")
        return
    
    # Check existing friends first
    try:
        friends = await core.list_friends()
        print(f"  existing friends: {len(friends) if isinstance(friends, list) else friends}")
        if isinstance(friends, list):
            for f in friends:
                print(f"    - {f.get('id')} ({f.get('display_name')})")
    except Exception as e:
        print(f"  list_friends error: {e}")
    
    # Try add_friend
    print(f"  sending friend request to {OWNER_ID}...")
    try:
        result = await core.add_friend(OWNER_ID, message=f"Hi! I am the agent for profile {profile}. Please accept.")
        print(f"  add_friend result: {json.dumps(result, indent=2, default=str)[:500]}")
    except Exception as e:
        print(f"  add_friend error: {e}")
        import traceback
        traceback.print_exc()

async def main():
    for profile, db in [("139", "data_139.db"), ("carterdong168", "data_carterdong168.db")]:
        await try_add(profile, db)

asyncio.run(main())

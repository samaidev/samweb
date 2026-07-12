
import os, json
from aicq import AICQCore
for db in ["data.db", "data_139.db", "data_carterdong168.db"]:
    path = os.path.join(os.path.expanduser("~"), ".aicq-sdk", db)
    core = AICQCore(db_path=path)
    agent = core.db.get_agent()
    if agent:
        print(f"{db}: account_id={agent.get('account_id')}, name={agent.get('name')}, signing_pub={agent.get('signing_pub','')[:20]}...")
    else:
        print(f"{db}: no agent")

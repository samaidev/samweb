#!/usr/bin/env python3
"""Send a reminder + wait for owner reply."""
import asyncio, json, sys
from aicq import AICQChatClient

async def main():
    client = AICQChatClient(server='https://aicq.me')
    await client.init(name='SamWeb Browser')
    
    # Send reminder
    print('=== Sending reminder ===', flush=True)
    result = await client.chat(content='请回复这条消息来测试聊天功能！', speak=True, wait_seconds=0)
    print('Sent:', result.get('your_message', {}).get('status', '?'), flush=True)
    
    # Poll for response
    print('\n=== Waiting for your reply (180s)... ===', flush=True)
    print('请在 aicq.me 给 SamWeb Browser 发一条消息！', flush=True)
    result = await client.chat(speak=False, wait_seconds=180)
    msgs = result.get('messages') or []
    if msgs:
        for m in msgs:
            from_id = m.get('from_id', '?')
            content = m.get('content', '')
            print(f'\n✅ Received from {from_id}: {content}', flush=True)
            
            # Now forward to z.ai via samweb agent API
            print(f'\n=== Forwarding to z.ai ===', flush=True)
            # For now just echo back
            reply = f"收到你的消息: {content}\n\n（z.ai 转发功能待实现）"
            print(f'Replying: {reply[:100]}', flush=True)
            await client.chat(content=reply, speak=True, wait_seconds=0)
            print('Reply sent!', flush=True)
    else:
        print('\n❌ No messages received in 180s', flush=True)
    
    await client.close()
    print('\nDone!', flush=True)

if __name__ == '__main__':
    asyncio.run(main())

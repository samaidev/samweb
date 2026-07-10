#!/usr/bin/env python3
"""Fix go.mod replace + build + check CDP."""
import os, sys, time
os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run
from shan_lib.agent import Agent
import json

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # Fix go.mod using SFTP
        sftp = client.open_sftp()
        with sftp.open('/C:/samweb/go.mod', 'r') as f:
            gomod = f.read().decode('utf-8')
        
        # Remove existing replace lines
        lines = [l for l in gomod.split('\n') if 'replace' not in l.lower()]
        # Add correct replace
        lines.append('')
        lines.append('replace github.com/wailsapp/go-webview2 v1.0.22 => ./go-webview2-patch')
        
        with sftp.open('/C:/samweb/go.mod', 'w') as f:
            f.write('\n'.join(lines).encode('utf-8'))
        print('go.mod fixed')
        
        # Verify
        with sftp.open('/C:/samweb/go.mod', 'r') as f:
            content = f.read().decode('utf-8')
        for line in content.split('\n'):
            if 'replace' in line:
                print(f'  {line.strip()}')
        sftp.close()
        
        # Build
        print('\n=== go build ===')
        rc, out, err = run(client, 'cd /d C:\\samweb && go build -tags desktop,production -ldflags "-w -s -H windowsgui" -o samweb.exe ./cmd/samweb 2>&1', timeout=300)
        print(f'build rc={rc}')
        if out: print(out[:500])
        if rc != 0:
            return 1
        
        # Restart
        run(client, 'taskkill /F /IM samweb.exe 2>nul')
        run(client, 'taskkill /F /IM msedgewebview2.exe 2>nul')
        time.sleep(5)
        run(client, 'schtasks /Run /TN RestartSamweb')
        time.sleep(20)
        
        # Check CDP
        rc, out, err = run(client, 'netstat -an | findstr :9222 | findstr LISTENING')
        cdp = 'LISTENING' in out
        print(f'CDP 9222: {"LISTENING ✅" if cdp else "not listening ❌"}')
        
        if cdp:
            # Now test with Playwright via CDP
            print('\n=== Testing with CDP ===')
            # Get the CDP JSON endpoint via SSH
            rc, out, err = run(client, 'powershell -Command "Invoke-WebRequest -Uri http://127.0.0.1:9222/json -UseBasicParsing | Select-Object -ExpandProperty Content" 2>&1', timeout=15)
            print('CDP targets:')
            print(out[:1000])
            
            # Use agent API to check page state
            a = Agent(verbose=False)
            try:
                h = a.get('/agent/health', timeout=10)
                print(f'\nagent health: {h.get("status")}')
            except Exception as e:
                print(f'agent health: {e}')
            finally:
                a.close()
        
        return 0 if cdp else 1
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

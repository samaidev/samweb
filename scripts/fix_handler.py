#!/usr/bin/env python3
"""Fix browser.go on shan: add back Handler for dynamic routes."""
import os, sys, time
os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        sftp = client.open_sftp()
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'r') as f:
            content = f.read().decode('utf-8')
        
        # Replace the AssetServer block to add Handler back
        old = '''                AssetServer:      &assetserver.Options{
                        Assets: uiAssets,
                },'''
        
        new = '''                AssetServer:      &assetserver.Options{
                        Assets: uiAssets,
                        Handler: &dynamicHandler{
                                uiPort:    uiPort,
                                engine:    engine,
                                agentAddr: opts.AgentAddr,
                        },
                },'''
        
        if old in content:
            content = content.replace(old, new)
            print('added Handler back')
        else:
            print('old block not found, checking content...')
            idx = content.find('AssetServer')
            print(content[idx:idx+200])
        
        # Also check if dynamicHandler type exists
        if 'type dynamicHandler struct' not in content:
            print('WARNING: dynamicHandler type not found in browser.go')
        
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'w') as f:
            f.write(content.encode('utf-8'))
        print('saved browser.go')
        sftp.close()
        
        # Build
        print('\n=== go build ===')
        rc, out, err = run(client, 'cd /d C:\\samweb && go build -tags desktop,production -ldflags "-w -s -H windowsgui" -o samweb.exe ./cmd/samweb 2>&1', timeout=300)
        print(f'build rc={rc}')
        if out: print(out[:500])
        if rc != 0: return 1
        
        # Restart
        run(client, 'taskkill /F /IM samweb.exe 2>nul')
        run(client, 'taskkill /F /IM msedgewebview2.exe 2>nul')
        time.sleep(5)
        run(client, 'schtasks /Run /TN RestartSamweb')
        time.sleep(20)
        
        rc, out, err = run(client, 'netstat -an | findstr :9222 | findstr LISTENING')
        print('CDP 9222:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

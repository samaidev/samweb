#!/usr/bin/env python3
"""Upload proxy.go + build + restart + test z.ai via Playwright."""
import os, sys, time
os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # Upload proxy.go
        sftp = client.open_sftp()
        with open('/home/z/my-project/samweb/internal/proxy/proxy.go', 'rb') as f:
            data = f.read()
        with sftp.open('/C:/samweb/internal/proxy/proxy.go', 'wb') as f:
            f.write(data)
        print(f'uploaded proxy.go ({len(data)} bytes)')
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

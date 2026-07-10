#!/usr/bin/env python3
"""Fix struct definition on shan."""
import os, sys, time
os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        sftp = client.open_sftp()
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'r') as f:
            lines = f.read().decode('utf-8').split('\n')
        
        # Find and fix the uiAssetHandler struct
        new_lines = []
        i = 0
        while i < len(lines):
            line = lines[i]
            # Check if this is the uiAssetHandler struct
            if 'type uiAssetHandler struct {' in line:
                new_lines.append(line)
                i += 1
                # Replace the struct body
                new_lines.append('\tuiPort           int')
                new_lines.append('\tengine           search.Engine')
                new_lines.append('\tagentAddr        string')
                new_lines.append('\tcallbackHandler  http.HandlerFunc')
                # Skip old fields until }
                while i < len(lines) and lines[i].strip() != '}':
                    i += 1
                if i < len(lines):
                    new_lines.append(lines[i])  # the } line
                i += 1
                print('fixed struct')
            else:
                new_lines.append(line)
                i += 1
        
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'w') as f:
            f.write('\n'.join(new_lines).encode('utf-8'))
        print('saved')
        sftp.close()
        
        # Build
        print('=== go build ===')
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
        print('CDP:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        rc, out, err = run(client, 'netstat -an | findstr :7777 | findstr LISTENING')
        print('7777:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

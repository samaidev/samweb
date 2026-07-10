#!/usr/bin/env python3
"""Fix browser.go: add OnDomReady to inject agent JS on every page load.
This enables navigate-direct (window.location.href = url) to work —
when the browser navigates to z.ai, the agent JS is re-injected so
the agent API can control the page."""
import os, sys, time
os.environ['AITUN_PATH'] = '/home/z/.venv/bin/aitun'
sys.path.insert(0, '/home/z/my-project/samweb/scripts')
from shan_lib.ssh import open_ssh, run
import shutil

def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        sftp = client.open_sftp()
        
        # Read current browser.go
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'r') as f:
            content = f.read().decode('utf-8')
        
        # Find the wails.Run block and add OnDomReady + OnStartup
        # Current block has no OnDomReady — add it to inject agent JS
        old_onstartup = '''                OnStartup: func(ctx context.Context) {
                        backend.SetContext(ctx)
                        log.Printf("[browser] wails app started")
                },'''
        
        new_onstartup = '''                OnStartup: func(ctx context.Context) {
                        backend.SetContext(ctx)
                        log.Printf("[browser] wails app started")
                },
                OnDomReady: func(ctx context.Context) {
                        // Inject agent bootstrap JS on every page load.
                        // This is critical for navigate-direct: when the browser
                        // navigates to z.ai (or any external site), the agent JS
                        // is re-injected so the agent API can control the page.
                        agentJS := agentBootstrapJS(uiPort)
                        wailsRuntime.WindowExecJS(ctx, agentJS)
                        log.Printf("[browser] agent JS injected on DOM ready: %s", wailsRuntime.WindowGetURL(ctx))
                },'''
        
        # Also need to add the wailsRuntime import back
        if 'wailsRuntime' not in content:
            # Add import
            old_import = '"github.com/wailsapp/wails/v2/pkg/options/assetserver"'
            new_import = '''"github.com/wailsapp/wails/v2/pkg/options/assetserver"
        wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"'''
            content = content.replace(old_import, new_import)
            print('added wailsRuntime import')
        
        if old_onstartup in content:
            content = content.replace(old_onstartup, new_onstartup)
            print('added OnDomReady')
        else:
            print('OnStartup block not found, checking...')
            idx = content.find('OnStartup')
            print(content[idx:idx+200])
        
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
        rc, out, err = run(client, 'netstat -an | findstr :7777 | findstr LISTENING')
        print('port 7777:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

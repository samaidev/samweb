#!/usr/bin/env python3
"""Fix handler type + build + restart."""
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
        
        # Fix: change dynamicHandler to uiAssetHandler
        content = content.replace('&dynamicHandler{', '&uiAssetHandler{')
        
        # Fix default case to return 404 (let wails handle static files)
        old_default = """        default:
                // Let wails serve the embedded static file.
                http.FileServer(http.FS(mustSubFS())).ServeHTTP(w, r)
        }"""
        new_default = """        default:
                // For static files, return 404 so wails falls back to embedded assets.
                http.NotFound(w, r)
        }"""
        
        if old_default in content:
            content = content.replace(old_default, new_default)
            print('fixed default case')
        else:
            # Try without the comment
            old_default2 = """        default:
                http.FileServer(http.FS(mustSubFS())).ServeHTTP(w, r)
        }"""
            if old_default2 in content:
                content = content.replace(old_default2, new_default)
                print('fixed default case (v2)')
            else:
                print('default case not found - checking...')
                idx = content.find('default:')
                if idx >= 0:
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
        if rc != 0:
            return 1
        
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

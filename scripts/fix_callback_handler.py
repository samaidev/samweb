#!/usr/bin/env python3
"""Fix: add /agent/callback to wails Handler + add navigate-direct support.
Also fix the agentBootstrapJS to use the UI server's absolute URL for callbacks."""
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
        
        # 1. Add /agent/callback to uiAssetHandler (wails Handler)
        # Find the uiAssetHandler.ServeHTTP function and add /agent/callback case
        old_proxy_case = '''        case r.URL.Path == "/proxy":'''
        new_proxy_case = '''        case r.URL.Path == "/agent/callback":
                // JS → Go callback — delegate to the callback handler
                if h.callbackHandler != nil {
                        h.callbackHandler(w, r)
                } else {
                        http.Error(w, "callback handler not set", http.StatusInternalServerError)
                }
        case r.URL.Path == "/proxy":'''
        
        if old_proxy_case in content:
            content = content.replace(old_proxy_case, new_proxy_case)
            print('added /agent/callback to uiAssetHandler')
        else:
            print('proxy case not found')
        
        # 2. Add callbackHandler field to uiAssetHandler
        old_struct = '''type uiAssetHandler struct {
                uiPort    int
                engine    search.Engine
                agentAddr string
}'''
        new_struct = '''type uiAssetHandler struct {
                uiPort           int
                engine           search.Engine
                agentAddr        string
                callbackHandler  http.HandlerFunc
}'''
        if old_struct in content:
            content = content.replace(old_struct, new_struct)
            print('added callbackHandler field')
        else:
            print('struct not found')
        
        # 3. Set callbackHandler when creating the handler
        old_handler_create = '''Handler: &uiAssetHandler{
                                uiPort:    uiPort,
                                engine:    engine,
                                agentAddr: opts.AgentAddr,
                        },'''
        new_handler_create = '''Handler: &uiAssetHandler{
                                uiPort:          uiPort,
                                engine:          engine,
                                agentAddr:       opts.AgentAddr,
                                callbackHandler: HandleCallbackHTTP(backend),
                        },'''
        if old_handler_create in content:
            content = content.replace(old_handler_create, new_handler_create)
            print('set callbackHandler')
        else:
            print('handler create not found')
        
        with sftp.open('/C:/samweb/internal/browser/browser.go', 'w') as f:
            f.write(content.encode('utf-8'))
        print('saved')
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
        print('CDP:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        rc, out, err = run(client, 'netstat -an | findstr :7777 | findstr LISTENING')
        print('7777:', 'LISTENING' if 'LISTENING' in out else 'not listening')
        return 0
    finally:
        client.close()
        if proc: proc.terminate()

if __name__ == '__main__':
    sys.exit(main())

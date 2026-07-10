#!/usr/bin/env python3
"""Try schtasks again but capture samweb's output by modifying the bat
to redirect to a log file. Also check the Windows event log for any
application crashes.
"""
import sys
import time

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


NEW_BAT = (
    "@echo off\r\n"
    "set WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222 --remote-allow-origins=*\r\n"
    "C:\\samweb\\samweb.exe > C:\\samweb\\run.log 2>&1\r\n"
)


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) Update bat with logging
        print("[1] updating start_samweb.bat with logging ...")
        sftp = client.open_sftp()
        with sftp.file("C:/samweb/start_samweb.bat", "w") as f:
            f.write(NEW_BAT)
        sftp.close()
        rc, out, _ = run(client, "type C:\\samweb\\start_samweb.bat", timeout=10)
        print(out)

        # 2) Make sure no leftover samweb
        run(client, 'taskkill /F /IM samweb.exe 2>nul', timeout=10)
        time.sleep(2)

        # 3) Delete old log
        run(client, 'del C:\\samweb\\run.log 2>nul', timeout=10)

        # 4) Run the schtask
        print("\n[2] running schtask ...")
        rc, out, _ = run(client, 'schtasks /Run /TN RestartSamweb', timeout=10)
        print(out)

        # 5) Wait + poll log
        print("\n[3] polling run.log ...")
        for i in range(15):
            time.sleep(2)
            rc, out, _ = run(client,
                'if exist C:\\samweb\\run.log (type C:\\samweb\\run.log) else (echo NO_LOG)',
                timeout=10)
            if "NO_LOG" in out:
                print(f"  [{(i+1)*2}s] still no log")
                continue
            print(f"  [{(i+1)*2}s] log so far:")
            print(out)
            if "bootstrap JS injected" in out or "wails app started" in out:
                print("  ==> bootstrap injected!")
                break

        # 6) Final state
        rc, out, _ = run(client, 'tasklist /FI "IMAGENAME eq samweb.exe" /FO CSV /NH', timeout=10)
        print("\n[4] samweb.exe:", out.strip())

        rc, out, _ = run(client,
            'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 5 -UseBasicParsing).Content } catch { \'ERR:\' + $_.Exception.Message }"',
            timeout=15)
        print("[5] /agent/health:", out.strip())

        # 7) Test /agent/eval from shan
        rc, out, _ = run(client,
            'powershell -Command "try { $b = @{\'script\'=\'1+1\'} | ConvertTo-Json; $r = Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/eval -Method Post -Body $b -ContentType \'application/json\' -TimeoutSec 15 -UseBasicParsing -Headers @{Authorization=\'Bearer test-token-2026\'}; $r.Content } catch { \'ERR:\' + $_.Exception.Message }"',
            timeout=25)
        print("\n[6] /agent/eval (1+1):", out.strip())

        # 8) Test /agent/state
        rc, out, _ = run(client,
            'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/state -TimeoutSec 15 -UseBasicParsing -Headers @{Authorization=\'Bearer test-token-2026\'}).Content } catch { \'ERR:\' + $_.Exception.Message }"',
            timeout=25)
        print("\n[7] /agent/state:", out.strip()[:400])

    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Patch start_samweb.bat to add --remote-allow-origins=* so external WS
clients (like our CDP eval script) can connect to port 9222.

Then restart samweb via schtasks, wait for the agent API + CDP to come
up, and dump the current page state.
"""
import json
import os
import socket
import sys
import threading
import time
import urllib.request
import websocket

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


NEW_BAT = (
    "@echo off\r\n"
    "set WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222 --remote-allow-origins=*\r\n"
    "start \"\" /wait C:\\samweb\\samweb.exe\r\n"
)


def start_forwarder(transport, local_port, remote_host, remote_port):
    listen = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listen.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listen.bind(("127.0.0.1", local_port))
    listen.listen(8)
    listen.settimeout(0.1)
    stop = threading.Event()

    def handle(cs):
        try:
            chan = transport.open_channel("direct-tcpip",
                                          (remote_host, remote_port),
                                          ("127.0.0.1", 0))
        except Exception:
            cs.close(); return
        if chan is None:
            cs.close(); return
        def pump(src, dst):
            try:
                while not stop.is_set():
                    b = src.recv(4096)
                    if not b: break
                    dst.sendall(b)
            except Exception: pass
            finally:
                try: dst.shutdown(socket.SHUT_WR)
                except OSError: pass
        threading.Thread(target=pump, args=(cs, chan), daemon=True).start()
        pump(chan, cs); chan.close(); cs.close()

    def loop():
        while not stop.is_set():
            try:
                cs, _ = listen.accept()
            except socket.timeout: continue
            except OSError: return
            threading.Thread(target=handle, args=(cs,), daemon=True).start()

    threading.Thread(target=loop, daemon=True).start()
    return listen, stop


def cdp_eval(ws_url, script, timeout=15, msg_id=1, origin="http://localhost"):
    ws = websocket.create_connection(ws_url, timeout=timeout, origin=origin)
    try:
        msg = json.dumps({
            "id": msg_id,
            "method": "Runtime.evaluate",
            "params": {
                "expression": script,
                "returnByValue": True,
                "awaitPromise": True,
            },
        })
        ws.send(msg)
        while True:
            raw = ws.recv()
            data = json.loads(raw)
            if data.get("id") == msg_id:
                return data
    finally:
        ws.close()


def main():
    verbose = "-v" in sys.argv
    client, proc, _ = open_ssh(verbose=verbose)
    try:
        # 1) Write the new bat file
        print("[1] writing new start_samweb.bat ...")
        # Write to a temp local file, then SFTP it over
        sftp = client.open_sftp()
        with sftp.file("C:/samweb/start_samweb.bat", "w") as f:
            f.write(NEW_BAT)
        sftp.close()
        # Verify
        rc, out, _ = run(client, "type C:\\samweb\\start_samweb.bat", timeout=10)
        print(out)

        # 2) Kill any running samweb.exe
        print("\n[2] killing running samweb.exe ...")
        rc, out, _ = run(client, 'taskkill /F /IM samweb.exe', timeout=10)
        print(out)
        time.sleep(2)

        # 3) Trigger the schtask
        print("\n[3] triggering RestartSamweb ...")
        rc, out, _ = run(client, 'schtasks /Run /TN RestartSamweb', timeout=10)
        print(out)

        # 4) Wait for agent API
        print("\n[4] waiting for /agent/health ...")
        for i in range(20):
            time.sleep(2)
            rc, out, _ = run(client,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:7777/agent/health -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR\' }"',
                timeout=10)
            if "ok" in out:
                print(f"   ready after ~{(i+1)*2}s: {out.strip()}")
                break
        else:
            print("   NEVER ready")
            return

        # 5) Wait for CDP
        print("\n[5] waiting for CDP 9222 ...")
        for i in range(15):
            time.sleep(1.5)
            rc, out, _ = run(client,
                'powershell -Command "try { (Invoke-WebRequest -Uri http://127.0.0.1:9222/json/version -TimeoutSec 3 -UseBasicParsing).Content } catch { \'ERR\' }"',
                timeout=10)
            if "Browser" in out:
                print(f"   ready after ~{(i+1)*1.5}s")
                break
        else:
            print("   NEVER ready")
            return
    finally:
        client.close()
        if proc:
            proc.terminate()

    # 6) Tunnel + query
    print("\n[6] querying CDP via SSH tunnel ...")
    client2, proc2, _ = open_ssh(verbose=verbose)
    try:
        transport = client2.get_transport()
        listen, stop = start_forwarder(transport, 19222, "127.0.0.1", 9222)
        try:
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            pages = [t for t in targets if t.get("type") == "page"]
            if not pages:
                print("ERR: no page targets"); return
            ws_url = pages[0]["webSocketDebuggerUrl"].replace(
                "127.0.0.1:9222", "127.0.0.1:19222")
            print(f"   page: {pages[0].get('url')}")

            script = """
            (function(){
              var omnibox = document.getElementById('omnibox');
              var view = document.getElementById('view');
              var r = {
                omnibox_value: omnibox ? omnibox.value : null,
                iframe_src: view ? view.src : null,
                iframe_srcdoc_present: view ? (!!view.srcdoc) : null,
                parent_title: document.title,
                parent_url: location.href,
                has_methods: !!(window.__samwebAgent && window.__samwebAgent.methods),
                method_names: window.__samwebAgent ? Object.keys(window.__samwebAgent.methods) : []
              };
              try {
                var d = view ? view.contentDocument : null;
                if (d) {
                  r.iframe_doc_url = d.location.href;
                  r.iframe_doc_title = d.title;
                  var innerFrame = d.querySelector('iframe');
                  if (innerFrame) {
                    r.inner_iframe_src = innerFrame.src;
                    try {
                      r.inner_iframe_doc_url = innerFrame.contentDocument.location.href;
                      r.inner_iframe_doc_title = innerFrame.contentDocument.title;
                    } catch(e) { r.inner_iframe_doc_err = e.message; }
                  }
                }
              } catch(e) { r.iframe_doc_err = e.message; }
              return r;
            })()
            """
            print("\n[eval] reading omnibox + iframe state ...")
            resp = cdp_eval(ws_url, script)
            result = resp.get("result", {}).get("result", {})
            if result.get("subtype") == "error":
                print("  ERR:", result.get("description", ""))
            else:
                val = result.get("value")
                print(json.dumps(val, ensure_ascii=False, indent=2))
                print()
                print(f"=== omnibox     : {val.get('omnibox_value')}")
                print(f"=== iframe.src  : {val.get('iframe_src')}")
                print(f"=== iframe doc  : {val.get('iframe_doc_url')}")
                print(f"=== iframe title: {val.get('iframe_doc_title')}")
                print(f"=== methods     : {val.get('method_names')}")
        finally:
            stop.set()
            listen.close()
    finally:
        client2.close()
        if proc2:
            proc2.terminate()


if __name__ == "__main__":
    main()

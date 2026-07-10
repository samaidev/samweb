#!/usr/bin/env python3
"""Load z.ai cookies from ~/.samweb/cdp-cookies.json into the live WebView2
cookie store via CDP Network.setCookie. After this, navigating to z.ai
should show the user as logged in.
"""
import json
import socket
import sys
import threading
import time
import urllib.request
import websocket

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh, run


def start_forwarder(transport, local_port, remote_host, remote_port):
    listen = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listen.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listen.bind(("127.0.0.1", local_port)); listen.listen(8); listen.settimeout(0.1)
    stop = threading.Event()
    def handle(cs):
        try: chan = transport.open_channel("direct-tcpip", (remote_host, remote_port), ("127.0.0.1", 0))
        except Exception: cs.close(); return
        if chan is None: cs.close(); return
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
            try: cs, _ = listen.accept()
            except socket.timeout: continue
            except OSError: return
            threading.Thread(target=handle, args=(cs,), daemon=True).start()
    threading.Thread(target=loop, daemon=True).start()
    return listen, stop


def cdp_call(ws_url, method, params=None, timeout=15, msg_id=1):
    ws = websocket.create_connection(ws_url, timeout=timeout, suppress_origin=True)
    try:
        ws.send(json.dumps({"id": msg_id, "method": method, "params": params or {}}))
        while True:
            data = json.loads(ws.recv())
            if data.get("id") == msg_id: return data
    finally: ws.close()


def cdp_eval(ws_url, script, timeout=30, msg_id=1):
    ws = websocket.create_connection(ws_url, timeout=timeout, suppress_origin=True)
    try:
        ws.send(json.dumps({"id": msg_id, "method": "Runtime.evaluate",
            "params": {"expression": script, "returnByValue": True, "awaitPromise": True}}))
        while True:
            data = json.loads(ws.recv())
            if data.get("id") == msg_id: return data
    finally: ws.close()


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        # 1) Read cdp-cookies.json from shan
        print("[1] reading cdp-cookies.json ...")
        rc, out, _ = run(client, 'type C:\\Users\\Administrator\\.samweb\\cdp-cookies.json', timeout=10)
        cookies = json.loads(out)
        print(f"   {len(cookies)} cookies in file")
        zai = [c for c in cookies if "z.ai" in c.get("domain", "") or "chatglm" in c.get("domain", "")]
        print(f"   {len(zai)} z.ai cookies to inject")

        # 2) Set up forwarder
        t = client.get_transport()
        listen, stop = start_forwarder(t, 19222, "127.0.0.1", 9222)
        try:
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            ws_url = [t for t in targets if t.get("type") == "page"][0]["webSocketDebuggerUrl"].replace("127.0.0.1:9222", "127.0.0.1:19222")

            # 3) Inject each cookie via Network.setCookie
            print(f"\n[2] injecting {len(zai)} cookies via CDP Network.setCookie ...")
            msg_id = 1
            for c in zai:
                # CDP setCookie expects: name, value, domain, path, secure, httpOnly, sameSite, expires
                params = {
                    "name": c["name"],
                    "value": c["value"],
                    "domain": c["domain"],
                    "path": c.get("path", "/"),
                    "secure": c.get("secure", False),
                    "httpOnly": c.get("httpOnly", False),
                }
                if c.get("expires") and not c.get("session"):
                    params["expires"] = c["expires"]
                if c.get("sameSite"):
                    ss = c["sameSite"].lower()
                    if ss in ("strict", "lax", "none"):
                        params["sameSite"] = ss.capitalize()
                r = cdp_call(ws_url, "Network.setCookie", params, msg_id=msg_id)
                msg_id += 1
                success = r.get("result", {}).get("success", False)
                print(f"   {c['domain']:20s} {c['name']:20s} success={success}")

            # 4) Verify by reading cookies back
            print("\n[3] verifying via Network.getAllCookies ...")
            r = cdp_call(ws_url, "Network.getAllCookies", msg_id=999)
            all_cookies = r.get("result", {}).get("cookies", [])
            zai_now = [c for c in all_cookies if "z.ai" in c.get("domain", "")]
            print(f"   {len(zai_now)} z.ai cookies now in store")
            for c in zai_now:
                v = c.get("value", "")
                v_show = v[:40] + "..." if len(v) > 40 else v
                print(f"     {c.get('domain')}: {c.get('name')}={v_show}")

            # 5) Now reload z.ai in the iframe
            print("\n[4] reloading iframe to https://chat.z.ai ...")
            r = cdp_eval(ws_url, """
            (function(){
              var view = document.getElementById('view');
              if (!view) return 'no iframe';
              view.removeAttribute('srcdoc');
              view.src = 'https://chat.z.ai';
              return 'set to ' + view.src;
            })()
            """)
            print("   ->", r.get("result",{}).get("result",{}).get("value"))

            # 6) Wait for load
            print("\n[5] waiting 8s for z.ai to load ...")
            time.sleep(8)

        finally:
            stop.set(); listen.close()
    finally:
        client.close()
        if proc: proc.terminate()


if __name__ == "__main__":
    main()

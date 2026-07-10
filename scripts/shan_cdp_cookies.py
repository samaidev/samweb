#!/usr/bin/env python3
"""Get all cookies from samweb's WebView2 cookie store via CDP
Network.getAllCookies (tunneled through SSH, suppress_origin to bypass
chromium 150's WS origin check).

Filters for z.ai / chatglm.cn domains to see if the user was previously
logged in.
"""
import json
import socket
import sys
import threading
import urllib.request
import websocket

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh


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


def main():
    client, proc, _ = open_ssh(verbose=False)
    try:
        t = client.get_transport()
        listen, stop = start_forwarder(t, 19222, "127.0.0.1", 9222)
        try:
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            ws_url = [t for t in targets if t.get("type") == "page"][0]["webSocketDebuggerUrl"].replace("127.0.0.1:9222", "127.0.0.1:19222")

            # Network.getAllCookies returns ALL cookies in the browser's cookie store
            print("[1] CDP Network.getAllCookies ...")
            r = cdp_call(ws_url, "Network.getAllCookies")
            cookies = r.get("result", {}).get("cookies", [])
            print(f"   total cookies: {len(cookies)}")

            # Group by domain
            by_domain = {}
            for c in cookies:
                d = c.get("domain", "")
                by_domain.setdefault(d, []).append(c)

            print(f"\n   domains ({len(by_domain)}):")
            for d in sorted(by_domain.keys()):
                print(f"     {d}: {len(by_domain[d])} cookie(s)")

            # Filter z.ai / chatglm
            print("\n[2] z.ai / chatglm.cn cookies:")
            zai_cookies = [c for c in cookies if "z.ai" in c.get("domain", "") or "chatglm" in c.get("domain", "")]
            print(f"   {len(zai_cookies)} cookie(s)")
            for c in zai_cookies:
                v = c.get("value", "")
                v_show = v[:50] + "..." if len(v) > 50 else v
                print(f"     {c.get('domain')}: {c.get('name')}={v_show}")

        finally:
            stop.set(); listen.close()
    finally:
        client.close()
        if proc: proc.terminate()


if __name__ == "__main__":
    main()

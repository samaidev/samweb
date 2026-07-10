#!/usr/bin/env python3
"""Bypass the dispatch layer entirely — use CDP Runtime.evaluate to
set iframe.src = 'https://chat.z.ai' directly. Then poll CDP for the
iframe's actual loaded URL.
"""
import json
import socket
import sys
import threading
import time
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
        t = client.get_transport()
        listen, stop = start_forwarder(t, 19222, "127.0.0.1", 9222)
        try:
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            ws_url = [t for t in targets if t.get("type") == "page"][0]["webSocketDebuggerUrl"].replace("127.0.0.1:9222", "127.0.0.1:19222")

            # 1) Set iframe.src directly to z.ai
            print("[1] setting iframe.src = https://chat.z.ai ...")
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

            # 2) Poll iframe state
            for i in range(10):
                time.sleep(2)
                r = cdp_eval(ws_url, """
                (function(){
                  var view = document.getElementById('view');
                  var omnibox = document.getElementById('omnibox');
                  var r = {
                    iframe_src: view ? view.src : null,
                    omnibox_value: omnibox ? omnibox.value : null,
                  };
                  try {
                    var d = view ? view.contentDocument : null;
                    if (d) {
                      r.iframe_doc_url = d.location.href;
                      r.iframe_doc_title = d.title;
                      r.iframe_body_text = (d.body && d.body.innerText || '').slice(0, 300);
                    }
                  } catch(e) { r.iframe_doc_err = e.message; }
                  return JSON.stringify(r);
                })()
                """)
                val = r.get("result",{}).get("result",{}).get("value")
                print(f"  [{(i+1)*2}s] {val}")
                if val and "iframe_doc_url" in val:
                    break

            # 3) Update omnibox to reflect new URL
            print("\n[3] updating omnibox value ...")
            r = cdp_eval(ws_url, """
            (function(){
              var omnibox = document.getElementById('omnibox');
              if (omnibox) omnibox.value = 'https://chat.z.ai';
              return 'omnibox updated';
            })()
            """)
            print("   ->", r.get("result",{}).get("result",{}).get("value"))

        finally:
            stop.set(); listen.close()
    finally:
        client.close()
        if proc: proc.terminate()


if __name__ == "__main__":
    main()

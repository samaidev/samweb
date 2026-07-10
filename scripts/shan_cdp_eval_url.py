#!/usr/bin/env python3
"""Get samweb's current iframe URL by calling CDP Runtime.evaluate via WS.

We connect to the page's webSocketDebuggerUrl (tunneled through SSH),
then run JS that reads:
  - omnibox.value            (what the user typed)
  - view.src                 (the proxy URL or direct URL)
  - view.contentDocument.location.href  (the actual loaded URL, same-origin only)
  - view.contentDocument.title
  - location info from inside the iframe

This is the authoritative answer to "samweb 没把 URL 告诉我".
"""
import json
import socket
import sys
import threading
import urllib.request
import websocket  # websocket-client

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh


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


def cdp_eval(ws_url, script, timeout=15, msg_id=1):
    """Send Runtime.evaluate over a CDP websocket, suppressing the Origin
    header (chromium 150 rejects all external WS origins unless
    --remote-allow-origins is set; suppressing Origin mimics what
    samweb's internal gorilla/websocket dialer does)."""
    ws = websocket.create_connection(
        ws_url, timeout=timeout, suppress_origin=True
    )
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


def cdp_call(ws_url, method, params=None, timeout=15, msg_id=1):
    """Send an arbitrary CDP method."""
    ws = websocket.create_connection(
        ws_url, timeout=timeout, suppress_origin=True
    )
    try:
        msg = json.dumps({"id": msg_id, "method": method, "params": params or {}})
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
        transport = client.get_transport()
        listen, stop = start_forwarder(transport, 19222, "127.0.0.1", 9222)
        try:
            # 1) get the page target's wsUrl
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            pages = [t for t in targets if t.get("type") == "page"]
            if not pages:
                print("ERR: no page targets")
                return
            ws_url = pages[0]["webSocketDebuggerUrl"].replace("127.0.0.1:9222", "127.0.0.1:19222")
            print(f"[cdp] page target: {pages[0].get('url')}")
            print(f"[cdp] ws: {ws_url}")

            # 2) Read omnibox + iframe state from the parent page
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
              };
              try {
                var d = view ? view.contentDocument : null;
                if (d) {
                  r.iframe_doc_url = d.location.href;
                  r.iframe_doc_title = d.title;
                  // try to read inner iframe's URL if it's the proxy page
                  var innerFrame = d.querySelector('iframe');
                  if (innerFrame) {
                    r.inner_iframe_src = innerFrame.src;
                    try {
                      r.inner_iframe_doc_url = innerFrame.contentDocument.location.href;
                      r.inner_iframe_doc_title = innerFrame.contentDocument.title;
                    } catch(e) {
                      r.inner_iframe_doc_err = e.message;
                    }
                  }
                }
              } catch(e) {
                r.iframe_doc_err = e.message;
              }
              return r;
            })()
            """
            print("\n[eval] reading omnibox + iframe state ...")
            resp = cdp_eval(ws_url, script)
            result = resp.get("result", {}).get("result", {})
            if result.get("type") == "object" and result.get("subtype") == "error":
                print("  ERR:", result.get("description", ""))
            else:
                val = result.get("value")
                print(json.dumps(val, ensure_ascii=False, indent=2))

            # 3) Also dump all CDP targets (Target.getTargets) to see iframes
            print("\n[cdp] Target.getTargets ...")
            resp2 = cdp_call(ws_url, "Target.getTargets", msg_id=2)
            targets = resp2.get("result", {}).get("targetInfos", [])
            print(f"  {len(targets)} target(s):")
            for t in targets:
                print(f"    - type={t.get('type')} title={t.get('title','')!r}")
                print(f"      url={t.get('url','')!r}")
        finally:
            stop.set()
            listen.close()
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

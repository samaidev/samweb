#!/usr/bin/env python3
"""Diagnose why /agent/eval times out even though bootstrap JS was injected.

Uses CDP Runtime.evaluate (via SSH-tunneled WS with suppress_origin) to
inspect the state of window.__samwebAgent + __samwebAgentDispatch.
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
            try: cs, _ = listen.accept()
            except socket.timeout: continue
            except OSError: return
            threading.Thread(target=handle, args=(cs,), daemon=True).start()

    threading.Thread(target=loop, daemon=True).start()
    return listen, stop


def cdp_eval(ws_url, script, timeout=15, msg_id=1):
    ws = websocket.create_connection(ws_url, timeout=timeout, suppress_origin=True)
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
        transport = client.get_transport()
        listen, stop = start_forwarder(transport, 19222, "127.0.0.1", 9222)
        try:
            with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                targets = json.loads(r.read())
            pages = [t for t in targets if t.get("type") == "page"]
            if not pages:
                print("ERR: no page targets"); return
            ws_url = pages[0]["webSocketDebuggerUrl"].replace(
                "127.0.0.1:9222", "127.0.0.1:19222")
            print(f"[cdp] page: {pages[0].get('url')}")

            # 1) Check window.__samwebAgent + __samwebAgentDispatch
            print("\n[1] checking window.__samwebAgent ...")
            script = """
            (function(){
              return JSON.stringify({
                has_agent: !!window.__samwebAgent,
                has_dispatch: !!window.__samwebAgentDispatch,
                has_methods: !!(window.__samwebAgent && window.__samwebAgent.methods),
                method_names: window.__samwebAgent ? Object.keys(window.__samwebAgent.methods || {}) : [],
                has_dispatch_fn: window.__samwebAgent ? typeof window.__samwebAgent.dispatch : null,
                location: location.href,
                ui_port_inferred: (function(){
                  var m = (window.__samwebAgent && typeof window.__samwebAgent.toString === 'function') ? '' : '';
                  return m;
                })()
              }, null, 2);
            })()
            """
            resp = cdp_eval(ws_url, script)
            result = resp.get("result", {}).get("result", {})
            if result.get("subtype") == "error":
                print("  ERR:", result.get("description"))
            else:
                print(result.get("value"))

            # 2) Try calling __samwebAgentDispatch directly with a no-op
            print("\n[2] trying direct dispatch call ...")
            script2 = """
            (function(){
              if (!window.__samwebAgentDispatch) return 'no dispatch fn';
              try {
                // Test fetch to /agent/callback
                return fetch('http://127.0.0.1:7777/agent/callback', {
                  method: 'POST',
                  headers: {'Content-Type': 'application/json'},
                  body: JSON.stringify({id: 'test-from-cdp', result: JSON.stringify({ok:true, msg:'hello from cdp'}), error: ''})
                }).then(function(r){
                  return 'fetch OK status=' + r.status;
                }).catch(function(e){
                  return 'fetch ERR: ' + e.message;
                });
              } catch(e) {
                return 'EXC: ' + e.message;
              }
            })()
            """
            resp = cdp_eval(ws_url, script2)
            result = resp.get("result", {}).get("result", {})
            if result.get("subtype") == "error":
                print("  ERR:", result.get("description"))
            else:
                print("  ->", result.get("value"))

            # 3) Show UI_BASE that bootstrapJS computed
            print("\n[3] checking UI_BASE in bootstrapJS scope ...")
            script3 = """
            (function(){
              // The IIFE in bootstrapJS closes over UI_BASE, but we can
              // infer it: __samwebAgent.dispatch calls fetch with
              // UI_BASE + '/agent/callback'. We can monkey-patch fetch
              // to log the URL, but easier: just check what port 7777
              // responds with.
              return fetch('http://127.0.0.1:7777/agent/health')
                .then(function(r){ return r.text(); })
                .then(function(t){ return 'health: ' + t; })
                .catch(function(e){ return 'ERR: ' + e.message; });
            })()
            """
            resp = cdp_eval(ws_url, script3)
            result = resp.get("result", {}).get("result", {})
            print("  ->", result.get("value"))

            # 4) Look at the bootstrapJS source by re-reading the script tags
            print("\n[4] looking at __samwebAgent internals ...")
            script4 = """
            (function(){
              var out = {};
              out.has_agent = !!window.__samwebAgent;
              if (window.__samwebAgent) {
                out.dispatch_type = typeof window.__samwebAgent.dispatch;
                out.methods_count = window.__samwebAgent.methods ? Object.keys(window.__samwebAgent.methods).length : 0;
                out.methods = window.__samwebAgent.methods ? Object.keys(window.__samwebAgent.methods) : [];
              }
              // Check if there's a port we can detect
              out.window_keys = Object.keys(window).filter(function(k){
                return k.indexOf('samweb') >= 0 || k.indexOf('__') === 0;
              });
              return JSON.stringify(out, null, 2);
            })()
            """
            resp = cdp_eval(ws_url, script4)
            result = resp.get("result", {}).get("result", {})
            print("  ->", result.get("value"))
        finally:
            stop.set()
            listen.close()
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

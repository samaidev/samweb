#!/usr/bin/env python3
"""Inject history, reload page, type 'z', screenshot the suggestions."""
import json, socket, sys, threading, urllib.request, websocket, time
sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh
from shan_lib.agent import Agent

def fwd(t, lp, rh, rp):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", lp)); s.listen(8); s.settimeout(0.1)
    stop = threading.Event()
    def h(cs):
        try: ch = t.open_channel("direct-tcpip", (rh, rp), ("127.0.0.1", 0))
        except: cs.close(); return
        def p(a, b):
            try:
                while not stop.is_set():
                    d = a.recv(4096)
                    if not d: break
                    b.sendall(d)
            except: pass
            finally:
                try: b.shutdown(socket.SHUT_WR)
                except: pass
        threading.Thread(target=p, args=(cs, ch), daemon=True).start()
        p(ch, cs); ch.close(); cs.close()
    def l():
        while not stop.is_set():
            try: cs, _ = s.accept()
            except socket.timeout: continue
            except OSError: return
            threading.Thread(target=h, args=(cs,), daemon=True).start()
    threading.Thread(target=l, daemon=True).start()
    return s, stop

def cdp(ws, method, params=None, mid=1):
    w = websocket.create_connection(ws, timeout=15, suppress_origin=True)
    try:
        w.send(json.dumps({"id": mid, "method": method, "params": params or {}}))
        while True:
            d = json.loads(w.recv())
            if d.get("id") == mid: return d
    finally: w.close()

def cdp_eval(ws, script, mid=1):
    w = websocket.create_connection(ws, timeout=15, suppress_origin=True)
    try:
        w.send(json.dumps({"id": mid, "method": "Runtime.evaluate",
            "params": {"expression": script, "returnByValue": True, "awaitPromise": True}}))
        while True:
            d = json.loads(w.recv())
            if d.get("id") == mid: return d
    finally: w.close()

def get_ws():
    client, proc, _ = open_ssh(verbose=False, use_aitun=True)
    t = client.get_transport()
    s, stop = fwd(t, 19222, "127.0.0.1", 9222)
    with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
        tg = json.loads(r.read())
    ws = [x for x in tg if x.get("type") == "page"][0]["webSocketDebuggerUrl"].replace("127.0.0.1:9222", "127.0.0.1:19222")
    return client, proc, s, stop, ws

# Step 1: inject history
print("[1] injecting history ...")
client, proc, s, stop, ws = get_ws()
try:
    d = cdp_eval(ws, """
    (function(){
      var h = [
        {url:'https://chat.z.ai', ts: Date.now()},
        {url:'https://z.ai', ts: Date.now()-1000},
        {url:'https://www.google.com', ts: Date.now()-2000},
        {url:'https://github.com/samaidev/samweb', ts: Date.now()-3000},
        {url:'https://chat.z.ai/agent', ts: Date.now()-4000},
        {url:'https://www.baidu.com/s?wd=test', ts: Date.now()-5000},
        {url:'https://chatglm.cn', ts: Date.now()-6000},
        {url:'https://z.ai/pricing', ts: Date.now()-7000},
      ];
      localStorage.setItem('samweb.history', JSON.stringify(h));
      return 'seeded ' + h.length;
    })()
    """)
    print("   ", d.get("result",{}).get("result",{}).get("value"))
finally:
    stop.set(); s.close(); client.close()
    if proc: proc.terminate()

# Step 2: reload page
print("\n[2] reloading page ...")
client, proc, s, stop, ws = get_ws()
try:
    cdp(ws, "Page.reload")
finally:
    stop.set(); s.close(); client.close()
    if proc: proc.terminate()
time.sleep(4)

# Step 3: type 'z' and check suggestions
print("\n[3] typing 'z' ...")
client, proc, s, stop, ws = get_ws()
try:
    d = cdp_eval(ws, """
    (function(){
      var om = document.getElementById('omnibox');
      if (!om) return 'no omnibox';
      om.focus();
      om.value = 'z';
      om.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertText', data: 'z'}));
      return new Promise(function(resolve){
        setTimeout(function(){
          var sug = document.getElementById('omnibox-suggestions');
          var items = sug ? sug.querySelectorAll('.omnibox-suggestion') : [];
          var out = [];
          items.forEach(function(n){ out.push(n.querySelector('.sug-text').textContent); });
          resolve(JSON.stringify({count: items.length, items: out, visible: sug ? !sug.classList.contains('hidden') : false}));
        }, 300);
      });
    })()
    """)
    print("   ", d.get("result",{}).get("result",{}).get("value"))
finally:
    stop.set(); s.close(); client.close()
    if proc: proc.terminate()

# Step 4: screenshot (suggestions should still be visible)
print("\n[4] screenshot ...")
a = Agent(verbose=False)
try:
    r = a.post("/agent/screenshot-trusted", {"fullPage": False}, timeout=30)
    if isinstance(r, (bytes, bytearray)):
        with open("/home/z/my-project/download/samweb_suggestions.png", "wb") as f:
            f.write(r)
        print(f"   saved {len(r)} bytes")
finally:
    a.close()

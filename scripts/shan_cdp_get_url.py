#!/usr/bin/env python3
"""Query samweb's current page URL via CDP HTTP API on port 9222.

The CDP endpoint at http://127.0.0.1:9222/json lists all page targets
with their url + title, independent of the wails dispatch layer. This
bypasses the broken /agent/state endpoint entirely.

We tunnel 9222 through SSH (paramiko direct-tcpip) and curl /json and
/json/version locally.
"""
import json
import socket
import sys
import threading
import urllib.request

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.ssh import open_ssh


def start_forwarder(transport, local_port, remote_host, remote_port):
    """Start a local TCP listener that forwards to remote_host:remote_port
    through the SSH transport. Returns (listen_sock, stop_event)."""
    listen = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listen.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listen.bind(("127.0.0.1", local_port))
    listen.listen(8)
    listen.settimeout(0.1)
    stop = threading.Event()

    def loop():
        while not stop.is_set():
            try:
                cs, _ = listen.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            threading.Thread(target=handle, args=(cs,), daemon=True).start()

    def handle(cs):
        try:
            chan = transport.open_channel(
                "direct-tcpip",
                (remote_host, remote_port),
                ("127.0.0.1", 0),
            )
        except Exception:
            cs.close()
            return
        if chan is None:
            cs.close()
            return

        def pump(src, dst):
            try:
                while not stop.is_set():
                    b = src.recv(4096)
                    if not b:
                        break
                    dst.sendall(b)
            except Exception:
                pass
            finally:
                try:
                    dst.shutdown(socket.SHUT_WR)
                except OSError:
                    pass

        threading.Thread(target=pump, args=(cs, chan), daemon=True).start()
        pump(chan, cs)
        chan.close()
        cs.close()

    t = threading.Thread(target=loop, daemon=True)
    t.start()
    return listen, stop


def main():
    verbose = "-v" in sys.argv
    client, proc, _ = open_ssh(verbose=verbose)
    try:
        transport = client.get_transport()
        listen, stop = start_forwarder(transport, 19222, "127.0.0.1", 9222)
        try:
            # 1) /json/version
            print("[1] GET http://127.0.0.1:19222/json/version ...")
            try:
                with urllib.request.urlopen("http://127.0.0.1:19222/json/version", timeout=10) as r:
                    ver = json.loads(r.read())
                print("   ->", json.dumps(ver, ensure_ascii=False, indent=2))
            except Exception as e:
                print("   ERR:", e)
                return

            # 2) /json — list all page targets
            print("\n[2] GET http://127.0.0.1:19222/json ...")
            try:
                with urllib.request.urlopen("http://127.0.0.1:19222/json", timeout=10) as r:
                    targets = json.loads(r.read())
            except Exception as e:
                print("   ERR:", e)
                return

            print(f"   -> {len(targets)} target(s)")
            for i, t in enumerate(targets):
                print(f"\n  [{i}] type={t.get('type')} title={t.get('title','')!r}")
                print(f"      url={t.get('url','')!r}")
                print(f"      wsUrl={t.get('webSocketDebuggerUrl','')[:80]}")

            # 3) Summary
            pages = [t for t in targets if t.get("type") == "page"]
            print(f"\n=== {len(pages)} page target(s) ===")
            for p in pages:
                print(f"  Title: {p.get('title','')}")
                print(f"  URL  : {p.get('url','')}")
        finally:
            stop.set()
            listen.close()
    finally:
        client.close()
        if proc:
            proc.terminate()


if __name__ == "__main__":
    main()

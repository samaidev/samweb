"""HTTP client for samweb's agent API on shan.

Since port 7777 is only bound to 127.0.0.1 on shan, we tunnel all
requests through SSH (paramiko's port-forwarding channel). This module
exposes a simple `req()` helper that hides the tunneling.

Usage:
    from shan_lib.agent import Agent
    a = Agent()           # opens SSH + tunnel
    r = a.get("/agent/health")
    r = a.post("/agent/state", {})
"""
import json
import os
import sys
import threading
import urllib.request
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from shan_lib.ssh import open_ssh, HOST


class Agent:
    """HTTP client for samweb's agent API, tunneled via SSH."""

    def __init__(self, token="test-token-2026", local_port=17777, verbose=False):
        self.token = token
        self.local_port = local_port
        self.verbose = verbose
        self._client, self._proc, _ = open_ssh(verbose=verbose)
        # Set up a persistent port-forward
        self._transport = self._client.get_transport()
        # Sanity check
        chan = self._transport.open_channel(
            "direct-tcpip",
            ("127.0.0.1", 7777),
            ("127.0.0.1", 0),
        )
        if chan is None:
            raise RuntimeError("failed to open SSH channel to 7777")
        chan.close()
        # Start a local listener that forwards each connection through SSH
        import socket
        self._listen_sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._listen_sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._listen_sock.bind(("127.0.0.1", local_port))
        self._listen_sock.listen(8)
        self._listen_sock.settimeout(0.1)
        self._stop = threading.Event()
        self._fwd_thread = threading.Thread(target=self._forward_loop, daemon=True)
        self._fwd_thread.start()
        self.base = f"http://127.0.0.1:{local_port}"
        if verbose:
            print(f"[agent] tunnel ready at {self.base}")

    def _forward_loop(self):
        import socket
        while not self._stop.is_set():
            try:
                client_sock, _ = self._listen_sock.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            threading.Thread(target=self._handle_conn, args=(client_sock,), daemon=True).start()

    def _handle_conn(self, client_sock):
        import socket
        try:
            chan = self._transport.open_channel(
                "direct-tcpip",
                ("127.0.0.1", 7777),
                ("127.0.0.1", 0),
            )
        except Exception:
            client_sock.close()
            return
        if chan is None:
            client_sock.close()
            return

        def pump(src, dst):
            try:
                while not self._stop.is_set():
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

        threading.Thread(target=pump, args=(client_sock, chan), daemon=True).start()
        pump(chan, client_sock)
        chan.close()
        client_sock.close()

    def req(self, method, path, body=None, timeout=60):
        url = self.base + path
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        r = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(r, timeout=timeout) as resp:
                payload = resp.read()
                if resp.status >= 400:
                    raise RuntimeError(f"{method} {path}: HTTP {resp.status}: {payload[:300]}")
                ctype = resp.headers.get("Content-Type", "")
                if ctype.startswith("application/json"):
                    return json.loads(payload)
                return payload
        except urllib.error.HTTPError as e:
            body = e.read().decode(errors="replace")
            raise RuntimeError(f"{method} {path}: HTTP {e.code}: {body[:300]}") from None

    def get(self, path, timeout=60):
        return self.req("GET", path, timeout=timeout)

    def post(self, path, body=None, timeout=60):
        return self.req("POST", path, body=body, timeout=timeout)

    def eval(self, script, timeout=60):
        """Run a JS eval, return (status, value_str)."""
        r = self.post("/agent/eval", {"script": script}, timeout=timeout)
        v = r.get("value") if isinstance(r, dict) else r
        if isinstance(v, dict) and "value" in v:
            v = v["value"]
        return 200, str(v)

    def state(self):
        return self.get("/agent/state")

    def screenshot(self, path="/agent/screenshot-trusted", timeout=30):
        """Take a CDP screenshot. Returns raw PNG bytes."""
        return self.post(path, {})

    def close(self):
        self._stop.set()
        try:
            self._listen_sock.close()
        except OSError:
            pass
        try:
            self._client.close()
        except OSError:
            pass
        try:
            self._proc.terminate()
        except OSError:
            pass

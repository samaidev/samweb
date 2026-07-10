"""SSH transport to shan.aitun.cc via aitun ssh-proxy or direct SSH + paramiko.

Supports two connection modes:
  1. Direct SSH (default) — paramiko connects directly to host:port
  2. aitun ssh-proxy — when AITUN is set or direct SSH fails

Usage:
    from shan_lib.ssh import open_ssh, run, run_many

    client, proc, _ = open_ssh()
    rc, out, err = run(client, "tasklist /FI \\"IMAGENAME eq samweb.exe\\"")
    print(out)
    client.close(); proc.terminate()

    # Force aitun mode
    client, proc, _ = open_ssh(use_aitun=True)
"""
import os
import socket
import subprocess
import sys
import threading
import time

import paramiko

AITUN = os.environ.get('AITUN_PATH', '/home/z/.venv/bin/aitun')
HOST = os.environ.get('SHAN_HOST', 'shan.aitun.cc')
PORT = int(os.environ.get('SHAN_PORT', '22'))
USER = os.environ.get('SHAN_USER', 'Administrator')
PASS = os.environ.get('SHAN_PASS', 'dongshan168')


class PipeSocket:
    """Adapter so paramiko can use a subprocess's stdin/stdout as a socket."""

    def __init__(self, proc):
        self.proc = proc
        self._client, self._bridge = socket.socketpair()
        self._bridge.setblocking(False)
        self._stop = threading.Event()
        self._reader = threading.Thread(target=self._pump_stdout, daemon=True)
        self._reader.start()
        self._writer = threading.Thread(target=self._pump_stdin, daemon=True)
        self._writer.start()
        self._closed = False

    def _pump_stdout(self):
        try:
            while not self._stop.is_set():
                b = self.proc.stdout.read(4096)
                if not b:
                    break
                while b and not self._stop.is_set():
                    try:
                        n = self._bridge.send(b)
                        b = b[n:]
                    except BlockingIOError:
                        time.sleep(0.001)
                    except OSError:
                        return
        finally:
            try:
                self._bridge.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    def _pump_stdin(self):
        try:
            while not self._stop.is_set():
                try:
                    b = self._bridge.recv(4096)
                except BlockingIOError:
                    time.sleep(0.001)
                    continue
                except OSError:
                    return
                if not b:
                    return
                self.proc.stdin.write(b)
                self.proc.stdin.flush()
        except OSError:
            pass

    def recv(self, n):
        return self._client.recv(n)

    def send(self, b):
        return self._client.send(b)

    def sendall(self, b):
        return self._client.sendall(b)

    def close(self):
        if self._closed:
            return
        self._closed = True
        self._stop.set()
        for c in (self._client, self._bridge):
            try:
                c.close()
            except OSError:
                pass
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.stdout.close()
        except OSError:
            pass
        try:
            self.proc.terminate()
        except OSError:
            pass

    def settimeout(self, t):
        self._client.settimeout(t)

    def getpeername(self):
        return (HOST, PORT)

    def fileno(self):
        return self._client.fileno()

    def gettimeout(self):
        return self._client.gettimeout()


def _connect_direct(verbose=False):
    """Try direct paramiko SSH connection. Returns (client, None, None)."""
    if verbose:
        print(f"[ssh] direct SSH to {USER}@{HOST}:{PORT}")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        hostname=HOST, port=PORT, username=USER, password=PASS,
        look_for_keys=False, allow_agent=False, timeout=30,
        banner_timeout=30, auth_timeout=30,
    )
    return client, None, None


def _connect_aitun(verbose=False):
    """Connect via aitun ssh-proxy. Returns (client, proc, sock)."""
    if not os.path.isfile(AITUN) or not os.access(AITUN, os.X_OK):
        raise FileNotFoundError(f"aitun not found at {AITUN}")
    if verbose:
        print(f"[ssh] spawning: {AITUN} ssh-proxy {HOST} {PORT}")
    proc = subprocess.Popen(
        [AITUN, "ssh-proxy", HOST, str(PORT)],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        bufsize=0,
    )

    def drain_stderr():
        while True:
            line = proc.stderr.readline()
            if not line:
                break
            if verbose:
                sys.stderr.write(f"[aitun] {line.decode(errors='replace').rstrip()}\n")
    threading.Thread(target=drain_stderr, daemon=True).start()

    time.sleep(1)
    if proc.poll() is not None:
        raise RuntimeError(f"aitun exited early with code {proc.returncode}")

    sock = PipeSocket(proc)
    if verbose:
        print(f"[ssh] paramiko via aitun to {USER}@{HOST}")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        hostname=HOST, port=PORT, username=USER, password=PASS,
        look_for_keys=False, allow_agent=False, timeout=30,
        sock=sock, banner_timeout=30, auth_timeout=30,
    )
    return client, proc, sock


def _find_aitun():
    """Search for aitun binary."""
    candidates = [
        AITUN,
        '/usr/local/bin/aitun',
        '/usr/bin/aitun',
    ]
    for c in candidates:
        if c and os.path.isfile(c) and os.access(c, os.X_OK):
            return c
    try:
        r = subprocess.run(['which', 'aitun'], capture_output=True, timeout=5)
        if r.returncode == 0:
            return r.stdout.decode().strip()
    except Exception:
        pass
    return None


def open_ssh(verbose=False, use_aitun=None):
    """Open an SSH client to shan.

    If use_aitun is None (default), tries direct SSH first, falls back to aitun.
    If use_aitun is True, uses aitun directly.
    If use_aitun is False, uses direct SSH only.

    Returns (client, proc_or_None, sock_or_None).
    """
    if use_aitun is True:
        return _connect_aitun(verbose=verbose)

    if use_aitun is False:
        return _connect_direct(verbose=verbose)

    # Auto: try direct first, fall back to aitun
    try:
        return _connect_direct(verbose=verbose)
    except Exception as e:
        if verbose:
            print(f"[ssh] direct failed: {e}, trying aitun...")
        aitun_path = _find_aitun()
        if aitun_path:
            global AITUN
            AITUN = aitun_path
            return _connect_aitun(verbose=verbose)
        raise RuntimeError(
            f"Direct SSH failed ({e}) and aitun not found. "
            f"Set AITUN_PATH env var or use_aitun=True."
        ) from e


def run(client, cmd, timeout=120):
    """Run a command via SSH, return (rc, stdout, stderr)."""
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode("utf-8", errors="replace")
    err = stderr.read().decode("utf-8", errors="replace")
    rc = stdout.channel.recv_exit_status()
    return rc, out, err


def run_many(client, cmds, timeout=120):
    """Run a list of commands, return list of (rc, out, err)."""
    return [run(client, c, timeout=timeout) for c in cmds]
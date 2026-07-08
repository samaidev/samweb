"""SSH transport to shan.aitun.cc via aitun ssh-proxy + paramiko.

This is the same approach as scripts/probe_shan.py but factored into a
reusable module so all the zai_*.py scripts can share one connection
helper.

Usage:
    from shan_lib.ssh import open_ssh, run, run_many

    client, proc, _ = open_ssh()
    rc, out, err = run(client, "tasklist /FI \"IMAGENAME eq samweb.exe\"")
    print(out)
    client.close(); proc.terminate()
"""
import os
import socket
import subprocess
import sys
import threading
import time

import paramiko

AITUN = "/home/z/.venv/bin/aitun"
HOST = "shan.aitun.cc"
PORT = "22"
USER = "Administrator"
PASS = "dongshan168"


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
        return (HOST, int(PORT))

    def fileno(self):
        return self._client.fileno()

    def gettimeout(self):
        return self._client.gettimeout()


def open_ssh(verbose=False):
    """Open an SSH client to shan via aitun. Returns (client, proc, sock)."""
    if verbose:
        print(f"[ssh] spawning aitun ssh-proxy {HOST} {PORT} ...")
    proc = subprocess.Popen(
        [AITUN, "ssh-proxy", HOST, PORT],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
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
        print(f"[ssh] connecting via paramiko to {USER}@{HOST} ...")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        hostname=HOST, port=int(PORT), username=USER, password=PASS,
        look_for_keys=False, allow_agent=False, timeout=30,
        sock=sock, banner_timeout=30, auth_timeout=30,
    )
    if verbose:
        print("[ssh] connected")
    return client, proc, sock


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

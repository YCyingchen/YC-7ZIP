#!/usr/bin/env python3
"""Local port forward over SSH, for testing a service that only binds to the
NAS loopback interface.

    python tools/portforward.py 8091 192.168.1.9 8090

Credentials come from .env.local, same as tools/nas.py.
"""

from __future__ import annotations

import os
import select
import socket
import sys
import threading
from pathlib import Path

import paramiko

ROOT = Path(__file__).resolve().parent.parent


def load_env() -> None:
    env_file = ROOT / ".env.local"
    if not env_file.is_file():
        return
    for raw in env_file.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        os.environ.setdefault(key.strip(), value.strip().strip('"').strip("'"))


def pipe(src: socket.socket, dst: socket.socket) -> None:
    try:
        while True:
            readable, _, _ = select.select([src], [], [], 60)
            if not readable:
                break
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def main() -> int:
    if len(sys.argv) < 4:
        print(__doc__)
        return 2
    local_port = int(sys.argv[1])
    host = sys.argv[2]
    remote_port = int(sys.argv[3])

    load_env()
    password = os.environ.get("NAS_PASS")
    if not password:
        print("NAS_PASS 未设置", file=sys.stderr)
        return 2

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        hostname=host,
        port=int(os.environ.get("NAS_PORT", "22")),
        username=os.environ.get("NAS_USER", "root"),
        password=password,
        timeout=20,
        look_for_keys=False,
        allow_agent=False,
    )

    listener = socket.socket()
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", local_port))
    listener.listen(16)
    print(f"forwarding 127.0.0.1:{local_port} -> {host}:{remote_port}", flush=True)

    try:
        while True:
            conn, _ = listener.accept()
            try:
                chan = client.get_transport().open_channel(
                    "direct-tcpip", ("127.0.0.1", remote_port), conn.getpeername()
                )
            except Exception as exc:  # noqa: BLE001 - report and keep serving
                print(f"channel failed: {exc}", file=sys.stderr, flush=True)
                conn.close()
                continue
            threading.Thread(target=pipe, args=(conn, chan), daemon=True).start()
            threading.Thread(target=pipe, args=(chan, conn), daemon=True).start()
    except KeyboardInterrupt:
        pass
    finally:
        listener.close()
        client.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Minimal SSH/SFTP helper for the fnOS NAS (Windows dev side).

Credentials come from the environment (or a gitignored .env.local):
    NAS_HOST (default 192.168.1.9)
    NAS_USER (default root)
    NAS_PASS

Usage:
    python tools/nas.py run  "<shell command>"
    python tools/nas.py put  <local-file> <remote-path>
    python tools/nas.py get  <remote-path> <local-file>
    python tools/nas.py putdir <local-dir> <remote-dir>
"""

from __future__ import annotations

import io
import os
import shlex
import stat
import sys
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


def connect() -> paramiko.SSHClient:
    load_env()
    host = os.environ.get("NAS_HOST", "192.168.1.9")
    user = os.environ.get("NAS_USER", "root")
    password = os.environ.get("NAS_PASS")
    if not password:
        sys.exit("NAS_PASS is not set (env or .env.local)")

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        hostname=host,
        port=int(os.environ.get("NAS_PORT", "22")),
        username=user,
        password=password,
        timeout=20,
        banner_timeout=30,
        auth_timeout=30,
        look_for_keys=False,
        allow_agent=False,
    )
    return client


def cmd_run(argv: list[str]) -> int:
    command = argv[0]
    client = connect()
    try:
        _, stdout, stderr = client.exec_command(command, timeout=None, get_pty=False)
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace")
        code = stdout.channel.recv_exit_status()
        if out:
            sys.stdout.write(out)
        if err:
            sys.stderr.write(err)
        return code
    finally:
        client.close()


def cmd_put(argv: list[str]) -> int:
    local, remote = argv
    client = connect()
    try:
        remote_dir = posix_dirname(remote)
        run_checked(client, f"mkdir -p {shlex.quote(remote_dir)}")
        sftp = client.open_sftp()
        src = Path(local)
        if src.suffix in (".sh", ".service", ".conf"):
            # Shell scripts written on Windows must reach Linux with LF only;
            # a stray CR breaks the shebang and every variable assignment.
            text = src.read_text(encoding="utf-8").replace("\r\n", "\n")
            sftp.putfo(io.BytesIO(text.encode("utf-8")), remote)
        else:
            sftp.put(local, remote)
        print(f"uploaded {local} -> {remote}")
        sftp.close()
        return 0
    finally:
        client.close()


def cmd_get(argv: list[str]) -> int:
    remote, local = argv
    client = connect()
    try:
        sftp = client.open_sftp()
        Path(local).parent.mkdir(parents=True, exist_ok=True)
        sftp.get(remote, local)
        print(f"downloaded {remote} -> {local}")
        sftp.close()
        return 0
    finally:
        client.close()


def cmd_putdir(argv: list[str]) -> int:
    local_dir, remote_dir = argv
    client = connect()
    try:
        base = Path(local_dir)
        remote_dir = remote_dir.rstrip("/")
        # Create every directory in one server-side mkdir -p. Doing this over
        # SFTP instead would need POSIX path semantics, which Windows pathlib
        # does not give us.
        wanted = {remote_dir}
        for path in sorted(base.rglob("*")):
            rel = path.relative_to(base).as_posix()
            if path.is_dir():
                wanted.add(f"{remote_dir}/{rel}")
            else:
                wanted.add(posix_dirname(f"{remote_dir}/{rel}"))
        run_checked(client, "mkdir -p " + " ".join(shlex.quote(d) for d in sorted(wanted)))

        sftp = client.open_sftp()
        count = 0
        for path in sorted(base.rglob("*")):
            if path.is_dir():
                continue
            rel = path.relative_to(base).as_posix()
            sftp.put(str(path), f"{remote_dir}/{rel}")
            count += 1
        sftp.close()
        print(f"uploaded {count} files: {local_dir} -> {remote_dir}")
        return 0
    finally:
        client.close()


def posix_dirname(remote: str) -> str:
    """Parent directory of a POSIX path, independent of the local OS."""
    trimmed = remote.rstrip("/")
    if "/" not in trimmed:
        return "."
    parent = trimmed.rsplit("/", 1)[0]
    return parent or "/"


def run_checked(client: paramiko.SSHClient, command: str) -> str:
    _, stdout, stderr = client.exec_command(command, timeout=60)
    out = stdout.read().decode("utf-8", "replace")
    err = stderr.read().decode("utf-8", "replace")
    if stdout.channel.recv_exit_status() != 0:
        raise RuntimeError(f"remote command failed: {command}\n{err}")
    return out


COMMANDS = {
    "run": cmd_run,
    "put": cmd_put,
    "get": cmd_get,
    "putdir": cmd_putdir,
}


def main() -> int:
    if len(sys.argv) < 3 or sys.argv[1] not in COMMANDS:
        print(__doc__)
        return 2
    return COMMANDS[sys.argv[1]](sys.argv[2:])


if __name__ == "__main__":
    raise SystemExit(main())

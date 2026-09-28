#!/usr/bin/env python3
"""把 GitHub Actions 机密写进仓库。

用 GitHub 的公钥做 libsodium sealed box 加密，这是写 secret 的唯一方式
（明文不允许直接上传）。

    python tools/set-secrets.py DOCKERHUB_USERNAME ycyingchen
    python tools/set-secrets.py DOCKERHUB_TOKEN dckr_pat_xxx
    python tools/set-secrets.py --list

令牌优先取 GITHUB_TOKEN 环境变量，其次读 .env.local 里的 GITHUB_TOKEN。
"""

from __future__ import annotations

import base64
import json
import os
import sys
import urllib.request
from pathlib import Path

from nacl import encoding, public

ROOT = Path(__file__).resolve().parent.parent
REPO = os.environ.get("GITHUB_REPO", "YCyingchen/YC-7ZIP")
API = "https://api.github.com"


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


def token() -> str:
    value = os.environ.get("GITHUB_TOKEN", "").strip()
    if not value:
        sys.exit("没有可用的 GitHub 令牌：请设置 GITHUB_TOKEN，或写进 .env.local")
    return value


def call(method: str, path: str, payload=None):
    body = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(API + path, data=body, method=method)
    req.add_header("Authorization", f"Bearer {token()}")
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("User-Agent", "yc7zip-setup")
    if body:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as res:
        raw = res.read()
        return json.loads(raw) if raw else {}


def encrypt(public_key: str, value: str) -> str:
    """libsodium sealed box：只有仓库私钥能解开，GitHub 也只存密文。"""
    pk = public.PublicKey(public_key.encode(), encoding.Base64Encoder())
    sealed = public.SealedBox(pk).encrypt(value.encode())
    return base64.b64encode(sealed).decode()


def main() -> int:
    load_env()

    if len(sys.argv) >= 2 and sys.argv[1] == "--list":
        data = call("GET", f"/repos/{REPO}/actions/secrets")
        print(f"仓库 {REPO} 现有机密 {data.get('total_count', 0)} 项")
        for item in data.get("secrets", []):
            print(f"  - {item['name']}  更新于 {item['updated_at']}")
        return 0

    if len(sys.argv) != 3:
        print(__doc__)
        return 2

    name, value = sys.argv[1], sys.argv[2]
    if not value.strip():
        sys.exit("值不能为空")

    key = call("GET", f"/repos/{REPO}/actions/secrets/public-key")
    encrypted = encrypt(key["key"], value)
    call(
        "PUT",
        f"/repos/{REPO}/actions/secrets/{name}",
        {"encrypted_value": encrypted, "key_id": key["key_id"]},
    )
    print(f"已写入 {name}（长度 {len(value)}，密文 {len(encrypted)} 字节）")
    return 0


if __name__ == "__main__":
    sys.exit(main())

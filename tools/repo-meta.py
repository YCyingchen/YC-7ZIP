#!/usr/bin/env python3
"""设置 GitHub 仓库的元信息（描述、主页、Topics）。

为什么单独写一个：用 PowerShell 的 ConvertTo-Json + Invoke-RestMethod 发
中文时，5.1 默认按 ASCII 编码请求体，中文会被整个替换成 `?`，仓库描述就
变成一串问号。这里显式用 UTF-8 编码请求体。

    python tools/repo-meta.py show
    python tools/repo-meta.py set
"""

from __future__ import annotations

import json
import os
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REPO = os.environ.get("GITHUB_REPO", "YCyingchen/YC-7ZIP")
API = "https://api.github.com"

DESCRIPTION = (
    "网页端压缩解压工具：直读 NAS 文件，支持 7z / ZIP / RAR / TAR 等格式，"
    "支持分卷压缩与分卷解压，可部署为 Docker 与飞牛 fnOS 应用"
)

TOPICS = [
    "nas", "7zip", "zip", "rar", "archive", "unrar",
    "self-hosted", "docker", "fnos", "golang", "web-ui",
]


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
    # 关键：显式 UTF-8 编码，并声明 charset
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(API + path, data=body, method=method)
    req.add_header("Authorization", f"Bearer {token()}")
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("User-Agent", "yc7zip-setup")
    if body:
        req.add_header("Content-Type", "application/json; charset=utf-8")
    try:
        with urllib.request.urlopen(req, timeout=30) as res:
            raw = res.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")
        needed = exc.headers.get("x-accepted-github-permissions", "")
        print(f"  {method} {path} -> {exc.code}", file=sys.stderr)
        print(f"    {detail}", file=sys.stderr)
        if needed:
            print(f"    需要的权限: {needed}", file=sys.stderr)
        raise


def cmd_show(_) -> int:
    repo = call("GET", f"/repos/{REPO}")
    print(f"仓库：{repo['full_name']}")
    print(f"描述：{repo.get('description') or '(空)'}")
    print(f"主页：{repo.get('homepage') or '(空)'}")
    topics = call("GET", f"/repos/{REPO}/topics")
    print(f"Topics：{', '.join(topics.get('names', [])) or '(空)'}")
    return 0


def cmd_set(_) -> int:
    # 先看有没有已经损坏的（问号），便于确认这次真的修好了
    before = call("GET", f"/repos/{REPO}").get("description") or ""
    if "?" in before:
        print(f"当前描述含问号（编码损坏的痕迹）：{before[:40]}…")
    else:
        print(f"当前描述：{before or '(空)'}")

    # 注意是 PATCH 不是 PUT：对 /repos/{owner}/{repo} 发 PUT 会得到 404，
    # 看起来像权限不足，其实是没有这个路由。
    call("PATCH", f"/repos/{REPO}", {"description": DESCRIPTION})
    print(f"\n已写入描述（{len(DESCRIPTION)} 字符）：{DESCRIPTION}")

    call("PUT", f"/repos/{REPO}/topics", {"names": TOPICS})
    print(f"已写入 Topics：{', '.join(TOPICS)}")

    repo = call("GET", f"/repos/{REPO}")
    after = repo.get("description") or ""
    ok = after == DESCRIPTION
    print(f"\n回读校验：{'一致 ✅' if ok else '不一致 ❌'}")
    if not ok:
        print(f"  期望：{DESCRIPTION}")
        print(f"  实际：{after}")
    if "?" in after:
        print("  仍然含问号，编码问题没解决")
        return 1
    return 0 if ok else 1


def main() -> int:
    load_env()
    commands = {"show": cmd_show, "set": cmd_set}
    if len(sys.argv) < 2 or sys.argv[1] not in commands:
        print(__doc__)
        return 2
    return commands[sys.argv[1]](None)


if __name__ == "__main__":
    sys.exit(main())

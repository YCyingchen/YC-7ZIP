#!/usr/bin/env python3
"""版本号工具 —— 本项目的唯一版本来源。

规则:

    zip<年份后两位><月份>.<当月修订号>

    zip2609.001
    │  │  │   └── 该月第 1 次修改
    │  │  └────── 09 月
    │  └───────── 2026 年
    └──────────── 项目前缀

VERSION 文件是唯一来源；Docker 标签、二进制里的 main.version、Release
标签全部由它推导。

例外：fnpack 打包 fpk 时只接受 semver（x.y.z[-r]），所以 manifest 里写
的是换算值。用 `fpk` 子命令取值、`sync-manifest` 写回文件。

    python tools/version.py show            # zip2609.001
    python tools/version.py fpk             # 26.9.1
    python tools/version.py check           # 校验格式，失败退出码 1
    python tools/version.py bump            # zip2609.001 -> zip2609.002
    python tools/version.py bump --month    # 跨月重置 -> zip2610.001
    python tools/version.py sync-manifest   # 把 fpk 值写进 deploy/fpk/manifest

注意这里刻意用 Python 而不是 shell：`printf '%d' 09` 在 shell 里会被当成
八进制并报 invalid octal number，月份 09 会算成 0。整数就是整数。
"""

from __future__ import annotations

import argparse
import datetime
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
VERSION_FILE = ROOT / "VERSION"
CHANNEL_FILE = ROOT / "CHANNEL"
MANIFEST = ROOT / "deploy" / "fpk" / "manifest"

PATTERN = re.compile(r"^zip(\d{2})(\d{2})\.(\d{3})$")

# 发布通道。版本号本身不变，通道决定这个构建发到哪儿、被当成什么：
#   test   -> 测试版：镜像打 :test，GitHub Release 标记为预发布
#   stable -> 正式版：镜像打 :latest（同时保留不可变的版本标签）
CHANNELS = ("test", "stable")
DEFAULT_CHANNEL = "test"


def read_channel() -> str:
    """返回当前发布通道，环境变量 CHANNEL 优先。"""
    env = os.environ.get("CHANNEL", "").strip().lower()
    if env:
        return env
    if CHANNEL_FILE.is_file():
        value = CHANNEL_FILE.read_text(encoding="utf-8").strip().lower()
        if value:
            return value
    return DEFAULT_CHANNEL


def channel_label(channel: str) -> str:
    return {"test": "测试版", "stable": "正式版"}.get(channel, channel)


def read_version() -> str:
    """返回当前版本号；环境变量 VERSION 优先，方便 CI 覆盖。"""
    env = os.environ.get("VERSION", "").strip()
    if env:
        return env
    if VERSION_FILE.is_file():
        return VERSION_FILE.read_text(encoding="utf-8").strip()
    return "dev"


def parse(version: str) -> tuple[int, int, int]:
    """拆成 (年, 月, 修订号)。格式不对时抛 ValueError。"""
    m = PATTERN.match(version)
    if not m:
        raise ValueError(
            f"版本号格式不对：{version!r}\n"
            f"应为 zip<YY><MM>.<NNN>，例如 zip2609.001"
        )
    year, month, revision = (int(g) for g in m.groups())
    if not 1 <= month <= 12:
        raise ValueError(f"月份必须是 01-12：{version!r}")
    return year, month, revision


def fpk_version(version: str) -> str:
    """换算成 fnpack 接受的 semver：zip2609.001 -> 26.9.1。"""
    year, month, revision = parse(version)
    return f"{year}.{month}.{revision}"


# ------------------------------------------------------------------ 子命令


def cmd_show(_: argparse.Namespace) -> int:
    print(read_version())
    return 0


def cmd_fpk(_: argparse.Namespace) -> int:
    version = read_version()
    try:
        print(fpk_version(version))
    except ValueError as exc:
        print(exc, file=sys.stderr)
        return 1
    return 0


def cmd_check(_: argparse.Namespace) -> int:
    version = read_version()
    try:
        year, month, revision = parse(version)
    except ValueError as exc:
        print(exc, file=sys.stderr)
        return 1
    channel = read_channel()
    if channel not in CHANNELS:
        print(f"未知的发布通道：{channel}（可选 {', '.join(CHANNELS)}）", file=sys.stderr)
        return 1
    print(
        f"版本号合法：{version}（{year} 年 {month} 月第 {revision} 次修改）"
        f"，通道 {channel}（{channel_label(channel)}）"
    )
    if channel == "test":
        print("  提醒：当前是测试版，镜像会打 :test，Release 会标记为预发布")
    if MANIFEST.is_file():
        want = fpk_version(version)
        got = manifest_version()
        if got != want:
            print(
                f"deploy/fpk/manifest 的版本是 {got}，应为 {want}"
                f"（跑一次 `version.py sync-manifest` 可修正）",
                file=sys.stderr,
            )
            return 1
        print(f"manifest 版本一致：{got}")
    return 0


def cmd_channel(args: argparse.Namespace) -> int:
    """查看或切换发布通道。"""
    if not args.set:
        print(read_channel())
        return 0
    value = args.set.strip().lower()
    if value not in CHANNELS:
        print(f"未知的发布通道：{value}（可选 {', '.join(CHANNELS)}）", file=sys.stderr)
        return 1
    old = read_channel()
    CHANNEL_FILE.write_text(value + "\n", encoding="utf-8", newline="\n")
    print(f"发布通道已切换：{old}（{channel_label(old)}） -> {value}（{channel_label(value)}）")
    if value == "stable":
        print("  正式版会覆盖镜像的 :latest 标签，GitHub Release 不再是预发布")
    return 0


def cmd_bump(args: argparse.Namespace) -> int:
    current = read_version()
    try:
        year, month, revision = parse(current)
    except ValueError as exc:
        print(exc, file=sys.stderr)
        return 1

    now = datetime.date.today().strftime("%y%m")
    this_month = f"{year:02d}{month:02d}"
    if args.month or now != this_month:
        year, month, revision = int(now[:2]), int(now[2:]), 1
    else:
        revision += 1

    new = f"zip{year:02d}{month:02d}.{revision:03d}"
    VERSION_FILE.write_text(new + "\n", encoding="utf-8", newline="\n")
    print(f"版本号已更新：{current} -> {new}")
    return 0


def cmd_sync_manifest(_: argparse.Namespace) -> int:
    version = read_version()
    try:
        want = fpk_version(version)
    except ValueError as exc:
        print(exc, file=sys.stderr)
        return 1
    if not MANIFEST.is_file():
        print(f"找不到 {MANIFEST}", file=sys.stderr)
        return 1
    text = MANIFEST.read_text(encoding="utf-8")
    updated = re.sub(r"(?m)^version\s*=.*$", f"version               = {want}", text)
    if updated == text:
        print(f"manifest 版本已是 {want}")
        return 0
    MANIFEST.write_text(updated, encoding="utf-8", newline="\n")
    print(f"manifest 版本已同步：{want}（来自 {version}）")
    return 0


def manifest_version() -> str:
    text = MANIFEST.read_text(encoding="utf-8")
    m = re.search(r"(?m)^version\s*=\s*(\S+)\s*$", text)
    return m.group(1) if m else ""


def main() -> int:
    parser = argparse.ArgumentParser(description="YC-7ZIP 版本号工具")
    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("show", help="打印当前版本号").set_defaults(func=cmd_show)
    sub.add_parser("fpk", help="打印 fnpack 用的 semver 换算值").set_defaults(func=cmd_fpk)
    sub.add_parser("check", help="校验格式并与 manifest 对齐").set_defaults(func=cmd_check)

    p_channel = sub.add_parser("channel", help="查看或切换发布通道")
    p_channel.add_argument("--set", help="切换为 test 或 stable")
    p_channel.set_defaults(func=cmd_channel)

    p_bump = sub.add_parser("bump", help="推进修订号")
    p_bump.add_argument("--month", action="store_true", help="强制按当前月份重置")
    p_bump.set_defaults(func=cmd_bump)

    sub.add_parser("sync-manifest", help="把换算值写进 fpk manifest").set_defaults(
        func=cmd_sync_manifest
    )

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())

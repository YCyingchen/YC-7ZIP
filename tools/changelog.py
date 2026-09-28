#!/usr/bin/env python3
"""把 CHANGELOG.md 渲染成下载页能直接用的 HTML。

只实现 CHANGELOG 里实际用到的那点 markdown 子集（标题、列表、加粗、行内代码、
分隔线），不引第三方渲染器——引一个连安全转义都要自己盯的依赖不划算。

    python tools/changelog.py latest 3      # 最近 3 个版本的 HTML
    python tools/changelog.py render        # 全部
"""

from __future__ import annotations

import html
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHANGELOG = ROOT / "CHANGELOG.md"

VERSION_RE = re.compile(r"^##\s+(zip\d{4}\.\d{3})\s*[·・]\s*(.+?)\s*$")
SECTION_RE = re.compile(r"^###\s+(.+?)\s*$")


def join_wrapped(left: str, right: str) -> str:
    """把折行的两段接起来，只在需要时补空格。"""
    if not left:
        return right
    if not right:
        return left
    if left[-1].isascii() and left[-1].isalnum() and right[0].isascii() and right[0].isalnum():
        return left + " " + right
    return left + right


def inline(text: str) -> str:
    """转义后应用行内标记。顺序很重要：先转义再插标签。"""
    out = html.escape(text, quote=False)
    out = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", out)
    out = re.sub(r"`(.+?)`", r"<code>\1</code>", out)
    return out


def parse(markdown: str) -> list[dict]:
    """拆成 [{version, date, sections: [{title, items: [html]}]}]，新版本在前。"""
    releases: list[dict] = []
    current: dict | None = None
    section: dict | None = None

    for raw in markdown.splitlines():
        line = raw.rstrip()

        if m := VERSION_RE.match(line):
            current = {"version": m.group(1), "date": m.group(2), "sections": []}
            releases.append(current)
            section = None
            continue

        if current is None:
            continue

        if m := SECTION_RE.match(line):
            section = {"title": m.group(1), "items": []}
            current["sections"].append(section)
            continue

        if line.startswith("- "):
            text = line[2:].strip()
            if not text:
                continue
            # 续行（缩进两格）并到上一条
            if section is None:
                section = {"title": "", "items": []}
                current["sections"].append(section)
            section["items"].append(text)
            continue

        # 列表项的续行：属于上一条的补充说明。
        # 源码里为了可读性会折行，中文之间不该补空格，英文单词之间必须补，
        # 所以只在"两侧都是 ASCII 单词字符"时插一个空格。
        if line.startswith("  ") and section and section["items"]:
            section["items"][-1] = join_wrapped(section["items"][-1], line.strip())
            continue

    return releases


def render(releases: list[dict], limit: int | None = None) -> str:
    """每个版本渲染成一个 <details>，只有最新的那个默认展开。

    更新日志通常是来查"这次改了什么"的，全部展开会把页面拉得很长；
    但最新版恰恰是最常看的一条，默认收起反而多点一次。
    """
    out: list[str] = []
    chosen = releases[:limit] if limit else releases

    for index, rel in enumerate(chosen):
        open_attr = " open" if index == 0 else ""
        out.append(f'<details class="release"{open_attr}>')
        out.append(
            '  <summary class="release-head">'
            f'<span class="release-ver">{html.escape(rel["version"])}</span>'
            f'<span class="release-date">{html.escape(rel["date"])}</span>'
            '<span class="release-toggle" aria-hidden="true"></span>'
            "</summary>"
        )
        out.append('  <div class="release-body">')
        for sec in rel["sections"]:
            if sec["title"]:
                out.append(f'    <h4 class="release-section">{inline(sec["title"])}</h4>')
            out.append('    <ul class="release-items">')
            for item in sec["items"]:
                out.append(f"      <li>{inline(item)}</li>")
            out.append("    </ul>")
        out.append("  </div>")
        out.append("</details>")

    if limit and len(releases) > limit:
        rest = len(releases) - limit
        out.append(
            f'<p class="release-more">还有 {rest} 个更早的版本，'
            f'见仓库里的 <code>CHANGELOG.md</code>。</p>'
        )
    return "\n".join(out)


def main() -> int:
    if not CHANGELOG.is_file():
        print(f"找不到 {CHANGELOG}", file=sys.stderr)
        return 1
    releases = parse(CHANGELOG.read_text(encoding="utf-8"))

    command = sys.argv[1] if len(sys.argv) > 1 else "render"
    if command == "latest":
        limit = int(sys.argv[2]) if len(sys.argv) > 2 else 3
        print(render(releases, limit))
    elif command == "render":
        print(render(releases))
    elif command == "count":
        print(len(releases))
    elif command == "versions":
        for rel in releases:
            print(rel["version"])
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())

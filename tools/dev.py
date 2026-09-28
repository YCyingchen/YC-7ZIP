#!/usr/bin/env python3
"""YC-7ZIP 构建与部署工具。

刻意用 Python 而不是 PowerShell / bash：这一路上引号和转义踩了太多次坑
（here-string 吃掉 $ROOT、Set-Content 按 ANSI 回写把中文搅乱、
printf '%d' 把 09 当八进制）。Python 里这些都是显式的。

    python tools/dev.py check           # gofmt + vet + 版本号校验
    python tools/dev.py linux           # 交叉编译 linux/amd64 与 arm64
    python tools/dev.py dist            # 完整发布产物（二进制 + 7-Zip + tar.gz）
    python tools/dev.py nas-test        # 把二进制和测试推到 NAS 并在 NAS 上跑
    python tools/dev.py nas-app         # 在 NAS 上构建镜像、推仓库、打 fpk、重装
    python tools/dev.py nas-download    # 把下载页与产物放到 NAS 的下载目录
    python tools/dev.py release-assets  # 把 NAS 上打的 .fpk 补到 GitHub Release
    python tools/dev.py ui [URL]        # 真实浏览器验收（默认打 NAS 上的应用）
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "tools"))

import changelog as changelog_tool  # noqa: E402
import version as version_tool  # noqa: E402

DIST = ROOT / "dist"
ZIP_VERSION = "2501"

# 7-Zip 官方 Linux 包，按架构取不同文件
SEVENZIP_URL = {
    "amd64": f"https://www.7-zip.org/a/7z{ZIP_VERSION}-linux-x64.tar.xz",
    "arm64": f"https://www.7-zip.org/a/7z{ZIP_VERSION}-linux-arm64.tar.xz",
}

# 打进发布包的文本文件一律按 LF 存储
TEXT_SUFFIXES = {".sh", ".md", ".txt", ".service"}


def say(title: str) -> None:
    print(f"\n\033[1;36m== {title}\033[0m", flush=True)


def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    """跑一条命令，回显，失败即抛。"""
    print("  $ " + " ".join(cmd), flush=True)
    return subprocess.run(cmd, check=True, **kwargs)


def version() -> str:
    return version_tool.read_version()


# ------------------------------------------------------------------ 子命令


def cmd_check(_: argparse.Namespace) -> int:
    say("gofmt")
    out = subprocess.run(
        ["gofmt", "-l", "."], cwd=ROOT, capture_output=True, text=True, check=True
    ).stdout.split()
    if out:
        print("以下文件未格式化：\n  " + "\n  ".join(out))
        return 1

    say("go vet")
    run(["go", "vet", "./..."], cwd=ROOT)

    say("版本号")
    result = version_tool.cmd_check(argparse.Namespace())
    if result:
        return result
    print(f"  fpk manifest 用：{version_tool.fpk_version(version())}")
    return 0


def cmd_linux(_: argparse.Namespace) -> int:
    v = version()
    ch = version_tool.read_channel()
    say(f"交叉编译 linux/amd64 linux/arm64（版本 {v}，通道 {ch}）")
    for arch in ("amd64", "arm64"):
        out_dir = DIST / arch
        out_dir.mkdir(parents=True, exist_ok=True)
        out = out_dir / "yc7zip"
        run(
            [
                "go", "build", "-trimpath",
                # 通道也要注入：运行时靠它决定检查更新时认哪些发布，
                # 只注入版本会让二进制永远以为自己在 stable 通道上。
                "-ldflags", f"-s -w -X main.version={v} -X main.channel={ch}",
                "-o", str(out), ".",
            ],
            cwd=ROOT,
            env={**os.environ, "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"},
        )
        print(f"  {out.relative_to(ROOT)}  {out.stat().st_size} bytes")
    return 0


def fetch_sevenzip(arch: str) -> Path:
    """下载并解出官方 7-Zip 的 7zz，返回其路径。"""
    target_dir = DIST / f"7z-{arch}"
    target = target_dir / "7zz"
    if target.is_file():
        return target

    target_dir.mkdir(parents=True, exist_ok=True)
    url = SEVENZIP_URL[arch]
    archive = DIST / f"7z-{arch}.tar.xz"
    print(f"  下载 {url}")
    urllib.request.urlretrieve(url, archive)

    # tarfile 直接支持 xz，不必依赖外部解包工具
    with tarfile.open(archive, "r:xz") as tf:
        member = tf.getmember("7zz")
        member.mode = 0o755
        tf.extract(member, target_dir, filter="data")
    archive.unlink(missing_ok=True)

    if not target.is_file():
        raise RuntimeError(f"解包后没找到 7zz：{target}")
    return target


def make_tarball(stage_dir: Path, dest: Path) -> None:
    """打包一个目录。文本文件按 LF 写入，避免 shebang 在 Windows 上被改成 CRLF。"""
    dest.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(dest, "w:gz") as tf:
        for path in sorted(stage_dir.rglob("*")):
            rel = path.relative_to(stage_dir.parent)
            if path.is_dir():
                continue
            info = tf.gettarinfo(str(path), arcname=str(rel))
            if path.suffix in TEXT_SUFFIXES:
                content = path.read_bytes().replace(b"\r\n", b"\n")
                info.size = len(content)
                import io

                tf.addfile(info, io.BytesIO(content))
            else:
                with path.open("rb") as fh:
                    tf.addfile(info, fh)


def cmd_dist(_: argparse.Namespace) -> int:
    cmd_linux(argparse.Namespace())

    say("取官方 7-Zip")
    sevenzip = {}
    for arch in ("amd64", "arm64"):
        sevenzip[arch] = fetch_sevenzip(arch)
        print(f"  {sevenzip[arch].relative_to(ROOT)}  {sevenzip[arch].stat().st_size} bytes")

    v = version()
    say(f"打包发布件（版本 {v}）")
    pkg = DIST / "pkg"
    if pkg.exists():
        shutil.rmtree(pkg)
    pkg.mkdir(parents=True)

    for arch in ("amd64", "arm64"):
        stage = pkg / f"yc-7zip-{v}-linux-{arch}"
        stage.mkdir(parents=True)
        shutil.copy2(DIST / arch / "yc7zip", stage / "yc7zip")
        shutil.copy2(sevenzip[arch], stage / "7zz")
        shutil.copy2(ROOT / "README.md", stage / "README.md")
        for script in ("start.sh", "stop.sh"):
            src = ROOT / "deploy" / script
            text = src.read_text(encoding="utf-8").replace("\r\n", "\n")
            (stage / script).write_text(text, encoding="utf-8", newline="\n")
            (stage / script).chmod(0o755)
        make_tarball(stage, pkg / f"{stage.name}.tar.gz")

    # fpk 源码包只放打包骨架，不放二进制——它叫"源码"。但要写清二进制从哪儿来，
    # 否则解开只会看到 manifest 与几个空目录，无从下手。
    fpk_src = pkg / "fpk-src"
    shutil.copytree(ROOT / "deploy" / "fpk", fpk_src, ignore=shutil.ignore_patterns("bin"))
    (fpk_src / "README.md").write_text(
        f"""# YC-7ZIP 飞牛应用包（源码）

这里是 `fnpack` 的输入。**它不含二进制**，打包之前要先补上：

    app/bin/amd64/yc7zip    app/bin/amd64/7zz
    app/bin/arm64/yc7zip    app/bin/arm64/7zz

两种架构的 `yc7zip` 与官方 `7zz` 都在同一条 Release 的
`yc-7zip-{v}-linux-<arch>.tar.gz` 里（解开就是 `yc7zip` 与 `7zz` 两个文件）。
`cmd/main` 会按 `uname -m` 现选架构，所以一个 fpk 同时支持 x86_64 与 arm64；
只做本机架构的话放对应那一层就够，但 `manifest` 里的 `arch` 要跟着改。

然后打包：

    fnpack build -d .

这个应用**不依赖 Docker**：装好后由 `cmd/main` 直接拉起 `app/bin/<arch>/yc7zip`。
""",
        encoding="utf-8",
        newline="\n",
    )
    make_tarball(fpk_src, pkg / f"yc-7zip-{v}-fpk-src.tar.gz")

    for item in sorted(pkg.glob("*.tar.gz")):
        print(f"  {item.name}  {item.stat().st_size} bytes")
    return 0


# ------------------------------------------------------------- NAS 相关


def nas_module():
    import nas  # tools/nas.py

    return nas


def nas_client():
    nas = nas_module()
    return nas.connect()


def nas_run(client, command: str) -> str:
    _, stdout, stderr = client.exec_command(command, timeout=None)
    out = stdout.read().decode("utf-8", "replace")
    err = stderr.read().decode("utf-8", "replace")
    code = stdout.channel.recv_exit_status()
    if out.strip():
        print(out.rstrip())
    if err.strip():
        print(err.rstrip(), file=sys.stderr)
    if code != 0:
        raise RuntimeError(f"远端命令失败（退出码 {code}）：{command}")
    return out


def nas_run_stdin(client, command: str, payload: str) -> str:
    """执行命令，并把内容写进它的标准输入。

    Docker Hub 的密码走 `--password-stdin` 而不是命令行参数：命令行会出现在
    进程列表里，同机的其他账号 `ps` 一下就能看到。
    """
    stdin, stdout, stderr = client.exec_command(command, timeout=None)
    stdin.write(payload)
    stdin.flush()
    stdin.channel.shutdown_write()
    out = stdout.read().decode("utf-8", "replace")
    err = stderr.read().decode("utf-8", "replace")
    code = stdout.channel.recv_exit_status()
    if out.strip():
        print(out.rstrip())
    if err.strip():
        print(err.rstrip(), file=sys.stderr)
    if code != 0:
        raise RuntimeError(f"远端命令失败（退出码 {code}）：{command}")
    return out


def docker_hub_login(client) -> None:
    """在 NAS 上登录 Docker Hub，然后才开始推镜像。

    这一步不能省：NAS 上的登录态会过期，而过期时的报错是
    "denied: requested access to the resource is denied" —— 看起来像仓库权限
    问题，实际只是没登录，很容易把人引到错误的方向去。
    凭据取自环境或 .env.local，只在远端一次性使用，不写进任何脚本。
    """
    user = env_value("DOCKERHUB_USER") or "ycyingchen"
    secret = env_value("DOCKERHUB_TOKEN") or env_value("DOCKERHUB_PASSWORD")
    if not secret:
        raise RuntimeError(
            "缺少 Docker Hub 凭据：在 .env.local 里加一行 "
            "DOCKERHUB_TOKEN=<Access Token>（Docker Hub → Account settings → "
            "Personal access tokens，权限选 Read & Write）"
        )
    nas_run_stdin(client, f"docker login -u {shlex.quote(user)} --password-stdin", secret + "\n")


def nas_put(client, local: Path, remote: str) -> None:
    sftp = client.open_sftp()
    try:
        nas_module().run_checked(client, f"mkdir -p {nas_module().shlex.quote(nas_module().posix_dirname(remote))}")
        sftp.put(str(local), remote)
        print(f"  {local.name} -> {remote}")
    finally:
        sftp.close()


def cmd_nas_test(_: argparse.Namespace) -> int:
    say("交叉编译二进制与测试")
    cmd_linux(argparse.Namespace())
    tests = {}
    for pkg in ("engine", "server"):
        out = DIST / f"{pkg}.test"
        run(
            ["go", "test", "-c", f"./internal/{pkg}", "-o", str(out)],
            cwd=ROOT,
            env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"},
        )
        tests[pkg] = out

    say("推送到 NAS")
    client = nas_client()
    try:
        # 上一轮起的测试实例会占住同名二进制，此时 SFTP 只回一句没头没脑的
        # "Failure"（内核那边其实是 Text file busy）。先按端口找出来停掉，
        # 别指望运维能从那一句里猜出原因。
        nas_run(
            client,
            "pid=$(ss -ltnp 2>/dev/null | grep ':8092' | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1); "
            "if [ -n \"$pid\" ]; then echo \"停掉旧测试实例 pid=$pid\"; kill \"$pid\"; sleep 1; fi; "
            "echo 可以推送",
        )
        try:
            nas_put(client, DIST / "amd64" / "yc7zip", "/root/yc7zip-test/yc7zip")
        except OSError as exc:
            raise RuntimeError(
                "写不进 /root/yc7zip-test/yc7zip —— 多半是它还在跑，占着这个文件。"
                f"先停掉 :8092 上的测试实例再试。底层报错：{exc}"
            ) from exc
        for pkg, path in tests.items():
            nas_put(client, path, f"/root/yc7zip-test/{pkg}.test")

        say("在 NAS 上跑测试")
        v = version()
        ch = version_tool.read_channel()
        # 先核对二进制里确实带着注入的版本与通道：这类缺失运行时不会报错，
        # 只会在"检查更新永远看不到预发布"这种地方悄悄表现出来。
        info = nas_run(client, "cd /root/yc7zip-test && ./yc7zip -build-info").strip()
        if f"version={v}" not in info or f"channel={ch}" not in info:
            print(f"  ❌ 构建信息与预期不符：得到 {info!r}，期望 version={v} channel={ch}", file=sys.stderr)
            return 1
        print(f"  ✅ 构建信息核对通过：{info}")

        nas_run(
            client,
            "cd /root/yc7zip-test && chmod +x yc7zip engine.test server.test "
            "&& echo '--- engine ---' && ./engine.test 2>&1 | tail -3 "
            "&& echo '--- server ---' && (cd internal/server && ../../server.test 2>&1 | tail -3)",
        )
    finally:
        client.close()
    return 0


def cmd_nas_app(_: argparse.Namespace) -> int:
    """在 NAS 上构建镜像、推 Docker Hub、打 fpk 并重装。"""
    v = version()
    say(f"准备 NAS 构建上下文（版本 {v}）")
    cmd_linux(argparse.Namespace())

    client = nas_client()
    try:
        nas_run(client, "mkdir -p /root/yc7zip-build/dist/amd64")
        nas_put(client, DIST / "amd64" / "yc7zip", "/root/yc7zip-build/dist/amd64/yc7zip")
        nas_put(client, ROOT / "Dockerfile", "/root/yc7zip-build/Dockerfile")
        nas_put(client, ROOT / "VERSION", "/root/yc7zip-build/VERSION")

        # fpk 源目录整体同步
        nas = nas_module()
        nas.run_checked(
            client,
            f"rm -rf /root/yc7zip-build/fpk && mkdir -p /root/yc7zip-build/fpk",
        )
        sftp = client.open_sftp()
        try:
            base = ROOT / "deploy" / "fpk"
            for path in sorted(base.rglob("*")):
                rel = path.relative_to(base).as_posix()
                remote = f"/root/yc7zip-build/fpk/{rel}"
                if path.is_dir():
                    nas.run_checked(client, f"mkdir -p {nas.shlex.quote(remote)}")
                else:
                    nas.run_checked(client, f"mkdir -p {nas.shlex.quote(nas.posix_dirname(remote))}")
                    if path.suffix in ("", ".sh", ".yaml", ".yml") or path.name in (
                        "manifest",
                        "privilege",
                        "resource",
                        "config",
                        "main",
                    ):
                        text = path.read_text(encoding="utf-8").replace("\r\n", "\n")
                        import io as _io

                        sftp.putfo(_io.BytesIO(text.encode("utf-8")), remote)
                    else:
                        sftp.put(str(path), remote)
        finally:
            sftp.close()
        print("  deploy/fpk -> /root/yc7zip-build/fpk")

        # 原生 fpk 得自己带二进制：不再从仓库拉镜像，包里没有东西就没得跑。
        # 两种架构都放进去，由 cmd/main 按 uname -m 现选，用户不必分辨该下哪个包。
        say("组装 fpk 内的二进制与官方 7-Zip")
        nas_run(
            client,
            "mkdir -p /root/yc7zip-build/fpk/app/bin/amd64 /root/yc7zip-build/fpk/app/bin/arm64",
        )
        for arch in ("amd64", "arm64"):
            nas_put(client, DIST / arch / "yc7zip", f"/root/yc7zip-build/fpk/app/bin/{arch}/yc7zip")
            nas_put(client, fetch_sevenzip(arch), f"/root/yc7zip-build/fpk/app/bin/{arch}/7zz")
        nas_run(
            client,
            "chmod +x /root/yc7zip-build/fpk/app/bin/*/* && "
            "ls -la /root/yc7zip-build/fpk/app/bin/amd64 /root/yc7zip-build/fpk/app/bin/arm64",
        )

        # Docker 镜像照旧构建推送：compose 部署那条路走的就是这个镜像。
        nas_put(client, fetch_sevenzip("amd64"), "/root/yc7zip-build/dist/amd64/7zz")

        say("在 NAS 上构建镜像")
        # 版本标签永远打且不可变；通道标签由 CHANNEL 决定，
        # 写死 latest 会让测试版覆盖正式版的分发标签。
        channel_tag = "latest" if version_tool.read_channel() == "stable" else "test"
        nas_run(
            client,
            "cd /root/yc7zip-build && chmod +x dist/amd64/yc7zip dist/amd64/7zz "
            f"&& DOCKER_BUILDKIT=0 docker build --build-arg TARGETARCH=amd64 "
            f"-t ycyingchen/yc-7zip:{v} -t ycyingchen/yc-7zip:{channel_tag} .",
        )

        say(f"推送到 Docker Hub（给 compose 部署的用户）[{v} 与 {channel_tag}]")
        docker_hub_login(client)
        nas_run(
            client,
            f"docker push ycyingchen/yc-7zip:{v} && docker push ycyingchen/yc-7zip:{channel_tag}",
        )

        say("打包 fpk")
        nas_run(client, "cd /root/yc7zip-build/fpk && rm -f yc7zip.fpk && fnpack build")

        say("重装应用")
        nas_run(
            client,
            "appcenter-cli uninstall yc7zip 2>&1 | tr '\\r' '\\n' | grep -E 'success|Error' || true",
        )
        nas_run(
            client,
            "printf 'wizard_app_port=8090\\n' > /root/yc7zip-build/env.txt && "
            "appcenter-cli install-fpk /root/yc7zip-build/fpk/yc7zip.fpk "
            "-e /root/yc7zip-build/env.txt -v 1 2>&1 | tr '\\r' '\\n' | grep -E 'Error|complete' || true",
        )

        say("等待健康检查")
        import time

        # 原生应用没有容器可以 inspect：直接问它自己的健康接口。
        # 打 127.0.0.1:8090 —— 这正是应用网关看到的那个地址。
        healthy = False
        for _ in range(12):
            time.sleep(5)
            code = nas_run(
                client,
                "curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8090/api/health || true",
            ).strip()
            if code == "200":
                healthy = True
                break
        if healthy:
            print("  ✅ /api/health 200")
        else:
            print("  ⚠ 健康检查没通过", file=sys.stderr)
        nas_run(client, "appcenter-cli list 2>&1 | tr '\\r' '\\n' | grep -i yc7zip || true")
        # 日志用 /var/apps/<app>/var 这个软链接取，别写死 /volN —— 应用装在哪个卷上是由用户选的
        nas_run(client, "tail -8 /var/apps/yc7zip/var/info.log 2>/dev/null || true")
    finally:
        client.close()
    return 0


def plain_text_release(rel: dict) -> str:
    """把一条更新日志压成界面能直接显示的纯文本。

    更新对话框里这段是按纯文本渲染的（textContent），留着 ** 和反引号
    会原样露出来，反而更难读。
    """
    lines = [f"{rel['version']} · {rel['date']}".strip(" ·"), ""]
    for sec in rel["sections"]:
        if sec["title"]:
            lines.append(sec["title"])
        for item in sec["items"]:
            text = re.sub(r"\*\*(.+?)\*\*", r"\1", item)
            text = re.sub(r"`(.+?)`", r"\1", text)
            lines.append(f"· {text}")
        lines.append("")
    return "\n".join(lines).strip()


def write_checksums(dl: Path, names: list[str]) -> dict[str, str]:
    """给下载目录里的产物写 SHA256SUMS.txt，返回 name -> 摘要。

    格式与 sha256sum 的输出一致（两列、路径带 ./），因为 GitHub Release 上
    那份就是 sha256sum 打的，两处要能被同一段客户端代码读。
    """
    sums: dict[str, str] = {}
    for name in names:
        path = dl / name
        if path.is_file():
            sums[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    body = "".join(f"{digest}  ./{name}\n" for name, digest in sums.items())
    (dl / "SHA256SUMS.txt").write_text(body, encoding="utf-8", newline="\n")
    return sums


def write_update_manifest(dl: Path, v: str, channel: str, rel: dict, sums: dict[str, str]) -> None:
    """写 update.json —— 自建更新源的清单。

    应用的在线检查直接读它：一次请求就把版本、说明、本架构的包和摘要都拿到。
    没有它就只好靠 VERSION + CHANGELOG + 猜文件名去拼，既拿不到摘要，
    也经不起文件名规则变动。
    """
    assets = [
        {
            "name": name,
            "size": (dl / name).stat().st_size,
            "sha256": sums.get(name, ""),
        }
        for name in ("yc-7zip-linux-amd64.tar.gz", "yc-7zip-linux-arm64.tar.gz", "yc7zip.fpk")
        if (dl / name).is_file()
    ]
    manifest = {
        "version": v,
        "channel": channel,
        "date": rel.get("date", ""),
        "notes": plain_text_release(rel),
        "assets": assets,
    }
    (dl / "update.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n"
    )


def cmd_nas_download(_: argparse.Namespace) -> int:
    """把下载页与产物放到 NAS 的下载目录。"""
    target = "/vol5/1000/空间4/YC-7ZIP"
    v = version()
    channel = version_tool.read_channel()
    say(f"准备下载页产物（版本 {v}，通道 {channel}）")

    dl = DIST / "dl"
    if dl.exists():
        shutil.rmtree(dl)
    dl.mkdir(parents=True)

    # 下载页是静态页，构建时把 CHANGELOG.md 渲染进去、并盖上版本与通道章，
    # 这样页面不依赖任何运行时请求，扔在 NAS 上就能直接看。
    page = (ROOT / "deploy" / "download-page" / "index.html").read_text(encoding="utf-8")
    releases = changelog_tool.parse((ROOT / "CHANGELOG.md").read_text(encoding="utf-8"))
    rendered = changelog_tool.render(releases, limit=3)
    page = re.sub(
        r"<!-- CHANGELOG:BEGIN -->.*?<!-- CHANGELOG:END -->",
        "<!-- CHANGELOG:BEGIN -->\n" + rendered + "\n    <!-- CHANGELOG:END -->",
        page,
        flags=re.S,
    )
    label = version_tool.channel_label(channel)
    page = page.replace("<!-- CHANNEL_CLASS -->", f'data-channel="{channel}"')
    page = page.replace("<!-- CHANNEL_BADGE -->", label)
    page = page.replace("<!-- VERSION -->", v)
    page = page.replace("<!-- FPK_VERSION -->", version_tool.fpk_version(v))
    (dl / "index.html").write_text(page, encoding="utf-8", newline="\n")

    shutil.copy2(ROOT / "assets" / "images" / "icon.png", dl / "icon.png")
    shutil.copy2(ROOT / "VERSION", dl / "VERSION")
    shutil.copy2(ROOT / "CHANNEL", dl / "CHANNEL")
    shutil.copy2(ROOT / "CHANGELOG.md", dl / "CHANGELOG.md")
    # 下载页上直接给 compose 文件，不必再进仓库找
    shutil.copy2(ROOT / "docker-compose.yml", dl / "docker-compose.yml")
    shutil.copy2(ROOT / ".env.example", dl / "env.example")

    pkg = DIST / "pkg"
    if not (pkg / f"yc-7zip-{v}-linux-amd64.tar.gz").is_file():
        cmd_dist(argparse.Namespace())
    # 页面里用的是不带版本号的稳定文件名，链接不会随版本失效
    shutil.copy2(pkg / f"yc-7zip-{v}-linux-amd64.tar.gz", dl / "yc-7zip-linux-amd64.tar.gz")
    shutil.copy2(pkg / f"yc-7zip-{v}-linux-arm64.tar.gz", dl / "yc-7zip-linux-arm64.tar.gz")
    shutil.copy2(pkg / f"yc-7zip-{v}-fpk-src.tar.gz", dl / "yc-7zip-fpk-src.tar.gz")

    client = nas_client()
    try:
        say("取回 NAS 上构建好的 fpk")
        sftp = client.open_sftp()
        try:
            sftp.get("/root/yc7zip-build/fpk/yc7zip.fpk", str(dl / "yc7zip.fpk"))
            print("  yc7zip.fpk <- /root/yc7zip-build/fpk/yc7zip.fpk")
        except OSError as exc:
            print(f"  跳过 fpk（还没在 NAS 上构建）：{exc}", file=sys.stderr)
        finally:
            sftp.close()

        # 清单要等产物齐了再写：fpk 是刚从 NAS 上取回来的，
        # 早一步生成就会漏掉它的摘要。
        say("生成更新源清单")
        rel = next((r for r in releases if r["version"] == v), None)
        if rel is None:
            print(f"  ⚠ CHANGELOG 里没有 {v} 的条目，清单会缺日期与更新说明", file=sys.stderr)
            rel = {"version": v, "date": "", "sections": []}
        sums = write_checksums(
            dl,
            [
                "yc-7zip-linux-amd64.tar.gz",
                "yc-7zip-linux-arm64.tar.gz",
                "yc-7zip-fpk-src.tar.gz",
                "yc7zip.fpk",
                "docker-compose.yml",
                "env.example",
            ],
        )
        write_update_manifest(dl, v, channel, rel, sums)
        print(f"  update.json / SHA256SUMS.txt（{len(sums)} 个产物）")

        say(f"上传到 {target}")
        nas_run(client, f"mkdir -p {target!r}".replace("'", '"'))
        sftp = client.open_sftp()
        try:
            for path in sorted(dl.iterdir()):
                sftp.put(str(path), f"{target}/{path.name}")
                print(f"  {path.name}  {path.stat().st_size} bytes")
        finally:
            sftp.close()
        nas_run(client, f"ls -la \"/vol5/1000/空间4/YC-7ZIP\"")
    finally:
        client.close()
    return 0


# ------------------------------------------------------------ GitHub Release


def github_request(
    path: str,
    token: str,
    method: str = "GET",
    data: bytes | None = None,
    ctype: str | None = None,
    accept: str = "application/vnd.github+json",
):
    """调一次 GitHub API，返回解析后的 JSON（无正文时为 None）。"""
    req = urllib.request.Request("https://api.github.com" + path, data=data, method=method)
    req.add_header("Authorization", f"Bearer {token}")
    req.add_header("Accept", accept)
    req.add_header("User-Agent", "yc7zip-release")
    if ctype:
        req.add_header("Content-Type", ctype)

    try:
        with github_opener().open(req, timeout=180) as res:
            body = res.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:300]
        raise RuntimeError(f"GitHub API {method} {path} 返回 {exc.code}：{detail}") from exc
    return json.loads(body) if body else None


def github_opener():
    """带上代理的 opener。本机直连 api.github.com 时通时不通，
    而这条链路一旦静默失败，表现就是"附件没传上去却没人知道"。"""
    proxy = env_value("GITHUB_PROXY") or env_value("NAS_PROXY")
    if not proxy:
        return urllib.request.build_opener()
    return urllib.request.build_opener(urllib.request.ProxyHandler({"http": proxy, "https": proxy}))


def find_release(repo: str, tag: str, token: str):
    """按标签取 Release；还没有就返回 None。"""
    try:
        return github_request(f"/repos/{repo}/releases/tags/{tag}", token)
    except RuntimeError as exc:
        if "返回 404" in str(exc):
            return None
        raise


def wait_for_release_assets(repo: str, tag: str, token: str, timeout_s: int = 900):
    """等 CI 把 Release 建出来并且附件传完，返回那个 Release。

    判据取"里面已经有 linux-amd64 的包"，而不是"Release 已存在"：打标签后
    建 Release 与上传附件之间有几秒空档，踩进去会和 action-gh-release 抢同一个
    release，表现为附件时有时无。
    """
    deadline = time.monotonic() + timeout_s
    while True:
        release = find_release(repo, tag, token)
        if release:
            names = [a["name"] for a in release.get("assets", [])]
            if any("linux-amd64.tar.gz" in n for n in names):
                return release
        if time.monotonic() > deadline:
            return None
        print("  等 CI 产出 Release 附件…", flush=True)
        time.sleep(20)


def upload_asset(repo: str, release: dict, path: Path, token: str, name: str | None = None) -> None:
    """把一个文件传成 Release 附件；同名先删，否则重复发布会 422。"""
    name = name or path.name
    for asset in release.get("assets", []):
        if asset["name"] == name:
            github_request(f"/repos/{repo}/releases/assets/{asset['id']}", token, method="DELETE")
            print(f"  已删除同名旧附件 {name}")

    query = urllib.parse.urlencode({"name": name})
    url = f"https://uploads.github.com/repos/{repo}/releases/{release['id']}/assets?{query}"
    req = urllib.request.Request(url, data=path.read_bytes(), method="POST")
    req.add_header("Authorization", f"Bearer {token}")
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("Content-Type", "application/octet-stream")
    req.add_header("User-Agent", "yc7zip-release")

    try:
        with github_opener().open(req, timeout=900) as res:
            json.loads(res.read() or b"{}")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:300]
        raise RuntimeError(f"上传 {name} 失败：{exc.code} {detail}") from exc
    print(f"  {name}  {path.stat().st_size} bytes")


def cmd_release_assets(_: argparse.Namespace) -> int:
    """把只有本地/NAS 才产得出的附件补到 GitHub Release 上。

    CI 能打出两个架构的二进制包、compose 包和 fpk **源码**包，但打不出编译好的
    .fpk——那要 fnpack，飞牛只在自己的环境里提供。于是 Releases 页面上架的是一份
    "还得自己装 fnpack 才能变成可安装的包"的源码，而下载页那边却有现成的 .fpk。
    补这一步，两边才是同一份东西。
    """
    token = env_value("GITHUB_TOKEN")
    repo = env_value("GITHUB_REPO")
    if not token or not repo:
        print("缺少 GITHUB_TOKEN / GITHUB_REPO（写在 .env.local）", file=sys.stderr)
        return 1

    v = version()
    fpk = DIST / "dl" / "yc7zip.fpk"
    if not fpk.is_file():
        print(f"找不到 {fpk}：先跑 nas-app（在 NAS 上打 fpk）或 nas-download", file=sys.stderr)
        return 1

    say(f"补充 {v} 的 Release 附件")
    release = wait_for_release_assets(repo, v, token)
    if release is None:
        print(f"等不到 {v} 的 Release 附件（CI 可能还在跑，或者失败了）", file=sys.stderr)
        return 1
    # 附件名跟同一条 Release 里的其他产物保持一致（带版本号），
    # 下载页那边仍用不带版本号的稳定名。
    upload_asset(repo, release, fpk, token, name=f"yc-7zip-{v}.fpk")
    print(f"  {release['html_url']}")
    return 0


def cmd_ui(args: argparse.Namespace) -> int:
    base = args.url or "http://192.168.1.9:8090/app/yc7zip"
    say(f"浏览器验收：{base}")

    node = os.environ.get("MIMO_NODE")
    if not node:
        print("需要 MIMO_NODE 环境变量指向 node.exe", file=sys.stderr)
        return 2
    env = {
        **os.environ,
        "PW_MODULE": os.environ.get("PW_MODULE", ""),
        "UI_OUT": os.environ.get("UI_OUT", str(Path(os.environ.get("TEMP", "/tmp")) / "yc7zip-ui")),
    }
    if not env["PW_MODULE"]:
        print("需要 PW_MODULE 环境变量指向 playwright 模块目录", file=sys.stderr)
        return 2
    return subprocess.run(
        [node, str(ROOT / "tools" / "ui-test.cjs"), base], env=env, cwd=ROOT
    ).returncode


# --------------------------------------------------------------- 发布


def git(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", *args], cwd=ROOT, check=check, capture_output=True, text=True
    )


def env_value(key: str) -> str:
    """从环境或 .env.local 取值。"""
    if os.environ.get(key):
        return os.environ[key].strip()
    env_file = ROOT / ".env.local"
    if env_file.is_file():
        for raw in env_file.read_text(encoding="utf-8").splitlines():
            line = raw.strip()
            if line.startswith(f"{key}="):
                return line.split("=", 1)[1].strip()
    return ""


def git_remote_url() -> str:
    token = env_value("GITHUB_TOKEN")
    return f"https://x-access-token:{token}@github.com/YCyingchen/YC-7ZIP.git"


def cmd_publish(args: argparse.Namespace) -> int:
    """走完整发布链路：提交 -> 推 GitHub -> 推镜像 -> 更新 NAS -> 刷新下载页。

    这是「每次完成后都要发一次」这条要求的固化：一条命令跑完，
    不用记有哪几步、也不用担心漏掉某一处。
    """
    v = version()
    channel = version_tool.read_channel()
    label = version_tool.channel_label(channel)
    say(f"发布 {v}（{label}）")

    # 1. 本地检查
    if cmd_check(argparse.Namespace()):
        print("本地检查未通过，已中止", file=sys.stderr)
        return 1

    # 2. 提交
    status = git("status", "--porcelain").stdout.strip()
    if status:
        message = args.message or f"{v}: 常规更新"
        git("-c", "user.name=YCyingchen",
            "-c", "user.email=ycyingchen@users.noreply.github.com",
            "commit", "-q", "-am", message, check=False)
        # -am 不会带上未跟踪的文件，补一次 add
        git("add", "-A")
        git("-c", "user.name=YCyingchen",
            "-c", "user.email=ycyingchen@users.noreply.github.com",
            "commit", "-q", "-m", message, check=False)
        print(f"  已提交：{message}")
    else:
        print("  工作区干净，无需提交")

    # 3. 推 GitHub
    remote = git_remote_url()
    proxy = env_value("NAS_PROXY") or "http://192.168.1.8:7890"
    git_cfg = ["-c", f"http.proxy={proxy}", "-c", f"https.proxy={proxy}"]
    head = git("rev-parse", "HEAD").stdout.strip()
    print(f"  推送 main（经 {proxy}）")
    git(*git_cfg, "push", remote, "HEAD:main", check=False)

    remote_sha = git(*git_cfg, "ls-remote", remote, "refs/heads/main").stdout.split()
    remote_sha = remote_sha[0] if remote_sha else ""
    if remote_sha != head:
        print("  推送未成功（多为网络问题），重试一次", file=sys.stderr)
        git(*git_cfg, "push", remote, "HEAD:main", check=False)
        remote_sha = git(*git_cfg, "ls-remote", remote, "refs/heads/main").stdout.split()
        remote_sha = remote_sha[0] if remote_sha else ""
    print(f"  远端 main = {remote_sha[:7]}{'  ✅' if remote_sha == head else '  ❌'}")

    # 4. 打标签触发 Release（测试通道会发成预发布）
    if remote_sha == head:
        tags = git("tag", "--list", v).stdout.strip()
        if not tags:
            git("tag", v)
        git(*git_cfg, "push", remote, f"refs/tags/{v}", check=False)
        print(f"  标签 {v} 已推送（通道 {channel}，Release 为{'预发布' if channel == 'test' else '正式'}）")

    # 5. NAS 侧：构建镜像、推 Docker Hub、打 fpk、重装
    say("NAS：构建镜像并推 Docker Hub")
    if cmd_nas_app(argparse.Namespace()):
        print("NAS 部署失败", file=sys.stderr)
        return 1

    # 6. 下载页
    say("刷新下载页")
    if cmd_nas_download(argparse.Namespace()):
        print("下载页更新失败", file=sys.stderr)
        return 1

    # 7. Release 附件：.fpk 只能在 NAS 上打，CI 那份是源码包
    say("补充 Release 附件")
    if cmd_release_assets(argparse.Namespace()):
        # 前面几步都已经生效了，不该因为这一步让整条发布链报失败；
        # 但补救命令要写清楚，否则 Release 会一直缺这一份。
        print("  ⚠ 附件没补上；可单独重试：python tools/dev.py release-assets", file=sys.stderr)

    say(f"完成：{v}（{label}）")
    print(f"  仓库   https://github.com/YCyingchen/YC-7ZIP")
    print(f"  发布   https://github.com/YCyingchen/YC-7ZIP/releases")
    print(f"  镜像   https://hub.docker.com/r/ycyingchen/yc-7zip")
    print(f"  下载页 http://192.168.1.9:5666/app/yc7zip/ 或 NAS 上 /vol5/1000/空间4/YC-7ZIP")
    return 0


COMMANDS = {
    "check": cmd_check,
    "linux": cmd_linux,
    "dist": cmd_dist,
    "nas-test": cmd_nas_test,
    "nas-app": cmd_nas_app,
    "nas-download": cmd_nas_download,
    "release-assets": cmd_release_assets,
    "ui": cmd_ui,
    "publish": cmd_publish,
}


def main() -> int:
    parser = argparse.ArgumentParser(description="YC-7ZIP 构建与部署工具")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in COMMANDS:
        p = sub.add_parser(name)
        if name == "ui":
            p.add_argument("url", nargs="?", help="目标地址")
        if name == "publish":
            p.add_argument("-m", "--message", help="提交信息")
    args = parser.parse_args()
    try:
        return COMMANDS[args.command](args) or 0
    except subprocess.CalledProcessError as exc:
        print(f"\n命令失败：{exc}", file=sys.stderr)
        return exc.returncode or 1
    except RuntimeError as exc:
        print(f"\n{exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())

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
    python tools/dev.py ui [URL]        # 真实浏览器验收（默认打 NAS 上的应用）
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import tarfile
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "tools"))

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
    say(f"交叉编译 linux/amd64 linux/arm64（版本 {v}）")
    for arch in ("amd64", "arm64"):
        out_dir = DIST / arch
        out_dir.mkdir(parents=True, exist_ok=True)
        out = out_dir / "yc7zip"
        run(
            [
                "go", "build", "-trimpath",
                "-ldflags", f"-s -w -X main.version={v}",
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

    fpk_src = pkg / "fpk-src"
    shutil.copytree(ROOT / "deploy" / "fpk", fpk_src)
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
        nas_put(client, DIST / "amd64" / "yc7zip", "/root/yc7zip-test/yc7zip")
        for pkg, path in tests.items():
            nas_put(client, path, f"/root/yc7zip-test/{pkg}.test")

        say("在 NAS 上跑测试")
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

        # 7-Zip 的 Linux 版在 NAS 上解出来（那边有 tar/xz）
        nas_put(client, ROOT / "dist" / "7z-amd64" / "7zz" if (ROOT / "dist" / "7z-amd64" / "7zz").is_file() else fetch_sevenzip("amd64"), "/root/yc7zip-build/dist/amd64/7zz")

        say("在 NAS 上构建镜像")
        nas_run(
            client,
            "cd /root/yc7zip-build && chmod +x dist/amd64/yc7zip dist/amd64/7zz "
            f"&& DOCKER_BUILDKIT=0 docker build --build-arg TARGETARCH=amd64 "
            f"-t ycyingchen/yc-7zip:{v} -t ycyingchen/yc-7zip:latest .",
        )

        say("推送到 Docker Hub（应用中心安装时会拉，必须先有）")
        nas_run(client, f"docker push ycyingchen/yc-7zip:{v} && docker push ycyingchen/yc-7zip:latest")

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

        for _ in range(12):
            time.sleep(5)
            status = nas_run(client, "docker inspect yc7zip --format '{{.State.Health.Status}}' 2>/dev/null || echo none")
            if "healthy" in status:
                break
        nas_run(client, "docker ps --filter name=yc7zip --format '{{.Names}} | {{.Status}}'")
    finally:
        client.close()
    return 0


def cmd_nas_download(_: argparse.Namespace) -> int:
    """把下载页与产物放到 NAS 的下载目录。"""
    target = "/vol5/1000/空间4/YC-7ZIP"
    v = version()
    say(f"准备下载页产物（版本 {v}）")

    dl = DIST / "dl"
    if dl.exists():
        shutil.rmtree(dl)
    dl.mkdir(parents=True)
    shutil.copy2(ROOT / "deploy" / "download-page" / "index.html", dl / "index.html")
    shutil.copy2(ROOT / "assets" / "images" / "icon.png", dl / "icon.png")
    shutil.copy2(ROOT / "VERSION", dl / "VERSION")

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


COMMANDS = {
    "check": cmd_check,
    "linux": cmd_linux,
    "dist": cmd_dist,
    "nas-test": cmd_nas_test,
    "nas-app": cmd_nas_app,
    "nas-download": cmd_nas_download,
    "ui": cmd_ui,
}


def main() -> int:
    parser = argparse.ArgumentParser(description="YC-7ZIP 构建与部署工具")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in COMMANDS:
        p = sub.add_parser(name)
        if name == "ui":
            p.add_argument("url", nargs="?", help="目标地址")
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

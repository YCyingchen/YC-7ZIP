#!/usr/bin/env python3
"""YC-7ZIP 端到端验收 —— 对着已经跑起来的服务跑一遍真实流程。

覆盖：健康检查 → 压缩（中文名 / 子目录）→ 分卷 → 加密 → 解压预览 →
选择性解压 → 分卷解压 → 密码校验 → 服务端直读直写 → 安全边界。

    python tools/e2e.py http://192.168.1.9:8090

用 Python 而不是 PowerShell：multipart 上传、JSON、非 ASCII 文件名在
PowerShell 里都要靠 curl 拼接并且经常被引号规则搞坏，这里用标准库直接写。
"""

from __future__ import annotations

import io
import json
import mimetypes
import os
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://192.168.1.9:8090").rstrip("/")

passed = 0
failed: list[str] = []


def check(name: str, ok: bool, detail: str = "") -> None:
    global passed
    if ok:
        passed += 1
        print(f"  \033[32m[PASS]\033[0m {name}")
    else:
        failed.append(f"{name}{' -> ' + str(detail) if detail else ''}")
        print(f"  \033[31m[FAIL]\033[0m {name}{' -> ' + str(detail) if detail else ''}")


def section(title: str) -> None:
    print(f"\n\033[1;36m== {title}\033[0m")


def request(method: str, path: str, body: bytes | None = None, ctype: str | None = None):
    """返回 (状态码, 解析后的 JSON 或原始字节)。"""
    status, raw = request_raw(method, path, body, ctype)
    return status, _maybe_json(raw)


def request_raw(
    method: str, path: str, body: bytes | None = None, ctype: str | None = None
) -> tuple[int, bytes]:
    """返回 (状态码, 原始字节)。下载产物必须走这个：二进制经 JSON 解析会被破坏。"""
    url = BASE + path
    req = urllib.request.Request(url, data=body, method=method)
    if ctype:
        req.add_header("Content-Type", ctype)
    try:
        with urllib.request.urlopen(req, timeout=120) as res:
            return res.status, res.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def _maybe_json(raw: bytes):
    text = raw.decode("utf-8", "replace")
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return text


def api(method: str, path: str, payload=None):
    body = json.dumps(payload).encode("utf-8") if payload is not None else None
    return request(method, path, body, "application/json" if body else None)


def upload(job_id: str, files: list[tuple[str, bytes, str]]):
    """files: (相对路径, 内容, 文件名)"""
    boundary = "----yc7zip" + uuid.uuid4().hex
    buf = io.BytesIO()
    for rel, content, name in files:
        buf.write(f"--{boundary}\r\n".encode())
        buf.write(f'Content-Disposition: form-data; name="relpath"\r\n\r\n{rel}\r\n'.encode("utf-8"))
        buf.write(f"--{boundary}\r\n".encode())
        ct = mimetypes.guess_type(name)[0] or "application/octet-stream"
        buf.write(
            f'Content-Disposition: form-data; name="files"; filename="{name}"\r\n'
            f"Content-Type: {ct}\r\n\r\n".encode("utf-8")
        )
        buf.write(content)
        buf.write(b"\r\n")
    buf.write(f"--{boundary}--\r\n".encode())
    return request(
        "POST", f"/api/jobs/{job_id}/upload", buf.getvalue(),
        f"multipart/form-data; boundary={boundary}",
    )


def new_job(kind: str) -> str:
    status, body = api("POST", f"/api/jobs?kind={kind}")
    if status != 201:
        raise RuntimeError(f"创建任务失败：{status} {body}")
    return body["id"]


def wait_job(job_id: str, timeout: float = 180) -> dict:
    deadline = time.time() + timeout
    while time.time() < deadline:
        _, job = api("GET", f"/api/jobs/{job_id}")
        if job.get("status") in ("done", "error", "cancelled"):
            return job
        time.sleep(0.3)
    raise RuntimeError(f"任务 {job_id} 在 {timeout}s 内没结束")


def download(path: str, dest: Path) -> int:
    status, raw = request_raw("GET", path)
    if status != 200:
        return 0
    dest.write_bytes(raw)
    return len(raw)


def main() -> int:
    work = Path(tempfile.mkdtemp(prefix="yc7zip-e2e-"))
    src = work / "src"
    (src / "相册").mkdir(parents=True)

    # 中文文件名与不被压缩的随机数据，都是容易出问题的地方
    (src / "说明.txt").write_text("hello yc-7zip from NAS", encoding="utf-8")
    (src / "相册" / "照片说明.txt").write_text("中文文件名测试", encoding="utf-8")
    blob = os.urandom(2 * 1024 * 1024)
    (src / "blob.bin").write_bytes(blob)

    print(f"目标服务端：{BASE}")

    section("1. 健康检查与格式表")
    status, health = api("GET", "/api/health")
    check("服务可用", status == 200 and health.get("ok") is True, status)
    engine = health.get("engine", {})
    check("7-Zip 引擎已就绪", bool(engine.get("path")), engine)
    print(f"     引擎：{engine.get('path')} v{engine.get('version')}")
    create = engine.get("create") or []
    check("可创建 7z/zip/tar", all(f in create for f in ("7z", "zip", "tar")), create)

    _, formats = api("GET", "/api/formats")
    ids = {f["id"]: f for f in formats["formats"]}
    check("格式表含 rar", "rar" in ids)
    check("rar 标记为仅解压", ids["rar"]["capability"] == 0)

    section("2. 压缩 7z（中文名 + 子目录）")
    job = new_job("compress")
    status, body = upload(job, [
        ("docs/说明.txt", (src / "说明.txt").read_bytes(), "说明.txt"),
        ("docs/相册/照片说明.txt", (src / "相册" / "照片说明.txt").read_bytes(), "照片说明.txt"),
        ("docs/blob.bin", blob, "blob.bin"),
    ])
    check("上传成功", status == 200, f"{status} {body}")

    status, _ = api("POST", f"/api/jobs/{job}/run",
                    {"format": "7z", "level": 5, "name": "bundle"})
    check("启动压缩被接受", status == 202, status)
    done = wait_job(job)
    check("压缩完成", done["status"] == "done", done.get("message"))
    check("进度到达 100%", done.get("progress") == 100, done.get("progress"))

    archive = work / "bundle.7z"
    size = download(f"/api/jobs/{job}/download-all", archive)
    check("下载到产物", size > 0, size)
    if size:
        head = archive.read_bytes()[:4]
        check("产物是合法 7z", head == b"7z\xbc\xaf", head.hex())

    section("3. 压缩 ZIP + 分卷")
    job2 = new_job("compress")
    upload(job2, [("blob.bin", blob, "blob.bin")])
    api("POST", f"/api/jobs/{job2}/run", {"format": "zip", "level": 1, "name": "zipped"})
    d2 = wait_job(job2)
    check("ZIP 压缩完成", d2["status"] == "done", d2.get("message"))
    check("产物名为 zipped.zip", d2["files"][0]["name"] == "zipped.zip", d2.get("files"))

    job3 = new_job("compress")
    upload(job3, [("blob.bin", blob, "blob.bin")])
    api("POST", f"/api/jobs/{job3}/run",
        {"format": "7z", "level": 1, "name": "split", "volume_size": "512k"})
    d3 = wait_job(job3)
    check("分卷压缩完成", d3["status"] == "done", d3.get("message"))
    names = [f["name"] for f in d3.get("files", [])]
    check("产生多个分卷", len(names) >= 2, names)
    check("第一卷命名为 .001", "split.7z.001" in names, names)

    section("4. 加密压缩（AES-256 + 文件名加密）")
    job4 = new_job("compress")
    upload(job4, [("说明.txt", (src / "说明.txt").read_bytes(), "说明.txt")])
    api("POST", f"/api/jobs/{job4}/run",
        {"format": "7z", "level": 5, "name": "locked",
         "password": "s3cr3t-密码", "encrypt_names": True})
    d4 = wait_job(job4)
    check("加密压缩完成", d4["status"] == "done", d4.get("message"))

    section("5. 解压：预览 + 选择性解压")
    job5 = new_job("extract")
    status, body = upload(job5, [("bundle.7z", archive.read_bytes(), "bundle.7z")])
    check("上传压缩包成功", status == 200, f"{status} {body}")

    status, preview = api("POST", f"/api/jobs/{job5}/preview", {})
    check("预览返回条目", status == 200 and len(preview["entries"]) == 5,
          len(preview.get("entries", [])))
    paths = [e["path"] for e in preview.get("entries", [])]
    check("中文条目名未损坏", any("照片说明" in p for p in paths), paths)
    check("识别出目录", sum(1 for e in preview["entries"] if e["is_dir"]) == 2)
    check("统计了原始总大小", preview["total_size"] > 2_000_000, preview["total_size"])

    status, _ = api("POST", f"/api/jobs/{job5}/run",
                    {"preserve_paths": True, "selected": ["docs/说明.txt"]})
    check("选择性解压被接受", status == 202, status)
    d5 = wait_job(job5)
    check("解压完成", d5["status"] == "done", d5.get("message"))
    check("只解出被选中的文件", len(d5["files"]) == 1, d5.get("files"))
    check("保留了目录结构", d5["files"][0]["name"] == "docs/说明.txt", d5["files"][0]["name"])
    check("多文件结果可打包下载", download(f"/api/jobs/{job5}/download-all", work / "e.zip") > 0)

    section("6. 解压：全部解压")
    job6 = new_job("extract")
    upload(job6, [("bundle.7z", archive.read_bytes(), "bundle.7z")])
    api("POST", f"/api/jobs/{job6}/run", {"preserve_paths": True})
    d6 = wait_job(job6)
    check("全部解压完成", d6["status"] == "done", d6.get("message"))
    check("解出全部 3 个文件", len(d6["files"]) == 3, [f["name"] for f in d6["files"]])

    section("7. 解压加密包（逐个密码场景）")
    locked = work / "locked.7z"
    download(f"/api/jobs/{job4}/download-all", locked)
    job7 = new_job("extract")
    upload(job7, [("locked.7z", locked.read_bytes(), "locked.7z")])

    status, body = api("POST", f"/api/jobs/{job7}/run", {})
    check("无密码解压被拒绝", status == 400, status)
    check("标记为需要密码", body.get("password_required") is True, body)

    status, body = api("POST", f"/api/jobs/{job7}/run", {"password": "wrong-password"})
    check("错误密码被同步拒绝（非 202）", status == 400, status)
    check("错误密码标记为需要密码", body.get("password_required") is True, body)
    _, still = api("GET", f"/api/jobs/{job7}")
    check("被拒后任务未启动", still["status"] == "pending", still["status"])

    status, body = api("POST", f"/api/jobs/{job7}/run", {"password": "s3cr3t-密码"})
    check("正确密码可解压", status == 202, f"{status} {body}")
    d7 = wait_job(job7)
    check("加密包解压完成", d7["status"] == "done", d7.get("message"))

    section("8. 安全边界")
    status, _ = api("GET", f"/api/jobs/{job6}/download?file=../../etc/passwd")
    check("下载路径穿越被拒绝", status != 200, status)
    status, _ = api("GET", f"/api/jobs/{job6}/download?file=/etc/passwd")
    check("下载绝对路径被拒绝", status != 200, status)
    status, _ = api("GET", "/api/jobs/doesnotexist000000000000000000")
    check("未知任务返回 404", status == 404, status)
    status, _ = api("POST", "/api/jobs?kind=bogus")
    check("非法 kind 被拒绝", status == 400, status)

    status, index = request("GET", "/")
    check("首页可访问", status == 200, status)
    check("首页含 YC-7ZIP 标记", isinstance(index, str) and "YC-7ZIP" in index)
    status, _ = request("GET", "/assets/style.css")
    check("样式表可访问", status == 200, status)
    status, _ = request("GET", "/assets/app.js")
    check("脚本可访问", status == 200, status)

    section("9. 清理")
    for jid in (job, job2, job3, job4, job5, job6, job7):
        try:
            api("DELETE", f"/api/jobs/{jid}")
        except Exception:  # noqa: BLE001 - 过期即可
            pass
    status, _ = api("GET", f"/api/jobs/{job}")
    check("任务删除后返回 404", status == 404, status)

    print("\n" + "=" * 46)
    if not failed:
        print(f"\033[32m全部通过：{passed} 项\033[0m")
        return 0
    print(f"\033[31m通过 {passed} 项，失败 {len(failed)} 项\033[0m")
    for item in failed:
        print(f"  - {item}")
    return 1


if __name__ == "__main__":
    sys.exit(main())

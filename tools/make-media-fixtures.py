#!/usr/bin/env python3
"""生成本地媒体素材，用于图览模式的验收。

特意包含：
- 一张横图、一张竖图，验证缩略图按长边缩放
- 一张带 EXIF Orientation=6 的 JPEG，验证服务端会把方向摆正
  （这是手机照片最常见的情况，不处理的话图览会一片躺倒）
- 一张 PNG、一张 GIF，验证多格式解码
- 一个故意损坏的 mp4，验证视频加载失败时退回图标而不是留破图

    python tools/make-media-fixtures.py <输出目录>
"""

from __future__ import annotations

import io
import struct
import sys
from pathlib import Path

from PIL import Image, ImageDraw

# EXIF Orientation 标签号
ORIENTATION_TAG = 0x0112


def striped(width: int, height: int, base: tuple[int, int, int]) -> Image.Image:
    """画一张有方向和纹理的图，缩略图里一眼能看出是否被旋转。"""
    img = Image.new("RGB", (width, height), base)
    d = ImageDraw.Draw(img)
    # 左上角一个明显的标记块，用来判断朝向
    d.rectangle([0, 0, width // 5, height // 5], fill=(255, 255, 255))
    for i in range(0, max(width, height), 40):
        d.line([(i, 0), (0, i)], fill=(20, 20, 30), width=6)
    d.rectangle([2, 2, width - 3, height - 3], outline=(240, 240, 240), width=8)
    return img


def with_orientation(img: Image.Image, orientation: int) -> bytes:
    """把 Orientation 标签写进 EXIF 再编码成 JPEG。"""
    buf = io.BytesIO()
    exif = img.getexif()
    exif[ORIENTATION_TAG] = orientation
    img.save(buf, format="JPEG", quality=88, exif=exif)
    return buf.getvalue()


def broken_mp4() -> bytes:
    """一个只有 ftyp 头的假 mp4：让浏览器去解码失败，走错误分支。"""
    return b"\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom" + b"\x00" * 32


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)

    # 横图：1600x900
    (out / "风景-横向.jpg").write_bytes(
        with_orientation(striped(1600, 900, (40, 90, 160)), 1)
    )

    # 竖图：900x1600
    (out / "人像-竖向.jpg").write_bytes(
        with_orientation(striped(900, 1600, (150, 70, 60)), 1)
    )

    # 关键用例：图本身是横的，但 EXIF 要求顺时针转 90 度 —— 手机竖拍就是这样
    (out / "手机竖拍-带EXIF旋转.jpg").write_bytes(
        with_orientation(striped(1600, 900, (70, 140, 90)), 6)
    )

    # PNG 与 GIF
    striped(1200, 800, (120, 110, 40)).save(out / "图表.png")
    frames = [striped(400, 300, (c, c // 2, 200)) for c in (60, 120, 180)]
    frames[0].save(out / "动画.gif", save_all=True, append_images=frames[1:], duration=300, loop=0)

    # 一个无后缀的文本文件，确认它进不了图览的图片筛选项
    (out / "读我.txt").write_text("这不是媒体文件\n", encoding="utf-8")

    (out / "损坏的视频.mp4").write_bytes(broken_mp4())

    for path in sorted(out.iterdir()):
        print(f"  {path.name}  {path.stat().st_size} bytes")
    return 0


if __name__ == "__main__":
    sys.exit(main())

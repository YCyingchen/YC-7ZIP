# AGENTS.md —— 给 AI 协作方（Codex / DSH / 其他）的项目说明

## 0. 动手之前先读对话记录

本项目的需求与历史决策，记在**本机**的 `docs/mimo/小米MiMo对话记录.md`
（用户与 Xiaomi MiMo 按时间顺序的完整对话）。**以后所有修改都要先读它。**

- 该文件**不提交进仓库**（仓库是公开的，而记录里有 NAS 路径、目录名这类私人信息，
  已在 `.gitignore` 里）。所以**换了机器就看不到它**——那时直接向用户要这份记录。
- 用户新给的对话记录**追加到文件末尾**，保留原文，不改写历史：这份文件的价值在于
  "当时到底决定了什么"。
- **逐字读过再动手**，别只读半截：记录里的判断有前后被推翻的（比如"仓库描述 404 是
  权限不足"后来发现其实是把 `PATCH` 写成了 `PUT`）。**以最新的那条结论为准**。

配套读物：

| 文件 | 用途 |
| --- | --- |
| `docs/mimo/小米MiMo对话记录.md`（本机，未提交） | "为什么是这样"——需求、取舍、踩过的坑 |
| [`CHANGELOG.md`](CHANGELOG.md) | 每个版本改了什么 |
| [`README.md`](README.md) | 当前能力、三种交付方式的边界、安全说明 |
| [`docs/`](docs) | fnOS 应用包 / 右键菜单 / 自更新 的研究笔记 |

规则：**不要推翻记录里已经定下的事**（版本号规则、目录可见性、root 与 allow-roots
边界、发布渠道、缓存策略…）。要改先说明"哪一条、为什么现在该变"。

## 1. 版本号与通道

- 版本号：`zip<年份后两位><月份两位>.<当月修订号三位>`，例：`zip2609.011` = 2026 年 9 月第 11 次修订。
  位数固定所以**字符串比较就是版本比较**。fpk 内部的 `26.9.11` 由 `tools/version.py` 换算。
- [`CHANNEL`](CHANNEL)：`test` / `stable`，当前是测试通道。
- 用户要求：**每次完成后上传到 GitHub + Docker Hub + 下载页**（fpk 还要部署到 NAS）。

## 2. 构建与发布：用 Python，不用 PowerShell 脚本

```
python tools/dev.py check|linux|dist|nas-test|nas-app|nas-download|ui|publish
python tools/e2e.py          端到端验收
python tools/version.py      版本号
tools/gitpush.sh             推送（这台机器直连 github.com:443 会超时，走 .env.local 里的代理）
```

## 3. 目录结构速查

```
main.go  index.html  assets/        裸二进制形态（静态资源用 go:embed 编译进二进制）
internal/engine  internal/job       压缩/解压引擎（7-Zip 调用、分卷、进度）
internal/server                      HTTP 层：handlers / media（缩略图）/ update / allowroots / csrf
deploy/fpk/                         飞牛应用包：manifest、wizard（安装/设置向导）、cmd（start/stop/status）
deploy/download-page/index.html     下载页
tools/                              全部 Python 构建脚本 + Playwright 验收脚本
```

约定：`index.html`、`assets/app.js`、`assets/style.css` 是**手写的单文件前端**，
不是从别处抄的模板，也没有构建步骤（改了就直接生效，但会被 `go:embed` 打进二进制）。

## 4. 这个环境里踩过的坑（都会静默出错，务必避开）

- **不要在工具调用里手打本机绝对路径**（历史上少写一个字母，害得改动落到不存在的目录，
  排查了好久）。会话工作目录本身就是项目目录，用相对路径。
- **PowerShell 发 HTTP 请求体时中文会变成 `?`**（默认 ASCII）→ 用 Python 或显式 UTF-8。
- `Set-Content` / `Get-Content -Raw` 按 GBK 读写，会破坏 UTF-8 文件。
- Git Bash 会改写 `/root/...` 这类参数 → `MSYS_NO_PATHCONV=1`。
- shell 的 `printf '%d' 09` 把月份当八进制 → 静默算错版本号（这也是改用 Python 的原因）。
- 静态资源曾用 `max-age=3600` 长缓存，导致"旧 JS 配新 HTML"→ 现在是 ETag + `no-cache` + URL 带版本号，
  **不要加回长缓存**（有回归测试 `TestStaticAssetsAreRevalidated` 钉着）。
- 权限不足时 GitHub 返回 **404 而不是 403**，响应头 `x-accepted-github-permissions` 会说缺哪项。
- 起测试实例时**按可执行文件路径杀进程**，只看端口会漏（SFTP 只回一句 `Failure`，很难查）。

## 5. 交付边界（别混）

| 形态 | allow-roots 由谁定 | 默认 |
| --- | --- | --- |
| fpk（发行包） | 安装向导填写，装好后在「应用中心 → 应用设置」里改 | `/vol1/1000`，留空则 `/vol1` |
| Docker | compose 里的 `YC7ZIP_ALLOW_ROOTS`（容器内路径） | `/share` |
| 裸二进制 | 命令行 `-allow-root`，可重复 | 不传则该功能关闭，只能上传 |

**父目录不隐含**：列了 `/vol5/1000/空间4` 不等于放开 `/vol5/1000`。
清单同时决定"能读写什么"和"从哪里开始显示"；清单外的路径**不显示、也不回带路径的报错**。

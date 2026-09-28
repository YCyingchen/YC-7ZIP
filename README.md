# YC-7ZIP

网页端的压缩与解压工具，**直接在 NAS 上读写文件**，不需要先把文件上传到浏览器里再打包。

支持 `7z` / `ZIP` / `RAR` / `TAR` / `GZ` / `BZ2` / `XZ` / `ZSTD` / `ISO` / `CAB` / `WIM` / `CPIO`
等格式，并且支持**分卷压缩与分卷解压**。

> 为什么值得单独做一个：飞牛 fnOS 自带的压缩功能**没有分卷选项**
> （zh-CN 语言包里「分卷」出现 0 次），而分卷恰恰是把大文件投递到网盘、
> 微信、FAT32 移动盘时最常需要的能力。

---

## 它长什么样

在飞牛文件管理器里右键一个压缩包 →「打开方式」→ YC-7ZIP，会弹出一个浮动窗口，
界面里已经把这个文件选好、把格式和分卷情况列清楚，选一下输出目录就可以解压：

```
┌ 文件管理器 ─────────────────────────────┐
│  📦 movie.7z.002          [右键]         │
│      └ 打开方式 ▸ 通过 YC-7ZIP 打开      │
└──────────────┬──────────────────────────┘
               ▼
   ┌ YC-7ZIP ────────────────────────────────┐
   │  分卷  这是一个分卷压缩包 · 共 6 卷       │
   │        movie.7z.001 … movie.7z.006      │
   │  压缩包内容  blob.bin            2.9 MB  │
   │  输出位置  /vol5/1000/空间4        [选择] │
   │                        [ 开始解压 ]      │
   └─────────────────────────────────────────┘
```

选中 `.002` 也能解压——程序自己会找到第一卷并把其余分卷收集起来。

---

## 三种部署方式

### 1. Docker（通用）

```bash
docker run -d --name yc-7zip \
  -p 8090:8080 \
  -v /vol1:/vol1 \
  -v /vol2:/vol2 \
  -v yc7zip-data:/data \
  ycyingchen/yc-7zip:latest \
  -addr :8080 -data /data -allow-root /vol1 -allow-root /vol2
```

多架构镜像：`linux/amd64`、`linux/arm64`。

然后打开 `http://<NAS 地址>:8090`。

### 2. 飞牛 fnOS 应用包（fpk）

```bash
# 在飞牛 NAS 上（已预装 fnpack）
git clone https://github.com/YCyingchen/YC-7ZIP.git
cd YC-7ZIP
fnpack build -d deploy/fpk          # 生成 deploy/fpk/yc7zip.fpk
```

然后到「应用中心 → 手动安装」选中这个 `.fpk`。

装好后会有两个入口：

- 桌面/应用列表里的 **YC-7ZIP** 图标；
- 文件管理器里对压缩包右键 →「打开方式」→ **YC-7ZIP 压缩解压**。

> ⚠️ **镜像必须先存在于仓库**。飞牛的应用中心在安装 fpk 时会去拉 compose 里写明的镜像，
> 镜像不存在就会安装失败并回滚。所以自己改过代码后，顺序是：
> `docker build` → `docker push` → 再 `fnpack build` + 安装。

> 端口只绑在 `127.0.0.1`：飞牛的应用网关是通过 unix socket 进来的，
> 不需要把端口暴露到局域网。想直接从别的机器访问端口，把 compose 里
> `"127.0.0.1:${wizard_app_port}:8080"` 的 `127.0.0.1:` 去掉，
> 并务必同时打开 `YC7ZIP_AUTH`。

> 右键菜单的实现方式是飞牛官方支持的**文件类型关联**（`app/ui/config` 的
> `fileTypes`）。飞牛不支持往右键菜单里塞任意菜单项，所以入口在「打开方式」子菜单里。

### 3. 裸二进制

从 [Releases](https://github.com/YCyingchen/YC-7ZIP/releases) 下载对应架构的压缩包：

```bash
tar -xzf yc-7zip-<版本>-linux-amd64.tar.gz
cd yc-7zip-<版本>-linux-amd64
./start.sh 8090                     # 默认端口 8090
./stop.sh
```

包内自带官方 `7zz`，不需要另外装 7-Zip。

---

## 命令行参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-addr` | `:8080` | HTTP 监听地址；留空则只监听 `-socket` |
| `-socket` | 空 | 额外监听一个 unix socket，例如 `/trim/app.sock` |
| `-base-path` | 空 | URL 前缀，例如 `/app/yc7zip` |
| `-data` | 系统临时目录 | 任务工作区；下载模式的结果放这里 |
| `-allow-root` | 无 | 允许读写的目录，**可重复**。不指定就不能直接操作 NAS 文件 |
| `-auth` | 空 | HTTP Basic 认证，格式 `user:password` |
| `-max-upload` | `4G` | 单任务上传上限，`0` 表示不限 |
| `-job-ttl` | `2h` | 结果保留时长 |

也可以用环境变量：`YC7ZIP_ADDR` / `YC7ZIP_SOCKET` / `YC7ZIP_BASE_PATH` / `YC7ZIP_DATA` /
`YC7ZIP_AUTH` / `YC7ZIP_MAX_UPLOAD` / `YC7ZIP_ALLOW_ROOTS`（逗号分隔）。

`YC7ZIP_7Z` 可以指定 7-Zip 可执行文件的位置；不设的话会依次在自身目录、
`PATH` 和常见安装位置里找。

---

## 安全说明

这个工具会按你的配置读写 NAS 上的文件，请留意：

- **`-allow-root` 是唯一的边界。** 只有列在这里的目录能被浏览和写入，
  其它路径一律拒绝（返回 403）。fpk 默认放开 `/vol1`,`/vol2`,`/vol3`,`/vol4`,`/vol5`, `/`，
  也就是容器挂进来的所有卷；想收紧就把 `/` 去掉，只留需要的目录。
- **容器能看到的路径 = compose 里挂进去的路径。** 应用的 `/` 是容器自己的根，
  不是 NAS 的根；数据在哪些卷上，就把哪些卷挂进来。
- **容器以 root 运行**，这是为了能写入属于 NAS 用户、权限受限的目录。
  fpk 默认只把端口绑在 `127.0.0.1`，访问必须经过飞牛的登录态；
  如果放开端口，请同时打开 `-auth`。
- 解压时会先校验压缩包内的路径，遇到 `..`、绝对路径等越界条目会直接拒绝，
  不会依赖 7-Zip 自己的净化行为。
- 上传与下载都限制在任务工作区内，`?file=` 参数经过两次路径校验
  （词法 + 真实路径），防止越界读取。

---

## 开发

```bash
go test ./...            # 需要 7-Zip：export YC7ZIP_7Z=/path/to/7zz

# 准备发布件（二进制 + 7-Zip）
deploy/build.sh dist

# 构建镜像
deploy/build.sh docker 1.0.0
```

目录结构：

```
main.go                  入口、参数解析、监听方式
embed.go                 前端资源内嵌
index.html assets/       前端（单页，零依赖，中英双语）
internal/engine/         7-Zip 封装：列表 / 解压 / 压缩 / 分卷识别
internal/job/            任务工作区与生命周期
internal/server/         HTTP API、NAS 文件浏览、服务端路径读写
deploy/fpk/              飞牛应用包源码
deploy/build.sh          构建脚本
tools/                   开发与验收脚本
```

### 验收脚本

项目自带三层验收，全部针对真实运行的服务：

```bash
# 1. Go 单元测试与集成测试
export YC7ZIP_7Z=/opt/7z/7zz
go test ./... -v

# 2. HTTP 端到端（对着已部署的服务跑）
powershell -File tools/e2e-nas.ps1 -Base http://192.168.1.9:8090

# 3. 真实浏览器验收（Playwright）
export PW_MODULE=/path/to/node_modules/playwright
node tools/ui-test.cjs http://192.168.1.9:8090
```

浏览器验收覆盖：浏览 NAS 文件 → 多选 → 分卷压缩写回 NAS → 选中**中间某一卷**解压 →
校验落盘内容 → 语言/主题切换 → 移动端无横向溢出 → 被 `?path=` 唤起时自动选中。

---

## 版本号规则

```
zip<年份后两位><月份>.<当月修订号>

zip2609.001
│  │  │   └── 该月第 1 次修改
│  │  └────── 09 月
│  └───────── 2026 年
└──────────── 项目前缀
```

`VERSION` 文件是唯一来源，Docker 标签与二进制内的 `main.version` 都由它推导：

```bash
deploy/version.sh bump            # zip2609.001 -> zip2609.002
deploy/version.sh bump --month    # 跨月时重置：-> zip2610.001
deploy/version.sh check           # 校验格式
```

**飞牛 fpk 是唯一的例外**：`fnpack` 打包含有版本号的 manifest 时只接受 semver
（`x.y.z[-r]`），`zip2609.001` 会被直接拒绝。所以 manifest 里写的是换算值
`26.9.1`（年.月.修订），由脚本生成、CI 校验：

```bash
deploy/version.sh fpk             # zip2609.001 -> 26.9.1
deploy/version.sh sync-manifest   # 把换算值写进 deploy/fpk/manifest
```

发布就是在推好代码后打个同名标签：

```bash
git tag zip2609.001 && git push origin zip2609.001
```

---

## 反馈与排错

界面上出错时不会只弹一个会消失的提示：**完整报错会留在页面上**，并附带
环境信息（版本号、7-Zip 路径与版本、来源路径、输出目录、分卷信息）。

- 顶栏右上角的 GitHub 图标 → 项目仓库；
- 报错面板里的「复制报错信息」→ 一键复制全部内容；
- 报错面板里的「在 GitHub 提交 Issue」→ **自动把报错与环境信息预填进 Issue**，
  你只需要补一句"我做了什么"。

仓库地址默认指向 <https://github.com/YCyingchen/YC-7ZIP>，
fork 之后可以用 `-repo https://github.com/你/你的仓库` 或 `YC7ZIP_REPO` 改掉。

---

## 已知限制

- **RAR 只能解压。** RAR 的压缩算法未开放授权，任何第三方软件都无法生成 `.rar`。
- 飞牛的文件类型关联只按**扩展名**匹配文件，所以右键压缩只对文件生效；
  压缩整个文件夹请从应用图标进入。
- 飞牛原生压缩/解压的菜单项无法被第三方替换，只能作为「打开方式」的一个候选，
  可以在「管理默认打开方式」里把 YC-7ZIP 设为某个扩展名的默认。

---

## 许可

MIT。镜像内附带的 7-Zip 遵循其自身许可（LGPL + unRAR 限制）。

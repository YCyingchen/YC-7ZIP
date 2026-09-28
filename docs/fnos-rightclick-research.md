# fnOS 文件管理器右键集成 — 调研报告

**结论先行**：存在官方支持的右键集成机制，但只有一种形式 —— **文件类型关联（"打开方式"）**。
它不能往右键菜单里塞任意菜单项。而用户想要的功能（右键 → 分卷压缩/解压 → 读取本机文件）
**已经有一个第三方应用在你这台 NAS 上跑通了**：`xinZip`（display_name `分卷解压`）。

---

## 1. 机制（VERIFIED）

文档：https://developer.fnnas.com/docs/core-concepts/app-entry/ ——「注册文件打开方式」

> 当应用可以从文件管理器右键菜单打开或处理文件时，可以注册文件打开方式。
> 用户通过该入口打开文件时，飞牛 fnOS 会在 URL 后追加 `path` 查询参数。

注册位置是 **`app/ui/config`** 的 `.url.<entryId>`，关键字段 `fileTypes` + `noDisplay:true`。

真实例证 `/vol1/@appcenter/xinZip/ui/config`（安装于本机 NAS）：

```json
{ ".url": { "xinZip.Application": {
    "title": "分卷解压", "icon": "images/icon_{0}.png", "type": "iframe",
    "protocol": "http", "url": "/cgi/ThirdParty/xinZip/index.cgi",
    "allUsers": true, "fileTypes": ["001", "rar", "zip"], "noDisplay": true } } }
```

**不存在** `contextMenu` / `file_action` / `right_click` / `open_with` 之类的 manifest 或 resource 键：
把本机全部已安装应用的 `ui/config` 键名枚举后，只有
`title desc icon type protocol port url allUsers fileTypes noDisplay microApp
gatewaySocket gatewayPrefix control{accessPerm,showRoute,auth,port,path,fullUrl,*Perm}`。
16 个应用注册了 `fileTypes`（`xinZip trim.docs m.text.editor Online3DViewer` …），无一使用菜单键。

## 2. 点击后收到什么 / 界面怎么弹（VERIFIED）

收到 `?path=<URL 编码的绝对路径>`，**单一路径**。nginx 访问日志里有你本机（192.168.1.232）的真实点击记录：

```
GET /cgi/ThirdParty/xinZip/index.cgi?path=%2Fvol5%2F1000%2F…%2Fycfrp-269.001-compose.zip 200
```

`/usr/trim/www/assets/appview-*.js` 里 `type:"iframe"` 落地为
`{appName:"appview.iframe", type:TEMPORARY, iframe:true, url}` —— 即 **fnOS 桌面里的浮动窗口 + iframe**，
不是文件管理器页面内的 modal；`type:"url"` 则是浏览器新标签页（`trim.docs` 用这种）。

菜单位置：右键 →「打开方式」(openWith) 子菜单，项文案 `通过 {appName} 打开`；另有两项
`管理默认打开方式`、`前往应用中心搜索`（无关联扩展名时）。

## 3. UI 怎么被伺服（VERIFIED）

- **index.cgi**：`/cgi/ThirdParty/{appname}/index.cgi/`。nginx `location /cgi` → `unix:/var/run/trim_http_cgi.socket`，
  由 `trim_http_cgi` 执行 `app/ui/index.cgi`；**先校验 NAS 登录态**，`protocol`/`port` 被忽略。每次请求起进程、不支持 WebSocket。
- **统一网关**（推荐给容器）：`gatewayPrefix` + `gatewaySocket`（socket 放 `$TRIM_APPDEST` 下），路径 `/app/{appname}`，
  转发 `X-Trim-Userid` / `X-Trim-Username` / `X-Trim-Isadmin`，支持 WebSocket。Docker 应用挂载 `TRIM_APPDEST` 即可用。

## 4. 本机文件读取权限（VERIFIED）

`config/privilege` 的 `run-as:"package"` 建包用户；实测 `id xin_zip` → `uid=988(xin_zip) gid=901(AppUsers)`，
**自动加入 `AppUsers` 组**。路径来源：`TRIM_DATA_SHARE_PATHS`（resource 声明）、`TRIM_DATA_ACCESSIBLE_PATHS`
（用户授权目录）、开放 API 的目录/文件选择器 + `file-acl` + `path-convert`。注册表 `data-share` 会给包用户授 ACL。
实测 `/vol5/1000` 权限 `other::r-x`，因此共享目录直接可读 —— **不需要上传，直接按绝对路径读盘**。

## 5. 原生压缩/解压（VERIFIED，且不可替换）

原生**已存在**：`/usr/trim/www/locales/zh-CN/apps/file-manager.json` 有
`"extract":"解压"`、`"compress":"压缩"`，压缩对话框含 `压缩等级 / 加密压缩 / 只打包不压缩 / 密码`;
`当前位置不支持在线解压，请在存储位置中操作`。压缩包类型的默认动作走原生 unzip。
第三方**不能替换或扩展**这些菜单项，只能（a）成为某扩展名的**默认打开方式**（需用户在"管理默认打开方式"里设），
（b）在无关联扩展名时让用户"前往应用中心搜索"。

**关键空白**：中文语言包里 `分卷` 出现 **0 次** —— fnOS 原生压缩**没有分卷选项**。这就是 YC-7ZIP 的立足点。

## 备选方案（按成本排序）

1. **保留 Docker 应用 + 加文件类型关联 + 统一网关入口**（成本最低，收益最大）：`app/ui/config` 注册
   `fileTypes:["001","7z","zip","rar","part1.rar","tar","gz"]`，`type:"iframe"`，`url:"/app/yc7zip?path=…"`；
   Go 服务读 `?path`，直接对绝对路径分卷压缩/解压。满足「右键 → 弹窗 → 读取本机 + 分卷」。
2. 用 `index.cgi` 反代到现有容器端口（比 socket 简单）；代价：每请求起 CGI、无 WebSocket。
3. 申请成为 `zip/7z` 的**默认打开方式**，最接近"顶掉原生动作为 YC-7ZIP"。
4. 文件夹压缩：`fileTypes` 只按扩展名匹配文件，**没有证据**支持目录关联 —— 目录压缩只能走原生对话框。

## 不确定项（勿当事实）

- `fileTypes` 是否支持目录/文件夹类型 —— 未找到任何证据，倾向不支持。
- 多选时的行为：实测只传单个 `path`；多选是否开 N 个窗口或只取第一个，未验证。
- 成为默认打开方式后能否覆盖压缩包的原生 `解压` 默认动作，未验证。
- 入口注册是否安装后立即生效，还是需重启文件管理器/App Center，未验证。
- `resource` 的 `permission.rw` 里 xinZip 写的是 **appname** 而非 `privilege.username`（`xin_zip`），
  两者哪个被接受未验证。
- 网关入口在文件管理器右键场景下是否也会追加 `?path`（文档只对文件入口描述），未实测。

**方法备注**：NAS 侧只做只读检查（cat/grep/ls/id/getfacl/读 nginx 日志），未修改任何文件。
本机无 python/node，SSH 用 `ssh.exe` + 自建 askpass 助手（密码取自 `.env.local`，未落盘）。

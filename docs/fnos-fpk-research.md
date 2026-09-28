# fnOS (飞牛 fnOS) `.fpk` packaging — verified recipe

Researched from the official docs (developer.fnnas.com) **and verified empirically** by downloading the
official packer `fnpack` 1.2.3 (Windows x64) and actually building + unpacking a `.fpk`.
Cross-checked against real published packages: `kisser214/fnos-istoreosrouter-fpk` (ARM64 Docker app),
`hpyer/fnos-apps`, `conversun/fnos-apps`.

## 1. What `.fpk` is + inner layout (VERIFIED by hex dump + untar)

`.fpk` is a **gzip-compressed tar** (`tar.gz`; magic `1f 8b`, first entry is a tar header). Not a zip.

Root of the inner tar — note **`app/` is not a directory inside the fpk**, it is repacked as a nested
`app.tgz`:

```
manifest                     # INI-style key = value, no extension
ICON.PNG                     # 64x64
ICON_256.PNG                 # 256x256
app.tgz                      # nested tar.gz: the app/ dir contents + config/{privilege,resource}
cmd/                         # 9 lifecycle scripts, NO file extension
  main install_init install_callback upgrade_init upgrade_callback
  uninstall_init uninstall_callback config_init config_callback
config/privilege             # JSON
config/resource              # JSON
wizard/                      # optional wizard JSON files (install/upgrade/uninstall/config)
```

All tar entries come out `-rw-rw-rw-`, uid/gid 0 — **executable bits are not stored**, so building on
Windows is fine (fnOS chmods `cmd/*` on install).

Authoring (source) tree that `fnpack build` consumes:

```
yc7zip/
├── manifest
├── ICON.PNG   ICON_256.PNG
├── app/
│   ├── docker/docker-compose.yaml
│   └── ui/config , ui/images/icon_64.png , ui/images/icon_256.png
├── cmd/  (9 scripts above)
├── config/privilege , config/resource
└── wizard/            (optional, may be empty)
```

## 2. `manifest` format

INI-style `key = value`, one per line. **Required (enforced by fnpack, found by iteration):**
`appname`, `version`, `source`, `display_name`.

| key | required | notes |
|---|---|---|
| `appname` | yes | unique ID; also prefixes entry IDs in `app/ui/config` |
| `version` | yes | e.g. `1.0.0` |
| `display_name` | yes | shown in App Center |
| `source` | yes | `thirdparty` for third-party apps |
| `platform` | no* | **validated**: one of `x86`, `arm`, `loongarch`, `risc-v`, `all` |
| `desc` | no* | HTML allowed |
| `maintainer`, `maintainer_url`, `distributor`, `distributor_url` | no* | |
| `os_min_version`, `os_max_version` | no | fnOS version range |
| `ctl_stop` | no | `true` shows start/stop/status in App Center; `false` hides |
| `checkport` | no | default `true` |
| `service_port` | no | host port the app listens on |
| `install_type` | no | empty = user picks volume; `root` = system partition. **Not validated by fnpack** |
| `install_dep_apps` | no | `name>1.2.3:other` |
| `desktop_uidir` | no | default `ui` |
| `desktop_applaunchname` | no | entry ID opened from the app card |
| `disable_authorization_path` | no | `true` hides authorized-folder settings |
| `changelog` | no | user-facing update notes |
| `checksum` | auto | **MD5 of `app.tgz`** — written by `fnpack`, don't hand-write |

\* not required by the packer, but should be set for real use.

Working sample (`platform` must stay in sync with your image's supported arch):

```ini
appname               = yc7zip
version               = 1.0.0
display_name          = YC-7ZIP
desc                  = Web-based archive compress / extract tool (zip / 7z / rar / tar).
source                = thirdparty
platform              = all
maintainer            = YC
maintainer_url        = https://example.com
distributor           = YC
desktop_uidir         = ui
desktop_applaunchname = yc7zip.main
service_port          = 8080
checkport             = true
ctl_stop              = true
os_min_version        = 1.2.0
changelog             = First release
```

## 3. Docker compose location + image reference

- File: **`app/docker/docker-compose.yaml`** (path is configurable, see below).
- Declared in `config/resource`; `path` is **relative to `app/`**:

```json
{ "docker-project": { "projects": [ { "name": "yc7zip", "path": "docker" } ] } }
```

- The image is a plain compose `image:` reference — **remote registry pull**, e.g.
  `ghcr.io/you/yc-7zip:1.0.0` or `you/yc-7zip:1.0.0` (Docker Hub). **Do not** try to embed a
  `docker save` tarball; nothing in the docs or any published package does this (see §8).
- fnOS injects env vars usable inside compose: `TRIM_SERVICE_PORT`, `TRIM_APPDEST`, `TRIM_PKGVAR`,
  `TRIM_DATA_SHARE_PATHS`. Wizard values are also available (e.g. `${wizard_port:-8080}`).

```yaml
services:
  yc7zip:
    image: ghcr.io/you/yc-7zip:1.0.0
    container_name: yc7zip
    restart: unless-stopped
    ports:
      - "${TRIM_SERVICE_PORT}:8080"
    volumes:
      - "${TRIM_PKGVAR}/data:/data"
```

`container_name` matters: `cmd/main status` greps it. Data that must survive restart goes under
`TRIM_PKGVAR` (`/var/apps/<app>/var` → `/vol<n>/@appdata/<app>`); user-visible folders are declared as
`data-share` in `config/resource` and created by fnOS with Windows ACLs.

## 4. Icons

- `ICON.PNG` **64×64**, `ICON_256.PNG` **256×256**, at package root. PNG (JPG tolerated).
- ≤1024 KB, sRGB, full square canvas, **rounded-rect** visual body (no edge-to-edge square), legible at 64px.
- Optional per-entry icons live in `app/ui/images/icon_64.png` + `icon_256.png`, referenced as
  `"icon": "images/icon_{0}.png"`.

## 5. `cmd/` lifecycle scripts

No extension, `#!/bin/bash`. Invocation: `install_init` → extract → `install_callback`;
`upgrade_init`/`upgrade_callback`; `uninstall_init`/`uninstall_callback`;
`config_init`/`config_callback`; and **`main start|stop|status`** (status: `exit 0` = running,
`exit 3` = not running, `exit 1` = error). Write user-visible errors to `$TRIM_TEMP_LOGFILE`.
Scripts must be idempotent. fnpack hard-requires 7 of them (`main`, `install_init`,
`install_callback`, `upgrade_init`, `upgrade_callback`, `uninstall_init`, `uninstall_callback`);
`config_init`/`config_callback` are optional — ship all 9 anyway.

For a Docker app, `start`/`stop` should be no-ops (App Center drives compose); only `status` matters:

```bash
#!/bin/bash
is_running() { docker inspect yc7zip 2>/dev/null | grep -q '"Status": "running"'; }
case "$1" in
  start|stop) exit 0 ;;
  status) is_running && exit 0 || exit 3 ;;
  *) echo "unknown: $1" > "$TRIM_TEMP_LOGFILE"; exit 1 ;;
esac
```

## 6. Build commands (no signing)

Official CLI `fnpack` (v1.2.3), Windows amd64 binary:
`https://static2.fnnas.com/fnpack/fnpack-1.2.3-windows-amd64` (kept at `tools/fnpack.exe`).
Linux amd64 sha256 `54b97fa7b70968c4d05c79840f5daeff508957d0bb2062fdb0376d00d9615c93`.

```
fnpack create yc7zip --template docker --without-ui false   # scaffold (creates ./yc7zip)
fnpack build -d yc7zip                                      # -> ./yc7zip.fpk (written to CWD)
```

`fnpack build` verifies required files/fields and injects `checksum`; **there is no signing**. One
`.fpk` per architecture: set `platform = x86` or `arm` (or `all` when the payload is arch-neutral and
the image is multi-arch). Building by hand (tar + nested `app.tgz` + md5) is possible — a third-party
project does it — but `fnpack` is the official path and is what was verified here.

Because the user's NAS already ships `fnpack`, the package can equally be built **on the NAS**
(e.g. `tools/nas.py putdir` the source, then `fnpack build` over SSH) and installed immediately with
`appcenter-cli install-fpk`, avoiding any host/target tar differences.

Build-time validation gotchas hit while testing:
- Entry IDs in `app/ui/config` **must start with `appname`** (`Packing failed. The entry name
  "hello-docker.Application" in "app/ui/config" should start with yc7zip`).
- Required files: `manifest`, `config/privilege`, `config/resource`, `ICON.PNG`, `ICON_256.PNG`,
  `cmd/main`, `app/` — but **`wizard/` is NOT actually enforced** despite the docs' checklist.
- `.DS_Store`/junk in `app/` gets packed; strip before building.

## 7. Install on fnOS

1. App Center → **手动安装 / Manual install** → pick `yc7zip.fpk` (local testing only; not a
   distribution channel). Or on the NAS shell: `appcenter-cli install-fpk yc7zip.fpk`
   (add `--env config.env` to pre-answer wizard fields); `appcenter-cli default-volume` sets the
   target volume, `appcenter-cli list|start|stop <app>` manage it.
2. Gotchas: the Docker image must be **pullable from the NAS** at install/start and must ship a
   manifest for the device arch (x86_64 NAS vs ARM NAS). `service_port` in `manifest`, the compose
   host-port mapping, and `port` in `app/ui/config` must all agree, or the desktop tile will not open.
   Don't hardcode `/vol5` — use `TRIM_APPDEST` / `TRIM_PKGVAR` / `TRIM_DATA_SHARE_PATHS`.

Optional: `config/resource` → `"port-config": { "protocol-file": "YC7ZIP.sc" }` with a
`YC7ZIP.sc` file (`[YC7ZIP]` / `port_forward="yes"` / `src.ports="8080/tcp"`) declares port
forwarding — found in real packages but **undocumented**; the official example does without it.

## 8. Remote image vs embedded tar — **use a remote image**

Every published `.fpk` examined references a registry image in compose (`istoreosrouter` pulls
`wukongdaily/openwrt-istoreos:arm64-latest@sha256:...` from Docker Hub; another project publishes to
`ghcr.io`). The docs only ever show `image:`; there is **no documented mechanism to embed a
`docker save` tarball**, and doing so would bloat `app.tgz` (which the `.fpk` checksums as one blob).
So: push a multi-arch image (amd64+arm64 if you want `platform = all`) and reference it by tag/digest.

## 9. On-device verification (real fnOS NAS, 192.168.1.9)

Facts read directly from the user's live fnOS box (read-only):

- Arch **`x86_64`**, Docker **29.6.2**, fnOS userland is **Debian 12 (bookworm)**-based.
- **`appcenter-cli` 1.0.1 and `fnpack` are both preinstalled at `/usr/local/bin/`** — so a package can
  be built and installed entirely on the NAS. `appcenter-cli` subcommands: `install`, `install-fpk`,
  `install-local`, `uninstall`, `start`, `stop`, `status`, `list`, `check`, `manual-install`,
  `default-volume`.
- Volumes present: `/vol00 /vol02 /vol1 /vol2 /vol3 /vol4 /vol5`. Installed apps here land on
  **`/vol1`** (the volume is user-selectable — never hardcode it).
- Installed layout of a real third-party Docker app (`/var/apps/coder-docker`) — matches the docs:

```
/var/apps/coder-docker/
├── cmd/            # 9 scripts, mode 755 after install (fnOS sets exec bits)
├── config/         # privilege, resource
├── ICON.PNG  ICON_256.PNG  manifest
├── etc  -> /vol1/@appconf/coder-docker
├── home -> /vol1/@apphome/coder-docker
├── meta -> /vol1/@appmeta/coder-docker
├── target -> /vol1/@appcenter/coder-docker     # = TRIM_APPDEST
├── tmp  -> /vol1/@apptemp/coder-docker
├── var  -> /vol1/@appdata/coder-docker         # = TRIM_PKGVAR
├── shares/
└── wizard/
```

  and inside `target/`: `config/{privilege,resource}`, `docker/docker-compose.yaml`, `ui/{config,images}`.
  ⇒ the fpk's root `config/` is deployed to **both** `/var/apps/<app>/config/` and `<target>/config/`,
  and compose is read from `${TRIM_APPDEST}/docker/docker-compose.yaml`. Confirms §1's nested `app.tgz`.

- A real `config/resource` uses `data-share` with explicit **`permission.rw`** listing the app user
  (`"docker-coder"`), and `privilege.username` is author-chosen (`docker-coder`, not auto-derived).

### IMPORTANT: `arch` vs `platform`

The official docs document only `platform`, but **every surveyed installed app carries an `arch` key**,
and many fill in one and leave the other empty:

| key | observed values |
|---|---|
| `platform` | `x86`, `all`, occasionally `"all"` (quoted) — fnpack validates: `x86\|arm\|loongarch\|risc-v\|all` |
| `arch` | `x86_64`, `all`, or a Synology-style CPU list (e.g. `python312` lists `apollolake avoton braswell …`) |

Examples: `coder-docker` → `arch = all`, `platform =` (empty); `allinssl` → `arch = x86_64`,
`platform =` (empty); `fn-appsettings` → `platform = all` with no meaningful `arch`.
**Recommendation:** set `platform` (official, packer-validated) *and* a matching `arch`
(`all` or `x86_64`) so both code paths agree. Which one fnOS actually gates on is unverified — see below.

## Not verified (do not treat as fact)

- Whether fnOS supports **offline/embedded** Docker images by any undocumented route — no doc, no
  example. Assumed unsupported.
- Exact legal values of `install_type` beyond docs (`""`, `root`): `fnpack` accepts arbitrary values,
  so validation is server-side and untested here.
- `config/resource` keys other than the three documented ones (`data-share`, `usr-local-linker`,
  `docker-project`): real packages also use `port-config` and `systemd-unit`, which appear nowhere in
  the current docs. Treated as working-but-unsupported.
- Whether the image is pulled at *install* time or first *start* (App Center runs compose; docs only
  say to verify the pull during install testing).
- Whether App Center publishing requires signing/packaging beyond `fnpack build` — the publish path is
  currently a WeChat group / developer backend, no CLI or signing key documented.
- `os_min_version` / `os_max_version` semantics and whether the installer enforces them (fnpack does not).
- **Whether the installer actually gates compatibility on `arch` or on `platform`** (both appear in real
  packages; §9). Set both to be safe.
- Whether `platform` values `loongarch` / `risc-v` are genuinely accepted by the *installer* (only
  fnpack's validator was tested).

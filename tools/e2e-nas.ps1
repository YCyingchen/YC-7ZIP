# YC-7ZIP — 针对已部署服务端的端到端验收脚本
#
# 用法：
#   powershell -File tools/e2e-nas.ps1 -Base http://192.168.1.9:8090
#
# 覆盖：健康检查 → 压缩（含中文/子目录/加密）→ 分卷 → 下载校验
#       → 解压预览 → 解压 → 打包下载 → 安全边界（路径穿越、错误密码）

param(
  [string]$Base = "http://192.168.1.9:8090",
  [string]$Work = "$env:TEMP\yc7zip-e2e"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$script:Pass = 0
$script:Fail = 0

function Check([string]$Name, [bool]$Ok, [string]$Detail = "") {
  if ($Ok) {
    $script:Pass++
    Write-Host ("  [PASS] " + $Name) -ForegroundColor Green
  } else {
    $script:Fail++
    Write-Host ("  [FAIL] " + $Name + $(if ($Detail) { " -> $Detail" } else { "" })) -ForegroundColor Red
  }
}

function Section([string]$Title) {
  Write-Host ""
  Write-Host "== $Title" -ForegroundColor Cyan
}

function Api([string]$Method, [string]$Path, $Body = $null) {
  $uri = "$Base$Path"
  if ($null -eq $Body) {
    return Invoke-RestMethod -Uri $uri -Method $Method -TimeoutSec 60
  }
  $json = $Body | ConvertTo-Json -Depth 8 -Compress
  return Invoke-RestMethod -Uri $uri -Method $Method -Body $json `
    -ContentType "application/json; charset=utf-8" -TimeoutSec 60
}

# 返回 @{ Status = <int>; Body = <parsed or raw> }
function ApiRaw([string]$Method, [string]$Path, $Body = $null) {
  $uri = "$Base$Path"
  try {
    if ($null -eq $Body) {
      $r = Invoke-WebRequest -Uri $uri -Method $Method -TimeoutSec 60 -UseBasicParsing
    } else {
      $json = $Body | ConvertTo-Json -Depth 8 -Compress
      $r = Invoke-WebRequest -Uri $uri -Method $Method -Body $json `
        -ContentType "application/json; charset=utf-8" -TimeoutSec 60 -UseBasicParsing
    }
  } catch {
    $resp = $_.Exception.Response
    if ($null -eq $resp) { throw }
    $code = [int]$resp.StatusCode
    # PowerShell 5.1 exposes the error body through ErrorDetails; reading the
    # raw response stream afterwards usually yields nothing.
    $text = $_.ErrorDetails.Message
    if (-not $text) {
      try {
        $reader = New-Object System.IO.StreamReader($resp.GetResponseStream())
        $text = $reader.ReadToEnd()
      } catch { $text = "" }
    }
    $parsed = $null
    try { $parsed = $text | ConvertFrom-Json } catch { }
    return @{ Status = $code; Body = $parsed; Text = $text }
  }
  $parsed = $null
  try { $parsed = $r.Content | ConvertFrom-Json } catch { $parsed = $r.Content }
  return @{ Status = [int]$r.StatusCode; Body = $parsed; Text = $r.Content }
}

# 用 curl.exe 上传，PS 5.1 没有原生的 multipart 支持
function Upload([string]$JobId, [hashtable[]]$Files) {
  $args = @("-s", "-w", "`n%{http_code}", "-X", "POST", "$Base/api/jobs/$JobId/upload")
  foreach ($f in $Files) {
    $args += "-F"
    $args += "relpath=$($f.Rel)"
    $args += "-F"
    $args += "files=@$($f.Path);filename=$($f.Name)"
  }
  $out = & curl.exe @args 2>&1
  $text = ($out | Out-String).Trim()
  $idx = $text.LastIndexOf("`n")
  $code = if ($idx -ge 0) { [int]$text.Substring($idx + 1) } else { 0 }
  $bodyText = if ($idx -ge 0) { $text.Substring(0, $idx) } else { $text }
  $parsed = $null
  try { $parsed = $bodyText | ConvertFrom-Json } catch { }
  return @{ Status = $code; Body = $parsed; Text = $bodyText }
}

function WaitJob([string]$JobId, [int]$TimeoutSec = 180) {
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  while ((Get-Date) -lt $deadline) {
    $j = Api "GET" "/api/jobs/$JobId"
    if ($j.status -in @("done", "error", "cancelled")) { return $j }
    Start-Sleep -Milliseconds 300
  }
  throw "任务 $JobId 在 $TimeoutSec 秒内未结束"
}

function NewJob([string]$Kind) {
  return (Api "POST" "/api/jobs?kind=$Kind").id
}

function Save([string]$Path, [string]$OutFile) {
  Invoke-WebRequest -Uri "$Base$Path" -OutFile $OutFile -TimeoutSec 300 -UseBasicParsing
  return (Get-Item $OutFile)
}

# ------------------------------------------------------------------ 准备数据

Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force "$Work\src\深空" | Out-Null
Set-Content -Path "$Work\src\readme.txt" -Value "hello yc-7zip from NAS" -NoNewline
Set-Content -Path "$Work\src\深空\资料.txt" -Value "中文文件名测试" -NoNewline
$bytes = New-Object byte[] (2MB)
(New-Object Random).NextBytes($bytes)
[System.IO.File]::WriteAllBytes("$Work\src\blob.bin", $bytes)

Write-Host "目标服务端：$Base"

# --------------------------------------------------------------- 1. 健康检查

Section "1. 健康检查与格式表"
$h = Api "GET" "/api/health"
Check "服务可用" ($h.ok -eq $true)
Check "7-Zip 引擎已就绪" ($h.engine.path -ne $null -and $h.engine.path -ne "")
Write-Host ("     引擎：" + $h.engine.path + " v" + $h.engine.version)
Check "可创建 7z/zip/tar/gzip/xz/zstd" (($h.engine.create -join ",") -match "7z" -and ($h.engine.create -join ",") -match "zip")

$f = Api "GET" "/api/formats"
$ids = ($f.formats | ForEach-Object { $_.id }) -join ","
Check "格式表含 rar 且标记为仅解压" ($ids -match "rar")
$rar = $f.formats | Where-Object { $_.id -eq "rar" }
Check "rar 只读（capability=0）" ($rar.capability -eq 0)

# ------------------------------------------------------------------ 2. 压缩

Section "2. 压缩 7z（中文名 + 子目录）"
$job = NewJob "compress"
$up = Upload $job @(
  @{ Rel = "docs/readme.txt";  Path = "$Work\src\readme.txt"; Name = "readme.txt" },
  @{ Rel = "docs/深空/资料.txt"; Path = "$Work\src\深空\资料.txt"; Name = "资料.txt" },
  @{ Rel = "docs/blob.bin";    Path = "$Work\src\blob.bin";  Name = "blob.bin" }
)
Check "上传成功" ($up.Status -eq 200) "HTTP $($up.Status) $($up.Text)"

$run = ApiRaw "POST" "/api/jobs/$job/run" @{ format = "7z"; level = 5; name = "bundle" }
Check "启动压缩被接受" ($run.Status -eq 202) "HTTP $($run.Status) $($run.Text)"
$done = WaitJob $job
Check "压缩完成" ($done.status -eq "done") $done.message
Check "进度到达 100%" ($done.progress -eq 100)

$art = Save "/api/jobs/$job/download-all" "$Work\bundle.7z"
Check "下载到 7z 产物" ($art.Length -gt 0)
$magic = [System.IO.File]::ReadAllBytes($art.FullName)[0..5]
$is7z = ($magic[0] -eq 0x37 -and $magic[1] -eq 0x7a -and $magic[2] -eq 0xbc -and $magic[3] -eq 0xaf)
Check "产物是合法 7z（魔数正确）" $is7z (($magic | ForEach-Object { $_.ToString('x2') }) -join " ")

Section "3. 压缩 ZIP + 分卷"
$job2 = NewJob "compress"
$null = Upload $job2 @(@{ Rel = "blob.bin"; Path = "$Work\src\blob.bin"; Name = "blob.bin" })
$null = Api "POST" "/api/jobs/$job2/run" @{ format = "zip"; level = 1; name = "zipped" }
$d2 = WaitJob $job2
Check "ZIP 压缩完成" ($d2.status -eq "done") $d2.message
Check "产物名为 zipped.zip" (($d2.files | Select-Object -First 1).name -eq "zipped.zip")

$job3 = NewJob "compress"
$null = Upload $job3 @(@{ Rel = "blob.bin"; Path = "$Work\src\blob.bin"; Name = "blob.bin" })
$null = Api "POST" "/api/jobs/$job3/run" @{ format = "7z"; level = 1; name = "split"; volume_size = "512k" }
$d3 = WaitJob $job3
Check "分卷压缩完成" ($d3.status -eq "done") $d3.message
Check "产生多个分卷" (($d3.files).Count -ge 2) (($d3.files | ForEach-Object { $_.name }) -join ", ")

Section "4. 加密压缩（AES-256）"
$job4 = NewJob "compress"
$null = Upload $job4 @(@{ Rel = "readme.txt"; Path = "$Work\src\readme.txt"; Name = "readme.txt" })
$null = Api "POST" "/api/jobs/$job4/run" @{ format = "7z"; level = 5; name = "locked"; password = "s3cr3t-密码"; encrypt_names = $true }
$d4 = WaitJob $job4
Check "加密压缩完成" ($d4.status -eq "done") $d4.message

# ---------------------------------------------------------------- 5. 解压流程

Section "5. 解压：预览 + 选择性解压"
$job5 = NewJob "extract"
$up5 = Upload $job5 @(@{ Rel = "bundle.7z"; Path = "$Work\bundle.7z"; Name = "bundle.7z" })
Check "上传压缩包成功" ($up5.Status -eq 200) "HTTP $($up5.Status) $($up5.Text)"

$prev = Api "POST" "/api/jobs/$job5/preview" @{}
# 三个文件 + 两级目录（docs、docs/深空）共 5 个条目
Check "预览返回条目" ($prev.entries.Count -eq 5) ("条目数 " + $prev.entries.Count)
$names = ($prev.entries | ForEach-Object { $_.path }) -join ","
Check "中文条目名未损坏" ($names -match "资料.txt") $names
Check "识别出目录" (($prev.entries | Where-Object { $_.is_dir }).Count -eq 2) (($prev.entries | Where-Object { $_.is_dir } | ForEach-Object { $_.path }) -join ", ")
Check "文件条目为 3 个" (($prev.entries | Where-Object { -not $_.is_dir }).Count -eq 3)
Check "统计了原始总大小" ($prev.total_size -gt 2000000) ("total_size=" + $prev.total_size)

$run5 = ApiRaw "POST" "/api/jobs/$job5/run" @{ preserve_paths = $true; selected = @("docs/readme.txt") }
Check "选择性解压被接受" ($run5.Status -eq 202) "HTTP $($run5.Status) $($run5.Text)"
$d5 = WaitJob $job5
Check "解压完成" ($d5.status -eq "done") $d5.message
Check "只解出被选中的文件" (($d5.files).Count -eq 1) (($d5.files | ForEach-Object { $_.name }) -join ", ")
Check "保留了目录结构" ((($d5.files | Select-Object -First 1).name) -eq "docs/readme.txt")
$zipOut = Save "/api/jobs/$job5/download-all" "$Work\extracted.zip"
Check "多文件结果打包为 ZIP" ($zipOut.Length -gt 0)

Section "6. 解压：全部解压"
$job6 = NewJob "extract"
$null = Upload $job6 @(@{ Rel = "bundle.7z"; Path = "$Work\bundle.7z"; Name = "bundle.7z" })
$null = Api "POST" "/api/jobs/$job6/run" @{ preserve_paths = $true }
$d6 = WaitJob $job6
Check "全部解压完成" ($d6.status -eq "done") $d6.message
Check "解出全部 3 个文件" (($d6.files).Count -eq 3) (($d6.files | ForEach-Object { $_.name }) -join ", ")

Section "7. 解压加密包（7z，含文件名加密）"
$locked = Save "/api/jobs/$job4/download-all" "$Work\locked.7z"
$job7 = NewJob "extract"
$null = Upload $job7 @(@{ Rel = "locked.7z"; Path = "$Work\locked.7z"; Name = "locked.7z" })

$bad = ApiRaw "POST" "/api/jobs/$job7/run" @{}
Check "无密码解压被拒绝" ($bad.Status -eq 400) "HTTP $($bad.Status)"
Check "错误标记为需要密码" ($bad.Body.password_required -eq $true) $bad.Text

$wrong = ApiRaw "POST" "/api/jobs/$job7/run" @{ password = "wrong-password" }
Check "错误密码被同步拒绝（非 202）" ($wrong.Status -eq 400) "HTTP $($wrong.Status)"
Check "错误密码标记为需要密码" ($wrong.Body.password_required -eq $true) $wrong.Text
$still = Api "GET" "/api/jobs/$job7"
Check "被拒后任务未启动" ($still.status -eq "pending") ("status=" + $still.status)

$ok = ApiRaw "POST" "/api/jobs/$job7/run" @{ password = "s3cr3t-密码" }
Check "正确密码可解压" ($ok.Status -eq 202) "HTTP $($ok.Status) $($ok.Text)"
$d7 = WaitJob $job7
Check "加密包解压完成" ($d7.status -eq "done") $d7.message
Check "解密后内容正确" (($d7.files | ForEach-Object { $_.name }) -contains "readme.txt")

Section "7b. 解压加密包（ZIP + AES-256，无头部加密）"
$jobZ = NewJob "compress"
$null = Upload $jobZ @(@{ Rel = "readme.txt"; Path = "$Work\src\readme.txt"; Name = "readme.txt" })
$null = Api "POST" "/api/jobs/$jobZ/run" @{ format = "zip"; level = 5; name = "zlocked"; password = "zip-pass-123" }
$dZ = WaitJob $jobZ
Check "ZIP 加密压缩完成" ($dZ.status -eq "done") $dZ.message
$zlocked = Save "/api/jobs/$jobZ/download-all" "$Work\zlocked.zip"
Check "拿到加密 ZIP" ($zlocked.Length -gt 0)

$jobZ2 = NewJob "extract"
$null = Upload $jobZ2 @(@{ Rel = "zlocked.zip"; Path = "$Work\zlocked.zip"; Name = "zlocked.zip" })
$zWrong = ApiRaw "POST" "/api/jobs/$jobZ2/run" @{ password = "nope" }
Check "ZIP 错误密码被同步拒绝" ($zWrong.Status -eq 400) "HTTP $($zWrong.Status)"
Check "ZIP 错误密码标记为 password_required" ($zWrong.Body.password_required -eq $true) $zWrong.Text
$zOk = ApiRaw "POST" "/api/jobs/$jobZ2/run" @{ password = "zip-pass-123" }
Check "ZIP 正确密码可解压" ($zOk.Status -eq 202) "HTTP $($zOk.Status) $($zOk.Text)"
$dZ2 = WaitJob $jobZ2
Check "加密 ZIP 解压完成" ($dZ2.status -eq "done") $dZ2.message
Check "失败重试后结果不含残留" (($dZ2.files).Count -eq 1) (($dZ2.files | ForEach-Object { $_.name }) -join ", ")

# -------------------------------------------------------------- 8. 安全边界

Section "8. 安全边界"
$t = ApiRaw "GET" "/api/jobs/$job6/download?file=../../etc/passwd"
Check "下载路径穿越被拒绝" ($t.Status -ne 200) "HTTP $($t.Status)"
$t2 = ApiRaw "GET" "/api/jobs/$job6/download?file=/etc/passwd"
Check "下载绝对路径被拒绝" ($t2.Status -ne 200) "HTTP $($t2.Status)"
$t3 = ApiRaw "GET" "/api/jobs/doesnotexist000000000000000000"
Check "未知任务返回 404" ($t3.Status -eq 404) "HTTP $($t3.Status)"
$t4 = ApiRaw "POST" "/api/jobs?kind=bogus"
Check "非法 kind 被拒绝" ($t4.Status -eq 400) "HTTP $($t4.Status)"

$index = Invoke-WebRequest -Uri "$Base/" -TimeoutSec 30 -UseBasicParsing
Check "首页可访问" ($index.StatusCode -eq 200)
Check "首页含 YC-7ZIP 标记" ($index.Content -match "YC-7ZIP")
$css = Invoke-WebRequest -Uri "$Base/assets/style.css" -TimeoutSec 30 -UseBasicParsing
Check "样式表可访问" ($css.StatusCode -eq 200)
$js = Invoke-WebRequest -Uri "$Base/assets/app.js" -TimeoutSec 30 -UseBasicParsing
Check "脚本可访问" ($js.StatusCode -eq 200)

Section "9. 清理"
foreach ($id in @($job, $job2, $job3, $job4, $job5, $job6, $job7, $jobZ, $jobZ2)) {
  try { $null = Api "DELETE" "/api/jobs/$id" } catch { }
}
$gone = ApiRaw "GET" "/api/jobs/$job"
Check "任务删除后返回 404" ($gone.Status -eq 404) "HTTP $($gone.Status)"

Write-Host ""
Write-Host "==============================================" -ForegroundColor Yellow
if ($script:Fail -eq 0) {
  Write-Host ("全部通过：$($script:Pass) 项") -ForegroundColor Green
  exit 0
} else {
  Write-Host ("通过 $($script:Pass) 项，失败 $($script:Fail) 项") -ForegroundColor Red
  exit 1
}

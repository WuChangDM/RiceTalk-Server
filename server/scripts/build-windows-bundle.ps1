<#
.SYNOPSIS
构建 RidgeRiceTalk「Windows 服务端一键部署包」（目标机无需 Go / Node / npm）。

.DESCRIPTION
产物布局（与 server/internal/config/paths.go 的 `<root>/server/webhost/dist` 判据一致）：

  ridgetalk-win-server-<ver>/
    server/ridgericetalk.exe          # GOOS=windows GOARCH=amd64 交叉编译
    server/webhost/dist/**            # 必须含 admin/index.html
    migrations/**
    console/{ServerConsole.exe,*.dll,server-console.json.template}
    scripts/deploy-windows.ps1
    start-server.bat                  # 解压后双击 → 管理员跑 -FromBundle
    README-deploy.md
    MANIFEST.sha256                   # <sha256>  <size>  <path>
    SHA256SUMS                        # <sha256>  <path>（sha256sum -c 可直接用）

LiveKit：Windows 版二进制不入库（>50MB），打包前用 server\scripts\download-livekit-win.ps1 下载并校验
SHA256 到 livekit\bin\windows-amd64\；存在则自动打入包内 livekit\livekit-server.exe（语音开箱可用），
不存在则包内不含 LiveKit，README / MANIFEST 写明「需另放或外接」，部署脚本缺失只 WARN。

用法（在仓库任意目录）：
  powershell -NoProfile -ExecutionPolicy Bypass -File server\scripts\build-windows-bundle.ps1
  powershell ... build-windows-bundle.ps1 -DryRun
  powershell ... build-windows-bundle.ps1 -OutDir D:\out -SkipFrontend -SkipConsole

边界：只读仓库、只写 -OutDir；不碰发布仓库、不 push、不打 tag。
#>
param(
    [string]$Version = "",
    [string]$OutDir = "Z:\RIce\发布窗口\1.9.0-win",
    [switch]$SkipFrontend,
    [switch]$SkipConsole,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$CReset = "`e[0m"; $CRed = "`e[91m"; $CGreen = "`e[92m"
$CYellow = "`e[93m"; $CCyan = "`e[96m"; $CWhite = "`e[97m"; $CGray = "`e[90m"
function step($n, $msg) { Write-Host "`n$CCyan[$n]$CReset $msg" }
function ok($msg)   { Write-Host "  $CGreen[OK]$CReset $msg" }
function warn($msg) { Write-Host "  $CYellow[WARN]$CReset $msg" }
function fail($msg) { Write-Host "  $CRed[FAIL]$CReset $msg" }
function info($msg) { Write-Host "  $CGray[INFO]$CReset $msg" }

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$ServerDir = (Resolve-Path (Join-Path $ScriptDir "..")).Path
$RepoRoot  = (Resolve-Path (Join-Path $ServerDir "..")).Path

# ------------------------------------------------------------
# 版本与 revision
# ------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($Version)) {
    $versionGo = Join-Path $ServerDir "core\version\version.go"
    if (-not (Test-Path $versionGo)) { fail "找不到 $versionGo"; exit 1 }
    $m = Select-String -Path $versionGo -Pattern '^\s*var\s+Server\s*=\s*"([^"]+)"' | Select-Object -First 1
    if (-not $m) { fail "无法从 version.go 解析 var Server"; exit 1 }
    $Version = $m.Matches[0].Groups[1].Value
}
$GitSha = ""
$GitDirty = $false
try {
    $GitSha = (& git -C $RepoRoot rev-parse --short HEAD 2>$null | Select-Object -First 1)
    if ($null -ne $GitSha) { $GitSha = "$GitSha".Trim() }
    $porcelain = (& git -C $RepoRoot status --porcelain 2>$null)
    $GitDirty = -not [string]::IsNullOrWhiteSpace(($porcelain -join ""))
} catch { $GitSha = "" }
if ([string]::IsNullOrWhiteSpace($GitSha)) {
    warn "取不到 git HEAD，revision 注入留空（可追溯性下降）"
    $GitSha = "unknown"
}
if ($GitDirty) { warn "工作区有未提交改动：包内 exe 的 revision 与源码可能不完全一致（MANIFEST 会记 WORKTREE=modified）" }

$PkgName   = "ridgetalk-win-server-$Version"
# 暂存目录放本机临时目录：网络盘（Z: 映射 UNC）上深路径/并发写会偶发
# "未能找到路径的一部分"，本地 NTFS 稳定且不受 UNC 长路径影响；打包完成即清理。
$StageRoot = Join-Path $env:TEMP ("rt-win-stage-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
$StageDir  = Join-Path $StageRoot $PkgName
$ZipPath   = Join-Path $OutDir "$PkgName.zip"

$DistSrc      = Join-Path $ServerDir "webhost\dist"
$ConsoleDist  = Join-Path $RepoRoot "tools\server-console\dist"
$Migrations   = Join-Path $RepoRoot "migrations"
$LiveKitWin   = Join-Path $RepoRoot "livekit\bin\windows-amd64\livekit-server.exe"

# ------------------------------------------------------------
# DRY RUN
# ------------------------------------------------------------
if ($DryRun) {
    Write-Host ""
    Write-Host "$CYellow=== DRY RUN（只打印计划，不写任何文件）===$CReset"
    Write-Host "  版本        : $Version"
    Write-Host "  revision    : $GitSha$(if ($GitDirty) { ' (dirty)' })"
    Write-Host "  输出目录    : $OutDir"
    Write-Host "  产物        : $ZipPath"
    Write-Host "  暂存        : $StageDir"
    Write-Host "  交叉编译    : GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags `"-s -w -X ridgericetalk/core/version.Server=$Version -X ridgericetalk/core/version.Commit=$GitSha`" ./cmd/server"
    Write-Host "  前端        : $(if ($SkipFrontend) { '跳过构建，复用' } else { '尝试 npm 构建 web/admin，失败则复用' }) $DistSrc"
    Write-Host "  控制台      : $(if ($SkipConsole) { '跳过（包内将没有 console/）' } else { $ConsoleDist })"
    Write-Host "  migrations  : $Migrations"
    Write-Host "  deploy 脚本 : $(Join-Path $ScriptDir 'deploy-windows.ps1') → scripts\deploy-windows.ps1"
    Write-Host "  LiveKit     : $(if (Test-Path $LiveKitWin) { "包含 $LiveKitWin" } else { '无 Windows 版二进制 → 包内不含，README/MANIFEST 写「需另放或外接」' })"
    Write-Host "  体积门禁    : zip < 100MB（50-100MB 提示）"
    $cc = @("server/ridgericetalk.exe", "server/webhost/dist/admin/index.html", "migrations/*.sql", "console/ServerConsole.exe", "scripts/deploy-windows.ps1", "start-server.bat", "README-deploy.md", "MANIFEST.sha256", "SHA256SUMS")
    Write-Host "  包内结构    : $($cc -join ', ')"
    exit 0
}

# ------------------------------------------------------------
# 暂存目录
# ------------------------------------------------------------
function Reset-Dir([string]$p) {
    if (Test-Path $p) { Remove-Item -Path $p -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $p | Out-Null
}
if (-not (Test-Path $OutDir)) { New-Item -ItemType Directory -Force -Path $OutDir | Out-Null }
# 逐项递归复制：目标已存在时 Copy-Item -Recurse 在部分 Win/UNC 场景下会丢父目录，这里自建目录层级
# 本机构建根目录是 SMB 映射盘（Z:）：刚 New-Item 出的目录对同进程偶发「不可见」，
# 导致 Copy-Item 报 DirectoryNotFoundException（随机、重跑即过）。统一走带重试的复制/建目录。
function Ensure-Dir([string]$p) {
    for ($i = 0; $i -lt 6; $i++) {
        if (Test-Path -LiteralPath $p) { return }
        New-Item -ItemType Directory -Force -Path $p | Out-Null
        Start-Sleep -Milliseconds 120
    }
}
function Copy-WithRetry([string]$src, [string]$dest) {
    $parent = Split-Path -Path $dest -Parent
    for ($i = 1; $i -le 6; $i++) {
        try {
            if ($parent) { Ensure-Dir $parent }
            Copy-Item -Path $src -Destination $dest -Force -ErrorAction Stop
            return
        } catch {
            if ($i -eq 6) { throw }
            Start-Sleep -Milliseconds (150 * $i)
        }
    }
}
function Copy-Tree([string]$src, [string]$dest) {
    if (-not (Test-Path $dest)) { New-Item -ItemType Directory -Force -Path $dest | Out-Null }
    foreach ($item in (Get-ChildItem -Path $src -Force)) {
        $target = Join-Path $dest $item.Name
        if ($item.PSIsContainer) {
            Copy-Tree $item.FullName $target
        } else {
            Copy-WithRetry $item.FullName $target
        }
    }
}
Reset-Dir $StageRoot
New-Item -ItemType Directory -Force -Path $StageDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $StageDir "server\webhost\dist") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $StageDir "server\webhost\dist\admin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $StageDir "migrations") | Out-Null

$created = New-Object System.Collections.Generic.List[string]

# ------------------------------------------------------------
step "1/7 前端产物（server/webhost/dist）"
if (-not $SkipFrontend) {
    $webAdmin = Join-Path $RepoRoot "web\admin"
    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if ($npm -and (Test-Path (Join-Path $webAdmin "package.json"))) {
        info "尝试构建 web/admin（npm run build）…"
        Push-Location $webAdmin
        try { & npm run build 2>&1 | Select-Object -Last 8 | ForEach-Object { Write-Host "    $_" } } catch { warn "npm 构建失败：$($_.Exception.Message)" }
        finally { Pop-Location }
    } else {
        warn "未找到 npm 或 web/admin：跳过前端构建，复用仓库内已有 dist（-SkipFrontend 效果相同）"
    }
} else { info "已指定 -SkipFrontend，复用仓库已有 dist" }
if (-not (Test-Path (Join-Path $DistSrc "admin\index.html"))) {
    fail "缺少 $DistSrc\admin\index.html —— 前端产物不可用，先构建 admin 或去掉 -SkipFrontend"
    exit 1
}
Copy-Tree $DistSrc (Join-Path $StageDir "server\webhost\dist")
$distFiles = (Get-ChildItem (Join-Path $StageDir "server\webhost\dist") -Recurse -File).Count
ok "已复制前端产物：$distFiles 个文件（含 admin/index.html）"

# ------------------------------------------------------------
step "2/7 交叉编译 server（windows/amd64，静态，-s -w）"
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    $cand = Join-Path $env:TEMP "go-tc\go\bin\go.exe"
    if (Test-Path $cand) { $goCmd = Get-Command $cand } 
}
if (-not $goCmd) { fail "找不到 go 工具链（PATH 或 %TEMP%\go-tc\go\bin\go.exe）"; exit 1 }
$ldflags = "-s -w -X ridgericetalk/core/version.Server=$Version -X ridgericetalk/core/version.Commit=$GitSha"
$exeOut = Join-Path $StageDir "server\ridgericetalk.exe"
$env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
Push-Location $ServerDir
try {
    & $goCmd.Source build -trimpath -ldflags $ldflags -o $exeOut ./cmd/server
    if ($LASTEXITCODE -ne 0) { fail "go build 失败（exit $LASTEXITCODE）"; exit 1 }
} finally { Pop-Location }
$exeLen = (Get-Item $exeOut).Length
$fs = [System.IO.File]::OpenRead($exeOut)
try { $b2 = New-Object byte[] 2; $null = $fs.Read($b2, 0, 2) } finally { $fs.Close() }
if (-not ($b2[0] -eq 0x4D -and $b2[1] -eq 0x5A)) { fail "产物不是 PE（缺 MZ 头）：$exeOut"; exit 1 }
ok "已编译 ridgericetalk.exe：$exeLen 字节，PE(MZ) 校验通过，revision=$GitSha"
$created.Add($exeOut)

# ------------------------------------------------------------
step "3/7 桌面控制台（console/）"
$consoleOutDir = Join-Path $StageDir "console"
if ($SkipConsole) {
    warn "已指定 -SkipConsole：包内不含 console/（目标机将退回浏览器访问 /admin/）"
} else {
    if (-not (Test-Path (Join-Path $ConsoleDist "ServerConsole.exe"))) {
        $buildScript = Join-Path $RepoRoot "tools\server-console\build.ps1"
        info "未找到已构建控制台，调用 $buildScript …"
        if (Test-Path $buildScript) {
            & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $buildScript | Select-Object -Last 6 | ForEach-Object { Write-Host "    $_" }
        }
    }
    if (-not (Test-Path (Join-Path $ConsoleDist "ServerConsole.exe"))) {
        fail "控制台产物缺失：$ConsoleDist\ServerConsole.exe（去掉 -SkipConsole 并先跑 tools\server-console\build.ps1）"
        exit 1
    }
    New-Item -ItemType Directory -Force -Path $consoleOutDir | Out-Null
    Copy-Tree $ConsoleDist $consoleOutDir
    # 只保留模板；实际 server-console.json 由 deploy-windows.ps1 按部署目录/端口生成
    $cfgJson = Join-Path $consoleOutDir "server-console.json"
    if (Test-Path $cfgJson) { Move-Item -Path $cfgJson -Destination (Join-Path $consoleOutDir "server-console.json.template") -Force }
    Copy-WithRetry (Join-Path $RepoRoot "tools\server-console\server-console.json") (Join-Path $consoleOutDir "server-console.json.template")
    $cFiles = (Get-ChildItem $consoleOutDir -File).Count
    ok "已复制桌面控制台：$cFiles 个文件（ServerConsole.exe + WebView2 dll + json 模板）"
}

# ------------------------------------------------------------
step "4/7 migrations 与部署脚本"
Copy-Tree $Migrations (Join-Path $StageDir "migrations")
$migCount = (Get-ChildItem (Join-Path $StageDir "migrations") -Filter "*.sql" -File).Count
if ($migCount -lt 1) { fail "migrations 复制为空"; exit 1 }
ok "已复制迁移 SQL：$migCount 个文件"

$scriptsOut = Join-Path $StageDir "scripts"
New-Item -ItemType Directory -Force -Path $scriptsOut | Out-Null
Copy-WithRetry (Join-Path $ScriptDir "deploy-windows.ps1") (Join-Path $scriptsOut "deploy-windows.ps1")
ok "已复制 scripts\deploy-windows.ps1"

$batSrc = Join-Path $ScriptDir "start-server.bat"
if (-not (Test-Path $batSrc)) { fail "缺少 $batSrc（包入口脚本）"; exit 1 }
Copy-WithRetry $batSrc (Join-Path $StageDir "start-server.bat")
ok "已复制 start-server.bat"

# 可选：Windows 版 LiveKit（存在才打进去；由 server\scripts\download-livekit-win.ps1 提前下载）
if (Test-Path $LiveKitWin) {
    $lkOutDir = Join-Path $StageDir "livekit"
    New-Item -ItemType Directory -Force -Path $lkOutDir | Out-Null
    Copy-WithRetry $LiveKitWin (Join-Path $lkOutDir "livekit-server.exe")
    $lkLen = (Get-Item (Join-Path $lkOutDir "livekit-server.exe")).Length
    if ($lkLen -lt 50MB) { fail "livekit-server.exe 大小异常（$lkLen 字节），疑似残缺，停止打包"; exit 1 }
    ok ("已内置 livekit-server.exe：{0:N0} 字节（语音开箱可用）" -f $lkLen)
} else {
    warn "未发现 livekit\bin\windows-amd64\livekit-server.exe：本包不含语音（可用 server\scripts\download-livekit-win.ps1 下载后重打）"
}

# README-deploy.md：模板变量替换
$readmeTpl = Join-Path $ScriptDir "WIN-BUNDLE-README.md"
if (-not (Test-Path $readmeTpl)) { fail "缺少 $readmeTpl"; exit 1 }
$livekitNote = if (Test-Path $LiveKitWin) { "已内置（语音开箱可用，需放通/映射 TCP 7880/7881 与 UDP 7882）" } else { "未包含（需另放 livekit\livekit-server.exe 或用外部 LiveKit）" }
$readme = Get-Content -Path $readmeTpl -Raw -Encoding UTF8
$readme = $readme.Replace("{{VERSION}}", $Version).Replace("{{REVISION}}", $GitSha).Replace("{{BUILD_DATE}}", (Get-Date -Format "yyyy-MM-dd")).Replace("{{LIVEKIT}}", $livekitNote)
[System.IO.File]::WriteAllText((Join-Path $StageDir "README-deploy.md"), $readme, (New-Object System.Text.UTF8Encoding($false)))
ok "已生成 README-deploy.md（LiveKit: $livekitNote）"

# ------------------------------------------------------------
step "5/7 关键内容自检（fail-closed）"
$required = @(
    "server\ridgericetalk.exe",
    "server\webhost\dist\admin\index.html",
    "migrations",
    "scripts\deploy-windows.ps1",
    "start-server.bat",
    "README-deploy.md"
)
if (-not $SkipConsole) { $required += "console\ServerConsole.exe" }
if (Test-Path $LiveKitWin) { $required += "livekit\livekit-server.exe" }
$missing = @()
foreach ($rel in $required) {
    if (-not (Test-Path (Join-Path $StageDir $rel))) { $missing += $rel }
}
if ($missing.Count -gt 0) { fail "包内关键内容缺失：$($missing -join ', ')"; exit 1 }
$vcCheck = & $goCmd.Source version -m $exeOut 2>$null
$vcText = ($vcCheck -join "`n")
if ($vcText -notmatch "GOOS=windows") { warn "go version -m 未显示 GOOS=windows（旧版 go 可能不写 build info）" }
ok "关键内容齐全（$($required.Count) 项）"

# ------------------------------------------------------------
step "6/7 生成 MANIFEST.sha256 / SHA256SUMS"
function Get-RelFiles([string]$root) {
    Get-ChildItem -Path $root -Recurse -File | Sort-Object FullName | ForEach-Object {
        $_.FullName.Substring($root.Length).TrimStart('\', '/') -replace '\\', '/'
    }
}
$manifestLines = New-Object System.Collections.Generic.List[string]
$sumsLines = New-Object System.Collections.Generic.List[string]
$rootLen = $StageDir.Length
foreach ($rel in (Get-RelFiles $StageDir)) {
    $full = Join-Path $StageDir ($rel -replace '/', '\')
    $h = (Get-FileHash -Path $full -Algorithm SHA256).Hash.ToLower()
    $len = (Get-Item $full).Length
    $manifestLines.Add("$h  $len  $rel")
    $sumsLines.Add("$h  $rel")
}
$worktree = if ($GitDirty) { "modified" } else { "clean" }
$manifestHeader = @(
    "# RidgeRiceTalk Windows 服务端一键部署包",
    "PACKAGE=$PkgName",
    "VERSION=$Version",
    "REVISION=$GitSha",
    "BINARY_COMMIT=$GitSha",
    "BINARY_NOTE=二进制构建自该提交（-X ridgericetalk/core/version.Commit 注入）；REVISION 为打包时 HEAD",
    "WORKTREE=$worktree",
    "BUILD_DATE=$(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK')",
    "PLATFORM=windows/amd64",
    "LIVEKIT=$(if (Test-Path $LiveKitWin) { 'included' } else { 'not-included (语音需另放 livekit\livekit-server.exe 或用外部 LiveKit)' })",
    "STORAGE_POLICY=preserve",
    "# 格式：<sha256>  <字节数>  <相对路径>",
    ""
)
[System.IO.File]::WriteAllLines((Join-Path $StageDir "MANIFEST.sha256"), ($manifestHeader + $manifestLines), (New-Object System.Text.UTF8Encoding($false)))
[System.IO.File]::WriteAllLines((Join-Path $StageDir "SHA256SUMS"), $sumsLines, (New-Object System.Text.UTF8Encoding($false)))
ok "MANIFEST.sha256：$($manifestLines.Count) 条；SHA256SUMS：$($sumsLines.Count) 条"

# ------------------------------------------------------------
step "7/7 打包 zip"
Add-Type -AssemblyName System.IO.Compression.FileSystem
if (Test-Path $ZipPath) { Remove-Item $ZipPath -Force }
[System.IO.Compression.ZipFile]::CreateFromDirectory($StageDir, $ZipPath, [System.IO.Compression.CompressionLevel]::Optimal, $true)
$zipLen = (Get-Item $ZipPath).Length
$zipSha = (Get-FileHash -Path $ZipPath -Algorithm SHA256).Hash.ToLower()
$zip = [System.IO.Compression.ZipFile]::OpenRead($ZipPath)
try { $entryCount = $zip.Entries.Count } finally { $zip.Dispose() }
[System.IO.File]::WriteAllText("$ZipPath.sha256", "$zipSha  $(Split-Path -Leaf $ZipPath)`r`n", (New-Object System.Text.UTF8Encoding($false)))

$sizeMB = [math]::Round($zipLen / 1MB, 2)
if ($zipLen -gt 100MB) { fail "包体 $sizeMB MB 超过 100MB 硬约束"; exit 1 }
elseif ($zipLen -gt 50MB) { warn "包体 $sizeMB MB（50-100MB，GitHub 会提示但可用）" }
else { ok "包体 $sizeMB MB（<50MB）" }

Write-Host ""
Write-Host "$CGreen=== 构建完成 ===$CReset"
Write-Host "  zip        : $ZipPath"
Write-Host "  字节数     : $zipLen"
Write-Host "  sha256     : $zipSha"
Write-Host "  包内条目   : $entryCount"
Write-Host "  版本/修订  : $Version / $GitSha$(if ($GitDirty) { ' (dirty)' })"
Write-Host "  sha256 文件: $ZipPath.sha256"
Write-Host ""
Write-Host "  目标机用法：解压 → 右键 start-server.bat「以管理员身份运行」"
Write-Host ""

# 清理本地暂存（zip 已生成并校验）
try { Remove-Item -LiteralPath $StageRoot -Recurse -Force -ErrorAction SilentlyContinue } catch { }
exit 0

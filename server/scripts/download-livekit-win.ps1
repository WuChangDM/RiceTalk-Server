<#
.SYNOPSIS
    下载并校验 Windows 版 LiveKit 服务端二进制（供 Windows 一键部署包内置语音能力）。

.DESCRIPTION
    官方发布包只提供压缩包（zip），本脚本：
      1) 下载 livekit_<版本>_windows_amd64.zip（默认走 GitHub Release）；
      2) 校验 zip 与解包后 exe 的 SHA256（写死在脚本里，防篡改/半包）；
      3) 仅把 livekit-server.exe 放到 <仓库>/livekit/bin/windows-amd64/（该目录已被 .gitignore 忽略，不入库）；
      4) 运行 --version 复核版本号。

    下载失败时支持 HTTP(S)_PROXY 环境变量（如本机 Clash：$env:HTTPS_PROXY="http://127.0.0.1:7890"）。

.PARAMETER Version
    LiveKit 版本，默认 1.13.11（与 Linux 内置版一致，见 livekit/bin/linux-amd64）。

.PARAMETER TargetDir
    输出目录，默认 <仓库根>/livekit/bin/windows-amd64。

.PARAMETER Force
    目标已存在且校验通过时也重新下载。

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File server\scripts\download-livekit-win.ps1

.EXAMPLE
    $env:HTTPS_PROXY = "http://127.0.0.1:7890"; .\download-livekit-win.ps1 -Force
#>
[CmdletBinding()]
param(
    [string]$Version = "1.13.11",
    [string]$TargetDir = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"

# 固定校验值（v1.13.11，2026-10-25 官方产物）
$ZipSha256 = "5d071ea43339f0140d24a66f0b2ff8a7bf6c4d0dceb19b30abca835670213667"
$ExeSha256 = "7c63b07e78121edab0c81554af97754cc7abbaac05313cdb91c81f51287e16c2"

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Warn2($msg){ Write-Host "[WARN] $msg" -ForegroundColor Yellow }

$scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot   = Split-Path -Parent (Split-Path -Parent $scriptRoot)   # server/scripts -> 仓库根
if (-not $TargetDir) { $TargetDir = Join-Path $repoRoot "livekit\bin\windows-amd64" }
$exePath = Join-Path $TargetDir "livekit-server.exe"

function Get-Sha256($path) { (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLower() }

# ---- 0. 已存在且校验通过则跳过 ----
if ((Test-Path -LiteralPath $exePath) -and -not $Force) {
    $sha = Get-Sha256 $exePath
    if ($sha -eq $ExeSha256) {
        Write-Ok "已存在且校验通过，跳过下载：$exePath ($('{0:N0}' -f (Get-Item $exePath).Length) 字节)"
        exit 0
    }
    Write-Warn2 "已存在但 SHA256 不匹配（实际 $sha），重新下载"
}

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

$zipName = "livekit_${Version}_windows_amd64.zip"
$url     = "https://github.com/livekit/livekit/releases/download/v$Version/$zipName"
$zipPath = Join-Path $env:TEMP $zipName

# ---- 1. 下载（curl 优先：支持续传；回退 Invoke-WebRequest） ----
Write-Step "下载 $url"
$downloaded = $false
if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
    & curl.exe -sSL --max-time 900 -C - -o $zipPath $url
    if ($LASTEXITCODE -eq 0) { $downloaded = $true }
} else {
    $oldProgress = $ProgressPreference
    $ProgressPreference = "SilentlyContinue"
    try { Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing -TimeoutSec 900; $downloaded = $true }
    catch { Write-Warn2 "Invoke-WebRequest 失败：$($_.Exception.Message)" }
    finally { $ProgressPreference = $oldProgress }
}
if (-not $downloaded) { throw "下载失败。若网络受限，请先设置 `$env:HTTPS_PROXY`（如 http://127.0.0.1:7890）后重试。" }

# ---- 2. 校验 zip ----
$zipSha = Get-Sha256 $zipPath
if ($zipSha -ne $ZipSha256) { throw "zip SHA256 不匹配：期望 $ZipSha256，实际 $zipSha（下载不完整或被篡改）" }
Write-Ok "zip 校验通过：$zipSha（$('{0:N0}' -f (Get-Item $zipPath).Length) 字节）"

# ---- 3. 解包并校验 exe ----
$extract = Join-Path $env:TEMP "livekit-win-extract-$Version"
if (Test-Path -LiteralPath $extract) { Remove-Item -LiteralPath $extract -Recurse -Force }
Expand-Archive -LiteralPath $zipPath -DestinationPath $extract -Force
$srcExe = Join-Path $extract "livekit-server.exe"
if (-not (Test-Path -LiteralPath $srcExe)) { throw "压缩包内未找到 livekit-server.exe" }
$exeSha = Get-Sha256 $srcExe
if ($exeSha -ne $ExeSha256) { throw "livekit-server.exe SHA256 不匹配：期望 $ExeSha256，实际 $exeSha" }
Write-Ok "exe 校验通过：$exeSha"

Copy-Item -LiteralPath $srcExe -Destination $exePath -Force
Remove-Item -LiteralPath $extract -Recurse -Force
if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }

# ---- 4. 版本复核 ----
$ver = & $exePath --version 2>&1
Write-Ok "已落盘：$exePath"
Write-Ok "版本输出：$($ver -join ' ')"
if (($ver -join ' ') -notmatch [regex]::Escape($Version)) { throw "版本输出与期望 $Version 不符" }

Write-Host ""
Write-Host "下一步：" -ForegroundColor Cyan
Write-Host "  .\server\scripts\build-windows-bundle.ps1     # 打包时会自动带上 livekit-server.exe（MANIFEST: LIVEKIT=included）"
Write-Host "  .\server\scripts\deploy-windows.ps1 -Port 5000 -AdminPort 5001 ...   # 部署后语音开箱可用"

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    将 RidgeRiceTalk 局域网 HTTPS 自签名证书安装到 Windows 受信任的根证书颁发机构。
.DESCRIPTION
    配合 setup-lan-https.sh --copy-cert-to 复制出的 ridgericetalk.crt 使用。
    需要以管理员身份运行 PowerShell；若未提升，脚本会尝试自动以管理员重新启动自身。
.PARAMETER CertPath
    证书文件（.crt 或 .cer）的完整路径。默认会尝试从脚本所在目录查找 ridgericetalk.crt。
.EXAMPLE
    .\install-cert-windows.ps1 -CertPath C:\Users\Alice\Downloads\ridgericetalk.crt
.EXAMPLE
    .\install-cert-windows.ps1
#>
param(
    [string]$CertPath = ""
)

# ============================================================
# 自动提升管理员权限
# ============================================================
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "当前未以管理员身份运行，尝试自动提升权限..."
    $scriptPath = $MyInvocation.MyCommand.Path
    $arguments = "-NoProfile -ExecutionPolicy Bypass -File `"$scriptPath`""
    if ($CertPath) {
        $arguments += " -CertPath `"$CertPath`""
    }
    Start-Process PowerShell -Verb RunAs -ArgumentList $arguments
    exit
}

# ============================================================
# 解析证书路径
# ============================================================
if (-not $CertPath) {
    $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $candidates = @(
        Join-Path $scriptDir "ridgericetalk.crt"
        Join-Path (Get-Location) "ridgericetalk.crt"
        Join-Path $env:USERPROFILE "Downloads\ridgericetalk.crt"
    )
    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            $CertPath = $candidate
            break
        }
    }
}

if (-not (Test-Path $CertPath)) {
    Write-Error "证书文件不存在: $CertPath"
    Write-Host "请使用 setup-lan-https.sh --copy-cert-to 将证书复制到本地，或通过 -CertPath 指定路径。"
    exit 1
}

Write-Host "正在安装证书: $CertPath"

# ============================================================
# 安装证书
# ============================================================
try {
    $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($CertPath)
    $store = New-Object System.Security.Cryptography.X509Certificates.X509Store(
        [System.Security.Cryptography.X509Certificates.StoreName]::Root,
        [System.Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine
    )
    $store.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
    try {
        $store.Add($cert)
        Write-Host "证书已安装到本地计算机的受信任根证书颁发机构存储。" -ForegroundColor Green
        Write-Host "请在浏览器中访问 https://<服务器IP> 验证证书是否已受信任。"
    } finally {
        $store.Close()
    }
} catch {
    Write-Error "证书安装失败: $_"
    exit 1
}

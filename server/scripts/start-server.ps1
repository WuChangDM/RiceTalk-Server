# RidgeRiceTalk Server Start Script (Windows)
# One-click launcher with dependency checks, progress display, and access info
# ---------------------------------------------------------------
# Usage: .\scripts\start-server.ps1
#        Double-click start.bat

$ErrorActionPreference = "Stop"

# ================================================================
# Console helpers
# ================================================================
$CReset = "`e[0m"; $CRed = "`e[91m"; $CGreen = "`e[92m"
$CYellow = "`e[93m"; $CCyan = "`e[96m"; $CWhite = "`e[97m"
$CGray = "`e[90m"; $CBlue = "`e[94m"; $CMAGENTA = "`e[95m"

function stage($n, $msg) { Write-Host "`n$CBlue[$n/5]$CReset $CCyan$msg$CReset" }
function ok($msg)      { Write-Host "  $CGreen[OK]$CReset $msg" }
function warn($msg)    { Write-Host "  $CYellow[WARN]$CReset $msg" }
function fail($msg)    { Write-Host "  $CRed[FAIL]$CReset $msg" }
function info($msg)    { Write-Host "  $CGray[INFO]$CReset $msg" }
function banner($msg) {
    Write-Host "`n$CMAGENTA========================================$CReset"
    Write-Host "$CMAGENTA  $msg$CReset"
    Write-Host "$CMAGENTA========================================$CReset"
}

# ================================================================
# Resolve paths
# ================================================================
$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Definition
$ServerDir  = Resolve-Path "$ScriptDir\.."
$ProjectDir = Resolve-Path "$ServerDir\..\.."
$StorageDir = "$ServerDir\storage"
$LogsDir    = "$ServerDir\logs"
$EnvFile    = "$ServerDir\.env"
Set-Location $ServerDir

# ================================================================
# Stage 1/5: Dependency Checks
# ================================================================
stage 1 "Checking dependencies..."
$hasFatal = $false

# 1.1 Go
$go = Get-Command go -ErrorAction SilentlyContinue
if ($go) {
    $gv = (go version) -replace 'go version ',''
    ok "Go runtime: $gv"
} else {
    fail "Go runtime not found. RidgeRiceTalk requires Go 1.25+"
    info "Download: https://go.dev/dl/"
    $hasFatal = $true
}

# 1.2 FFmpeg
$ff = Get-Command ffmpeg -ErrorAction SilentlyContinue
if ($ff) {
    $fv = (ffmpeg -version 2>$null | Select-Object -First 1) -replace 'ffmpeg version ','' -split ' ' | Select-Object -First 1
    ok "FFmpeg: $fv"
} else {
    if ($env:RRT_EMBEDDED_DEPS -ne 'false') {
        info "FFmpeg not found in PATH; embedded deps enabled, server will download it automatically."
    } else {
        warn "FFmpeg not found. Music bot and TTS will be unavailable."
        info "Download: https://ffmpeg.org/download.html"
    }
}

# 1.3 Ports
$apiPort = if ($env:RRT_PORT) { [int]$env:RRT_PORT } else { 8080 }
$inUse = Get-NetTCPConnection -LocalPort $apiPort -ErrorAction SilentlyContinue
if ($inUse) {
    $proc = Get-Process -Id $inUse.OwningProcess -ErrorAction SilentlyContinue
    $procName = if ($proc) { $proc.ProcessName } else { "unknown" }
    fail "Port $apiPort is already in use by process: $procName (PID $($inUse.OwningProcess))"
    info "Set a different port: ``$env:RRT_PORT = <port>``"
    $hasFatal = $true
} else {
    ok "Port $apiPort is available"
}

$lkInUse = Get-NetTCPConnection -LocalPort 7880 -ErrorAction SilentlyContinue
if ($lkInUse) {
    warn "Port 7880 (LiveKit) is already in use. Voice may conflict."
} else {
    ok "Port 7880 (LiveKit) is available"
}

# 1.4 Storage writable
try {
    New-Item -ItemType Directory -Path $StorageDir -Force | Out-Null
    "$null" | Out-File "$StorageDir\.write_test" -Force
    Remove-Item "$StorageDir\.write_test" -Force
    ok "Storage directory writable"
} catch {
    fail "Storage directory not writable: $StorageDir"
    $hasFatal = $true
}

if ($hasFatal) {
    Write-Host "`n$CRed[ABORTED]$CReset Please fix the issues above and run again."
    exit 1
}

# ================================================================
# Stage 2/5: Environment Preparation
# ================================================================
stage 2 "Preparing environment..."

# Directories
New-Item -ItemType Directory -Path $StorageDir -Force | Out-Null
New-Item -ItemType Directory -Path $LogsDir    -Force | Out-Null
New-Item -ItemType Directory -Path "$ServerDir\webhost\dist" -Force | Out-Null
ok "Directories ready"

# Frontend dist
$voiceSrc = "$ProjectDir\web\voice\dist"
$adminSrc = "$ProjectDir\web\admin\dist"
if (Test-Path $voiceSrc) {
    New-Item -ItemType Directory -Path "$ServerDir\webhost\dist\voice" -Force | Out-Null
    Copy-Item "$voiceSrc\*" "$ServerDir\webhost\dist\voice\" -Recurse -Force
    ok "Voice frontend copied"
} else {
    warn "Voice frontend dist missing. Run: cd web/voice && npm run build"
}
if (Test-Path $adminSrc) {
    New-Item -ItemType Directory -Path "$ServerDir\webhost\dist\admin" -Force | Out-Null
    Copy-Item "$adminSrc\*" "$ServerDir\webhost\dist\admin\" -Recurse -Force
    ok "Admin frontend copied"
} else {
    warn "Admin frontend dist missing. Run: cd web/admin && npm run build"
}

# Secrets
$secretsChanged = $false
if (Test-Path $EnvFile) {
    Get-Content $EnvFile | ForEach-Object {
        if ($_ -match '^\s*([^#][^=]+)\s*=\s*(.*?)\s*$') {
            [Environment]::SetEnvironmentVariable($matches[1], $matches[2], "Process")
        }
    }
    ok "Environment loaded from .env"
}

if (!$env:RRT_JWT_SECRET -or $env:RRT_JWT_SECRET -match 'your-super-secret') {
    $env:RRT_JWT_SECRET = -join ((48..57)+(97..122) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
    $secretsChanged = $true
    ok "JWT secret generated"
}
if (!$env:RRT_CSRF_TOKEN_SECRET -or $env:RRT_CSRF_TOKEN_SECRET -match 'your-super-secret') {
    $env:RRT_CSRF_TOKEN_SECRET = -join ((48..57)+(97..122) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
    $secretsChanged = $true
    ok "CSRF secret generated"
}
if ($secretsChanged) {
    $lines = @()
    if (Test-Path $EnvFile) {
        $lines = Get-Content $EnvFile | Where-Object { $_ -notmatch '^RRT_JWT_SECRET=' -and $_ -notmatch '^RRT_CSRF_TOKEN_SECRET=' }
    }
    $lines += "RRT_JWT_SECRET=$env:RRT_JWT_SECRET"
    $lines += "RRT_CSRF_TOKEN_SECRET=$env:RRT_CSRF_TOKEN_SECRET"
    $lines | Set-Content $EnvFile -Encoding UTF8
    ok "Secrets saved to .env"
}

# Defaults
if (!$env:RRT_ENV)          { $env:RRT_ENV = 'development' }
if (!$env:RRT_PORT)         { $env:RRT_PORT = '8080' }
if (!$env:RRT_LOG_LEVEL)    { $env:RRT_LOG_LEVEL = 'info' }
if (!$env:RRT_DATABASE_URL) { $env:RRT_DATABASE_URL = "$StorageDir\ridgericetalk.db" }
if (!$env:RRT_DB_DRIVER)    { $env:RRT_DB_DRIVER = 'sqlite' }
if (!$env:RRT_LOCAL_DATA_PATH) { $env:RRT_LOCAL_DATA_PATH = $StorageDir }
if (!$env:RRT_PUBLIC_ADDRESS)  { $env:RRT_PUBLIC_ADDRESS = "http://localhost:$($env:RRT_PORT)" }
if (!$env:RRT_EMBEDDED_DEPS)   { $env:RRT_EMBEDDED_DEPS = 'true' }
if (!$env:RRT_THIRD_PARTY_DIR) { $env:RRT_THIRD_PARTY_DIR = "$ServerDir\third_party" }
if (!$env:RRT_MODELS_DIR)      { $env:RRT_MODELS_DIR = "$ServerDir\models" }

ok "Environment configured"
info "  Port: $($env:RRT_PORT) | DB: $($env:RRT_DB_DRIVER) | Env: $($env:RRT_ENV)"

# ================================================================
# Stage 3/5: Pre-Launch
# ================================================================
stage 3 "Pre-launch check..."
$isFirstRun = !(Test-Path $env:RRT_DATABASE_URL)
if ($isFirstRun) {
    warn "First run detected — database will be initialized"
} else {
    ok "Database exists"
}

# ================================================================
# Stage 3.5/5: Start LiveKit (if not already running)
# ================================================================
stage 3.5 "Starting LiveKit..."
$LIVEKIT_YAML = "$StorageDir\livekit.yaml"
$livekitProc = $null

$lkRunning = Get-NetTCPConnection -LocalPort 7880 -ErrorAction SilentlyContinue
if ($lkRunning) {
    ok "LiveKit already running on port 7880"
} else {
    # Try native binary first
    $lkBin = $null
    $candidates = @(
        "$ProjectDir\livekit\livekit-server.exe",
        "$ServerDir\..\livekit\livekit-server.exe"
    )
    foreach ($c in $candidates) {
        if (Test-Path $c) { $lkBin = $c; break }
    }
    if (-not $lkBin) {
        $lkCmd = Get-Command livekit-server -ErrorAction SilentlyContinue
        if ($lkCmd) { $lkBin = $lkCmd.Source }
    }

    if ($lkBin) {
        info "Starting LiveKit (native binary)..."
        $livekitProc = Start-Process -FilePath $lkBin `
            -ArgumentList "--config","`"$LIVEKIT_YAML`"","--dev" `
            -RedirectStandardOutput "$LogsDir\livekit.log" `
            -RedirectStandardError "$LogsDir\livekit.err.log" `
            -NoNewWindow -PassThru
        Start-Sleep -Seconds 2
        if ($livekitProc -and -not $livekitProc.HasExited) {
            ok "LiveKit started (PID $($livekitProc.Id))"
        } else {
            warn "LiveKit failed to start — voice features unavailable"
            $livekitProc = $null
        }
    } else {
        warn "LiveKit not found (no binary) — voice features unavailable"
        info "Install: download livekit-server.exe from https://github.com/livekit/livekit/releases"
    }
}

# ================================================================
# Stage 4/5: Start Server
# ================================================================
stage 4 "Starting RidgeRiceTalk server..."
info "Press Ctrl+C to stop the server"

# Choose binary or dev mode
$exePath = "$ServerDir\build\ridgericetalk.exe"
if (Test-Path $exePath) {
    $cmdLine = $exePath
    info "Mode: production binary (ridgericetalk.exe)"
} else {
    $cmdLine = "go run ./cmd/server"
    info "Mode: development (go run)"
}

# Start server, redirect output to temp log for parsing
$logFile = "$LogsDir\server_startup.log"
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = "powershell"
$psi.Arguments = "-NoProfile -Command `"$cmdLine 2>&1 | Tee-Object -FilePath '$logFile'`""
$psi.WorkingDirectory = $ServerDir
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true

$proc = [System.Diagnostics.Process]::Start($psi)

# Wait for server to become ready (poll health endpoint)
$ready = $false
$bootstrapToken = $null
$maxWait = 60
$waited = 0

while ($waited -lt $maxWait -and !$proc.HasExited) {
    Start-Sleep -Seconds 1
    $waited++

    # Check for bootstrap token file (secure, not in logs)
    $tokenFile = "$StorageDir\.bootstrap_token"
    if (Test-Path $tokenFile) {
        $bootstrapToken = Get-Content $tokenFile -Raw -ErrorAction SilentlyContinue
        $bootstrapToken = $bootstrapToken.Trim()
    }

    # Check health
    try {
        $resp = Invoke-RestMethod -Uri "http://localhost:$apiPort/api/health" -TimeoutSec 2 -ErrorAction Stop
        if ($resp.status -eq "ok") {
            $ready = $true
            break
        }
    } catch {
        # Not ready yet
    }

    if ($waited % 5 -eq 0) {
        info "Waiting for server to start... ($waited`s)"
    }
}

if ($proc.HasExited) {
    fail "Server process exited unexpectedly. Check logs: $logFile"
    if (Test-Path $logFile) { Get-Content $logFile -Tail 30 }
    exit 1
}

if (!$ready) {
    warn "Server did not respond to health check within ${maxWait}s. It may still be starting."
}

# ================================================================
# Stage 5/5: Service Ready — Display Access Info
# ================================================================
stage 5 "Service ready!"

# Collect IPs
$localIPs = @()
try {
    $adapters = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notmatch '^127\.' -and $_.IPAddress -notmatch '^169\.254\.' -and $_.PrefixOrigin -ne 'WellKnown' }
    foreach ($a in $adapters) { $localIPs += $a.IPAddress }
} catch {}
if ($localIPs.Count -eq 0) { $localIPs += 'localhost' }

$publicIP = $null
try { $publicIP = Invoke-RestMethod -Uri 'https://api.ipify.org' -TimeoutSec 5 } catch {}

banner "RidgeRiceTalk Server is Running"

Write-Host "  $CWhite Environment:$CReset  $($env:RRT_ENV)"
Write-Host "  $CWhite API Port:$CReset     $apiPort"
Write-Host "  $CWhite LiveKit:$CReset      ws://localhost:7880"
Write-Host ""

Write-Host "  $CBlue Local Access:$CReset"
foreach ($ip in $localIPs) {
    Write-Host "    Voice:   $CGreen http://$ip`:$apiPort$CReset"
    Write-Host "    Admin:   $CGreen http://$ip`:$apiPort/admin$CReset"
}

if ($publicIP) {
    Write-Host ""
    Write-Host "  $CBlue Public Access:$CReset"
    Write-Host "    Voice:   $CGreen http://$publicIP`:$apiPort$CReset"
    Write-Host "    Admin:   $CGreen http://$publicIP`:$apiPort/admin$CReset"
} else {
    Write-Host ""
    Write-Host "  $CGray(Public IP could not be detected)$CReset"
}

Write-Host ""
Write-Host "  $CBlue WebSocket:$CReset  $CGray ws://localhost:$apiPort/ws$CReset"
Write-Host "  $CBlue Metrics:$CReset    $CGray http://localhost:$apiPort/metrics$CReset"

if ($bootstrapToken) {
    Write-Host ""
    Write-Host "  $CYellow>>> FIRST RUN — Bootstrap Token <<<$CReset"
    Write-Host "  $CYellow Token: $CWhite$bootstrapToken$CReset"
    Write-Host "  $CYellow Use this token to create the admin account at:$CReset"
    Write-Host "  $CYellow http://localhost:$apiPort/admin$CReset"
    Write-Host "  $CGray(This token will not be shown again)$CReset"
}

Write-Host ""
Write-Host "  $CGray(Press Ctrl+C to stop the server)$CReset"
Write-Host ""

# Wait for the server process — Ctrl+C kills this script and the child
$proc.WaitForExit()

Write-Host ""
banner "Server Stopped"
Write-Host "  RidgeRiceTalk server has exited (code: $($proc.ExitCode))."
Write-Host ""

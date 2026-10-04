# RidgeRiceTalk 嵌入式依赖一键部署脚本（Windows）
# 不依赖 Windows Service，适合个人服务器、开发机、小型部署
# ---------------------------------------------------------------
# 用法（管理员 PowerShell）：
#   .\deploy-embedded.ps1 -PublicAddress "http://YOUR_IP:8080"
#
# 参数：
#   -PublicAddress  对外访问地址（必填）
#   -DeployDir      部署目录（默认 C:\ProgramData\RidgeRiceTalk）
#   -PortApi        API 端口（默认 8080）
#   -PortAdmin      Admin 端口（默认 9090）
#   -PortLkWs       LiveKit WebSocket 端口（默认 7880）
#   -PortLkTcp      LiveKit TCP 端口（默认 7881）
#   -PortLkUdp      LiveKit UDP 端口（默认 7882）
#   -SkipFrontend   跳过前端构建
#   -SkipBuild      跳过后端构建
#   -Force          强制覆盖 .env.production

param(
    [Parameter(Mandatory = $true)]
    [string]$PublicAddress,

    [string]$DeployDir = "C:\ProgramData\RidgeRiceTalk",

    [int]$PortApi = 8080,
    [int]$PortAdmin = 9090,
    [int]$PortLkWs = 7880,
    [int]$PortLkTcp = 7881,
    [int]$PortLkUdp = 7882,

    [switch]$SkipFrontend,
    [switch]$SkipBuild,
    [switch]$Force
)

$ErrorActionPreference = "Stop"

# ============================================================
# Console helpers
# ============================================================
$CReset = "`e[0m"; $CRed = "`e[91m"; $CGreen = "`e[92m"
$CYellow = "`e[93m"; $CCyan = "`e[96m"; $CWhite = "`e[97m"
$CGray = "`e[90m"; $CBlue = "`e[94m"; $CMAGENTA = "`e[95m"

function stage($n, $total, $msg) { Write-Host "`n$CBlue[$n/$total]$CReset $CCyan$msg$CReset" }
function ok($msg)      { Write-Host "  $CGreen[OK]$CReset $msg" }
function warn($msg)    { Write-Host "  $CYellow[WARN]$CReset $msg" }
function fail($msg)    { Write-Host "  $CRed[FAIL]$CReset $msg" }
function info($msg)    { Write-Host "  $CGray[INFO]$CReset $msg" }
function banner($msg) {
    Write-Host "`n$CMAGENTA========================================$CReset"
    Write-Host "$CMAGENTA  $msg$CReset"
    Write-Host "$CMAGENTA========================================$CReset"
}

# ============================================================
# Resolve paths
# ============================================================
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$ProjectDir = Resolve-Path "$ScriptDir\..\.."
$ServerDir = "$DeployDir\server"
$WebDir = "$DeployDir\web"
$StorageDir = "$ServerDir\storage"
$EnvFile = "$ServerDir\.env.production"

$TotalStages = 7
if ($SkipFrontend) { $TotalStages-- }
if ($SkipBuild)    { $TotalStages-- }

# ============================================================
# Stage 1: Dependency checks
# ============================================================
stage 1 $TotalStages "Checking dependencies and ports"
$hasFatal = $false

$go = Get-Command go -ErrorAction SilentlyContinue
if ($go) {
    $gv = (go version) -replace 'go version ',''
    ok "Go runtime: $gv"
} else {
    fail "Go runtime not found. Please install Go 1.25+ from https://go.dev/dl/"
    $hasFatal = $true
}

$node = Get-Command node -ErrorAction SilentlyContinue
if ($node) {
    ok "Node.js: $(node -v)"
} else {
    warn "Node.js not found. Netease API and frontend build will be unavailable."
    info "Install: https://nodejs.org/"
}

$npm = Get-Command npm -ErrorAction SilentlyContinue
if ($npm) {
    ok "npm: $(npm -v)"
} else {
    warn "npm not found. Netease API npm install will fail."
}

$git = Get-Command git -ErrorAction SilentlyContinue
if ($git) {
    ok "git: $(git --version)"
} else {
    warn "git not found. Cannot verify repository state."
}

function Test-PortAvailable($port, $name) {
    $conn = Get-NetTCPConnection -LocalPort $port -ErrorAction SilentlyContinue
    if ($conn) {
        $proc = Get-Process -Id $conn.OwningProcess -ErrorAction SilentlyContinue
        $procName = if ($proc) { $proc.ProcessName } else { "unknown" }
        fail "Port $port ($name) is already in use by $procName (PID $($conn.OwningProcess))"
        $script:hasFatal = $true
    } else {
        ok "Port $port ($name) is available"
    }
}

Test-PortAvailable $PortApi "API"
Test-PortAvailable $PortAdmin "Admin"
Test-PortAvailable $PortLkWs "LiveKit WebSocket"
Test-PortAvailable $PortLkTcp "LiveKit TCP"

if ($hasFatal) {
    Write-Host "`n$CRed[ABORTED]$CReset Please install missing dependencies or free occupied ports and rerun."
    exit 1
}

# ============================================================
# Stage 2: Prepare deployment directory
# ============================================================
stage 2 $TotalStages "Preparing deployment directory"

# Safety: refuse to delete well-known system paths
$systemPaths = @("C:\", "C:\Windows", "C:\Program Files", "C:\Program Files (x86)", "C:\Users", $env:SystemRoot)
if ($systemPaths -contains $DeployDir) {
    fail "Refusing to deploy to unsafe directory: $DeployDir"
    exit 1
}

if ($DeployDir -ne $ProjectDir) {
    info "Copying project to $DeployDir ..."
    if (Test-Path $DeployDir) { Remove-Item -Recurse -Force $DeployDir }
    Copy-Item -Recurse -Force $ProjectDir $DeployDir
    # Remove runtime directories that should not be copied
    @('.git', 'node_modules', 'third_party', 'models', 'storage', 'logs') | ForEach-Object {
        $p = "$DeployDir\$_"
        if (Test-Path $p) { Remove-Item -Recurse -Force $p }
    }
    Get-ChildItem "$DeployDir\server" -Filter ".env*" | Remove-Item -Force -ErrorAction SilentlyContinue
    ok "Project copied to $DeployDir"
} else {
    info "Deploy directory is the project directory; skipping copy"
}

New-Item -ItemType Directory -Path $StorageDir -Force | Out-Null
New-Item -ItemType Directory -Path "$ServerDir\logs" -Force | Out-Null
New-Item -ItemType Directory -Path "$ServerDir\webhost\dist" -Force | Out-Null
ok "Directories ready"

# ============================================================
# Stage 3: Build frontend
# ============================================================
if (-not $SkipFrontend) {
    stage 3 $TotalStages "Building frontend"

    if (-not $npm) {
        warn "npm not found, skipping frontend build"
    } else {
        function Build-Frontend($name) {
            $src = "$WebDir\$name"
            $dst = "$ServerDir\webhost\dist\$name"

            if (-not (Test-Path $src)) {
                warn "$name frontend source not found at $src"
                return
            }

            info "Building $name frontend..."
            Push-Location $src
            try {
                npm ci
                if ($LASTEXITCODE -ne 0) { throw "npm ci failed for $name" }
                npm run build
                if ($LASTEXITCODE -ne 0) { throw "npm run build failed for $name" }
            } finally {
                Pop-Location
            }

            New-Item -ItemType Directory -Path $dst -Force | Out-Null
            Copy-Item "$src\dist\*" $dst -Recurse -Force
            ok "$name frontend built and copied"
        }

        Build-Frontend "voice"
        Build-Frontend "admin"
    }
} else {
    info "Skipping frontend build as requested"
    if (Test-Path "$ProjectDir\web\voice\dist") {
        New-Item -ItemType Directory -Path "$ServerDir\webhost\dist\voice" -Force | Out-Null
        Copy-Item "$ProjectDir\web\voice\dist\*" "$ServerDir\webhost\dist\voice\" -Recurse -Force
        ok "Voice frontend dist copied"
    }
    if (Test-Path "$ProjectDir\web\admin\dist") {
        New-Item -ItemType Directory -Path "$ServerDir\webhost\dist\admin" -Force | Out-Null
        Copy-Item "$ProjectDir\web\admin\dist\*" "$ServerDir\webhost\dist\admin\" -Recurse -Force
        ok "Admin frontend dist copied"
    }
}

# ============================================================
# Stage 4: Build backend
# ============================================================
$buildStage = 4
if ($SkipFrontend) { $buildStage-- }

if (-not $SkipBuild) {
    stage $buildStage $TotalStages "Building backend"
    Set-Location $ServerDir
    go build -o ridgericetalk.exe ./cmd/server
    ok "Backend built: $ServerDir\ridgericetalk.exe"
} else {
    stage $buildStage $TotalStages "Skipping backend build"
    if (-not (Test-Path "$ServerDir\ridgericetalk.exe")) {
        fail "No existing binary at $ServerDir\ridgericetalk.exe and -SkipBuild requested"
        exit 1
    }
    ok "Using existing binary: $ServerDir\ridgericetalk.exe"
}

# ============================================================
# Stage 5: Generate environment configuration
# ============================================================
$envStage = 5
if ($SkipFrontend) { $envStage-- }
if ($SkipBuild)    { $envStage-- }

stage $envStage $TotalStages "Generating environment configuration"

if ((Test-Path $EnvFile) -and -not $Force) {
    warn "$EnvFile already exists. Use -Force to overwrite."
} else {
    function New-RandomHex($bytes) {
        $b = [byte[]]::new($bytes)
        [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
        return ($b | ForEach-Object { $_.ToString("x2") }) -join ''
    }

    $jwtSecret = New-RandomHex 32
    $csrfSecret = New-RandomHex 32

    $envContent = @"
# RidgeRiceTalk production configuration (generated by deploy-embedded.ps1)
# Generated at: $(Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ")

RRT_ENV=production
RRT_LOG_LEVEL=info
RRT_DEPLOY_MODE=embedded
RRT_PUBLIC_ADDRESS=$PublicAddress

RRT_PORT=$PortApi
RRT_ADMIN_PORT=$PortAdmin

RRT_CORS_ORIGINS=$PublicAddress,http://localhost:$PortApi,http://localhost:$PortAdmin

RRT_DATABASE_URL=$StorageDir\ridgericetalk.db
RRT_DB_DRIVER=sqlite

RRT_JWT_SECRET=$jwtSecret
RRT_CSRF_TOKEN_SECRET=$csrfSecret
RRT_ENCRYPTION_KEY=

RRT_EMBEDDED_DEPS=true
RRT_THIRD_PARTY_DIR=$ServerDir\third_party
RRT_MODELS_DIR=$ServerDir\models

RRT_LIVEKIT_AUTOSTART=true
RRT_LIVEKIT_URL=ws://127.0.0.1:$PortLkWs
RRT_LIVEKIT_PUBLIC_URL=ws://127.0.0.1:$PortLkWs
RRT_NETEASE_API_ENDPOINT=http://127.0.0.1:3300

RRT_STORAGE_TYPE=local
RRT_LOCAL_DATA_PATH=$StorageDir

RRT_ALLOW_REGISTER=true
RRT_MAX_USERS=1000
"@

    $envContent | Set-Content $EnvFile -Encoding UTF8
    ok "Created $EnvFile"
    info "Remember to back up JWT/CSRF secrets from $EnvFile"
}

# ============================================================
# Stage 6: Initialize storage
# ============================================================
$initStage = 6
if ($SkipFrontend) { $initStage-- }
if ($SkipBuild)    { $initStage-- }

stage $initStage $TotalStages "Initializing storage"

New-Item -ItemType Directory -Path $StorageDir -Force | Out-Null
New-Item -ItemType Directory -Path "$ServerDir\third_party" -Force | Out-Null
New-Item -ItemType Directory -Path "$ServerDir\models" -Force | Out-Null
"$null" | Out-File "$StorageDir\.write_test" -Force
Remove-Item "$StorageDir\.write_test" -Force
ok "Storage directory writable: $StorageDir"

# ============================================================
# Stage 7: Complete
# ============================================================
$finalStage = 7
if ($SkipFrontend) { $finalStage-- }
if ($SkipBuild)    { $finalStage-- }

stage $finalStage $TotalStages "Deployment complete"

banner "RidgeRiceTalk Embedded Deployment Ready"

Write-Host "  $CWhite`Deploy directory:$CReset  $DeployDir"
Write-Host "  $CWhite`Server binary:$CReset     $ServerDir\ridgericetalk.exe"
Write-Host "  $CWhite`Public address:$CReset    $PublicAddress"
Write-Host "  $CWhite`API port:$CReset          $PortApi"
Write-Host "  $CWhite`Admin port:$CReset        $PortAdmin"
Write-Host "  $CWhite`Database:$CReset          SQLite ($StorageDir\ridgericetalk.db)"
Write-Host ""
Write-Host "  $CBlue`Start the server:$CReset"
Write-Host "    $CGreen`cd $ServerDir; .\ridgericetalk.exe$CReset"
Write-Host ""
Write-Host "  $CBlue`Or use the helper script:$CReset"
Write-Host "    $CGreen`$ServerDir\scripts\start-server.ps1$CReset"
Write-Host ""
Write-Host "  $CBlue`First run:$CReset"
Write-Host "    Visit $CGreen$PublicAddress/admin$CReset and use the bootstrap token shown in the logs to create the owner account."
Write-Host ""
Write-Host "  $CGray`(Press Ctrl+C to stop the server)$CReset"
Write-Host ""

info "Reminder: ensure ports $PortApi, $PortAdmin, $PortLkWs, $PortLkTcp, $PortLkUdp are open in Windows Firewall."

exit 0

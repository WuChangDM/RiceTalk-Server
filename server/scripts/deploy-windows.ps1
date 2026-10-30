# RidgeRiceTalk Windows 一键部署（常驻控制台 + 自动打开控制面板）
# -----------------------------------------------------------------
# 两种来源：
#   A. 源码模式（默认）：调用 deploy-embedded.ps1 拷源码 / 建前端 / 编后端
#   B. 预构建包模式 -FromBundle <zip 或已解压目录>：目标机无需 Go/Node/npm
#
# 本脚本负责 Windows 运行态：
#   1) 生成服务器对外端口与 LiveKit 公网地址（.env）
#   2) 放通 Windows 防火墙
#   3) 生成「启动服务端.cmd」双击启动器
#   4) 以常驻命令窗口启动服务端（关闭窗口即停止）
#   5) 安装桌面控制台（WinForms + WebView2）并轮询 /api/health，就绪后自动用浏览器打开控制面板
#      （-WithConsole 时再额外启动桌面控制台；-NoBrowser 时两者都不自动打开）
#   6) 停服（控制台「停止」或 console\stop-server.ps1）会同时停止本目录 ridgericetalk.exe 与 livekit\livekit-server.exe
#      （按 ExecutablePath 精确匹配，不按镜像名批量杀；使用外部 LiveKit 时不受影响）
#   7) 数据保全（P1 数据丢失事故修复）：server\storage 是**唯一**数据目录（SQLite ridgericetalk.db、
#      上传文件、预置音频、日志）。全量部署（-FromBundle 且不带 -SkipDeploy）在检测到已有数据库时
#      会先做时间戳备份、然后**直接拒绝**并中止（原始文件零改动）；确需重置数据必须显式 -AllowDataReset
#      （旧 storage 归档到 <部署目录>\_storage.bak.<ts>，可回滚，不会被直接删除）。
#      **升级请一律用 -SkipDeploy -FromBundle。**
#
# 用法（管理员 PowerShell）：
#   .\deploy-windows.ps1 -PublicAddress "http://t46.sjcmc.cn" -PortApi 21103 -PortAdmin 21484
#   .\deploy-windows.ps1 -FromBundle "D:\ridgetalk-win-server-0.4.0.zip" -PublicAddress "http://192.168.1.10:8080"
#   .\deploy-windows.ps1 -FromBundle "D:\ridgetalk-win-server-0.4.0.zip" -DryRun
#
# 必填参数：-PublicAddress（所有模式都需要，含 -SkipDeploy；示例 http://127.0.0.1:8080）
#
# 常用开关：
#   -SkipDeploy      不重新解包/覆盖程序文件（二进制/前端/migrations/console），但仍会重写运行期配置
#                    （.env / .env.production / 启动器 / console json 与 stop 脚本）并重启服务
#   -SkipBuild       后端不重新编译（用已有 server\ridgericetalk.exe）
#   -SkipFrontend    前端不重新构建（用已有 webhost\dist）
#   -NoStart         不启动（只生成配置与启动器）
#   -NoBrowser       不自动打开浏览器/桌面控制台
#   -WithConsole     就绪后额外启动桌面控制台（默认只开浏览器，避免两套 UI 抢屏）
#   -NoConsole       不安装桌面控制台
#   -NoFirewall      不改防火墙（自备规则）
#   -Force           -FromBundle 时允许覆盖非空部署目录（只覆盖程序文件；server\storage 数据目录永不删除）
#   -AllowDataReset  显式允许「重置数据」：仅在 -FromBundle 全量部署且检测到已有数据库时生效，
#                    旧 storage 归档到 <部署目录>\_storage.bak.<ts>，随后新建空库（可回滚）。
#                    **升级不要用它**，升级用 -SkipDeploy -FromBundle
#   -DryRun          只打印将要做什么，不做任何改动
# -----------------------------------------------------------------
param(
    [Parameter(Mandatory = $true)]
    [string]$PublicAddress,

    [string]$DeployDir = "C:\RidgeRiceTalk",
    [string]$SourceDir = "",
    [string]$FromBundle = "",

    [int]$PortApi   = 8080,
    [int]$PortAdmin = 9090,
    [int]$PortLkWs  = 7880,
    [int]$PortLkTcp = 7881,
    [int]$PortLkUdp = 7882,

    [switch]$SkipDeploy,
    [switch]$SkipBuild,
    [switch]$SkipFrontend,
    [switch]$Force,
    [switch]$AllowDataReset,
    [switch]$NoStart,
    [switch]$NoBrowser,
    [switch]$WithConsole,
    [switch]$NoConsole,
    [switch]$NoFirewall,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

# ============================================================
# 控制台输出
# ============================================================
$CReset = "`e[0m"; $CRed = "`e[91m"; $CGreen = "`e[92m"
$CYellow = "`e[93m"; $CCyan = "`e[96m"; $CWhite = "`e[97m"
$CGray = "`e[90m"; $CBlue = "`e[94m"; $CMagenta = "`e[95m"

function step($n, $msg) { Write-Host "`n$CBlue[$n/5]$CReset $CCyan$msg$CReset" }
function ok($msg)      { Write-Host "  $CGreen[OK]$CReset $msg" }
function warn($msg)    { Write-Host "  $CYellow[WARN]$CReset $msg" }
function fail($msg)    { Write-Host "  $CRed[FAIL]$CReset $msg" }
function info($msg)    { Write-Host "  $CGray[INFO]$CReset $msg" }
function banner($msg) {
    Write-Host "`n$CMagenta================================================$CReset"
    Write-Host "$CMagenta  $msg$CReset"
    Write-Host "$CMagenta================================================$CReset"
}

# ============================================================
# 参数与路径
# ============================================================
$BundleMode = -not [string]::IsNullOrWhiteSpace($FromBundle)
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
if ($BundleMode) {
    $SourceDir = $ScriptDir   # 预构建包模式不需要源码目录，占位避免 Resolve-Path 失败
} elseif ([string]::IsNullOrWhiteSpace($SourceDir)) {
    $SourceDir = (Resolve-Path "$ScriptDir\..\..").Path
} else {
    $SourceDir = (Resolve-Path $SourceDir).Path
}
$EmbeddedScript = Join-Path $ScriptDir "deploy-embedded.ps1"
if (-not $BundleMode -and -not (Test-Path $EmbeddedScript)) {
    fail "找不到 deploy-embedded.ps1（应与本脚本同目录）：$EmbeddedScript"
    exit 1
}

$uri = $null
if (-not [System.Uri]::TryCreate($PublicAddress, [System.UriKind]::Absolute, [ref]$uri)) {
    fail "PublicAddress 不是合法的绝对地址：$PublicAddress（示例 http://t46.sjcmc.cn）"
    exit 1
}
if ($uri.Scheme -notin @("http", "https")) {
    fail "PublicAddress 只支持 http/https：$PublicAddress"
    exit 1
}
$Scheme = $uri.Scheme
$PublicHost = $uri.Host
$PublicBase = "${Scheme}://${PublicHost}"
$LiveKitScheme = if ($Scheme -eq "https") { "wss" } else { "ws" }

$ServerDir = Join-Path $DeployDir "server"
$EnvProd   = Join-Path $ServerDir ".env.production"
$EnvRuntime = Join-Path $ServerDir ".env"
$LogDir    = Join-Path $DeployDir "logs"
$StorageDir = Join-Path $ServerDir "storage"
$Launcher  = Join-Path $DeployDir "启动服务端.cmd"
$AdminUrl  = "${PublicBase}:$PortAdmin/admin/"
$HealthUrl = "http://127.0.0.1:$PortApi/api/health"
$LiveKitExe = Join-Path (Join-Path $DeployDir "livekit") "livekit-server.exe"

function Assert-PortUnique() {
    $pairs = @(
        @{ Port = $PortApi;   Name = "API" },
        @{ Port = $PortAdmin; Name = "控制面板" },
        @{ Port = $PortLkWs;  Name = "LiveKit 信令(WS)" },
        @{ Port = $PortLkTcp; Name = "LiveKit TCP 回退" }
    )
    $seen = @{}
    foreach ($p in $pairs) {
        if ($seen.ContainsKey($p.Port)) {
            fail "端口重复：$($p.Name) 与 $($seen[$p.Port]) 都是 $($p.Port)"
            return $false
        }
        $seen[$p.Port] = $p.Name
    }
    if ($PortLkUdp -lt 1 -or $PortLkUdp -gt 65535) {
        fail "LiveKit UDP 端口非法：$PortLkUdp"
        return $false
    }
    return $true
}

if (-not (Assert-PortUnique)) { exit 1 }

# ============================================================
# 预构建包：成员校验 + 解包
# ============================================================
function New-BaseEnvContent([string]$publicBase) {
    # 与 deploy-embedded.ps1 Stage 5 的基础环境变量保持一致（仅 Windows 预构建包需要，
    # 源码模式仍由 deploy-embedded.ps1 生成），差异：预构建包内无 third_party/models，
    # 且默认不联网下载运行期依赖 → RRT_EMBEDDED_DEPS=false（task-49 口径）。
    function New-RandomHex([int]$bytes) {
        $b = [byte[]]::new($bytes)
        [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
        return ($b | ForEach-Object { $_.ToString("x2") }) -join ''
    }
    $jwt = New-RandomHex 32
    $csrf = New-RandomHex 32
    $lkAuto = if (Test-Path $LiveKitExe) { "true" } else { "false" }
    $lines = @(
        "# RidgeRiceTalk production configuration (generated by deploy-windows.ps1 -FromBundle)",
        "# Generated at: $(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ')",
        "",
        "RRT_ENV=production",
        "RRT_LOG_LEVEL=info",
        "RRT_DEPLOY_MODE=embedded",
        "RRT_PUBLIC_ADDRESS=$publicBase",
        "",
        "RRT_PORT=$PortApi",
        "RRT_ADMIN_PORT=$PortAdmin",
        "",
        "RRT_CORS_ORIGINS=$publicBase,http://localhost:$PortApi,http://localhost:$PortAdmin",
        "",
        "RRT_DATABASE_URL=$StorageDir\ridgericetalk.db",
        "RRT_DB_DRIVER=sqlite",
        "RRT_FORCE_AUTOMIGRATE=1",
        "",
        "RRT_JWT_SECRET=$jwt",
        "RRT_CSRF_TOKEN_SECRET=$csrf",
        "RRT_ENCRYPTION_KEY=",
        "",
        "RRT_EMBEDDED_DEPS=false",
        "RRT_THIRD_PARTY_DIR=$ServerDir\third_party",
        "RRT_MODELS_DIR=$ServerDir\models",
        "",
        "RRT_LIVEKIT_AUTOSTART=$lkAuto",
        "RRT_LIVEKIT_URL=ws://127.0.0.1:$PortLkWs",
        "RRT_LIVEKIT_PUBLIC_URL=ws://127.0.0.1:$PortLkWs",
        "RRT_NETEASE_API_ENDPOINT=",
        "",
        "RRT_STORAGE_TYPE=local",
        "RRT_LOCAL_DATA_PATH=$StorageDir",
        "",
        "RRT_ALLOW_REGISTER=true",
        "RRT_MAX_USERS=1000"
    )
    return ($lines -join "`r`n") + "`r`n"
}

function Test-BundleMembers([string]$bundlePath) {
    # 返回包内顶层目录名；非法即 fail + exit
    $required = @(
        "server/ridgericetalk.exe",
        "server/webhost/dist/admin/index.html",
        "migrations",
        "scripts/deploy-windows.ps1",
        "start-server.bat"
    )
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    if (-not (Test-Path $bundlePath)) { fail "找不到预构建包：$bundlePath"; exit 1 }
    $item = Get-Item $bundlePath
    if ($item.PSIsContainer) {
        # 已解压目录：顶层目录即包根
        $top = $item.Name
        $names = @(Get-ChildItem $item.FullName -Recurse -File | ForEach-Object {
            ($_.FullName.Substring($item.FullName.Length).TrimStart('\') -replace '\\', '/')
        })
        $rootPrefix = ""
    } else {
        $zip = [System.IO.Compression.ZipFile]::OpenRead($item.FullName)
        try {
            $names = @()
            $tops = @{}
            foreach ($e in $zip.Entries) {
                $name = $e.FullName -replace '\\', '/'
                if ($name -match '^/' -or $name -match '^[A-Za-z]:') {
                    fail "包内含绝对路径成员，拒绝解包：$name"; exit 1
                }
                $segs = @($name -split '/' | Where-Object { $_ -ne '' })
                if ($segs -contains '..') { fail "包内含路径穿越成员，拒绝解包：$name"; exit 1 }
                if (($e.ExternalAttributes -band 0xF000) -eq 0xA000) {
                    fail "包内含符号链接成员，拒绝解包：$name"; exit 1
                }
                if ($segs.Count -ge 1 -and -not $name.EndsWith('/')) { $tops[$segs[0]] = $true }
                if (-not $name.EndsWith('/')) { $names += ($name -replace "^$([regex]::Escape($segs[0]))/", '') }
            }
        } finally { $zip.Dispose() }
        if ($tops.Count -ne 1) {
            fail "包内顶层目录必须恰好 1 个，实际 $($tops.Count) 个：$($tops.Keys -join ', ')"
            exit 1
        }
        $top = @($tops.Keys)[0]
        $rootPrefix = "$top/"
        $names = @($names | Where-Object { $_ -ne '' })
    }
    foreach ($req in $required) {
        $hit = $names | Where-Object { $_ -eq $req -or $_ -like "$req/*" } | Select-Object -First 1
        if (-not $hit) { fail "预构建包缺少必需成员：$req（包根：$top）"; exit 1 }
    }
    return $top
}

function Expand-BundleTo([string]$bundlePath, [string]$target) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $item = Get-Item $bundlePath
    if ($item.PSIsContainer) {
        $srcRoot = $item.FullName
        Copy-Item -Path (Join-Path $srcRoot "*") -Destination $target -Recurse -Force
        return
    }
    $zip = [System.IO.Compression.ZipFile]::OpenRead($item.FullName)
    try {
        $tops = @($zip.Entries | ForEach-Object { ($_.FullName -replace '\\', '/') -split '/' | Select-Object -First 1 } | Where-Object { $_ -ne '' } | Sort-Object -Unique)
        $top = $tops[0]
        foreach ($e in $zip.Entries) {
            $name = $e.FullName -replace '\\', '/'
            if ($name.EndsWith('/')) { continue }
            if (-not $name.StartsWith("$top/")) { continue }
            $rel = $name.Substring($top.Length + 1)
            $dest = Join-Path $target ($rel -replace '/', '\')
            $parent = Split-Path -Parent $dest
            if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
            [System.IO.Compression.ZipFileExtensions]::ExtractToFile($e, $dest, $true)
        }
    } finally { $zip.Dispose() }
}

# ============================================================
# 数据保全（P1 数据丢失事故）：server\storage 的备份 / 归档
# ============================================================
function Get-DataDbFiles {
    if (-not (Test-Path $StorageDir)) { return @() }
    return @(Get-ChildItem -LiteralPath $StorageDir -Filter "*.db" -File -ErrorAction SilentlyContinue)
}

function Backup-DataDbFiles([string]$reason) {
    # 任何覆盖式部署前：把 server\storage\*.db 复制到 <部署目录>\_data-backup\<UTC>，
    # 复制或校验失败即中止（此时尚未删除/解包任何文件）。
    $files = @(Get-DataDbFiles)
    if ($files.Count -eq 0) { return $null }
    # 同时备份 SQLite 的 -wal / -shm：强制结束进程时 WAL 中可能有未 checkpoint 的已提交事务，
    # 只备份 .db 会丢最近写入（恢复时 -wal 缺失即丢失）。
    foreach ($dbf in @(Get-DataDbFiles)) {
        foreach ($suffix in @('-wal', '-shm')) {
            $companion = "$($dbf.FullName)$suffix"
            if (Test-Path -LiteralPath $companion) { $files += Get-Item -LiteralPath $companion }
        }
    }
    $ts = (Get-Date).ToUniversalTime().ToString("yyyyMMdd'T'HHmmss'Z'")
    $backupDir = Join-Path $DeployDir (Join-Path "_data-backup" $ts)
    New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
    foreach ($f in $files) {
        $dest = Join-Path $backupDir ("{0}.bak-{1}" -f $f.Name, $ts)
        try { Copy-Item -LiteralPath $f.FullName -Destination $dest -Force -ErrorAction Stop }
        catch {
            fail "数据备份失败（$reason）：$($f.FullName) → $dest：$($_.Exception.Message)"
            fail "已中止部署，原始数据文件未做任何修改。"
            exit 1
        }
        if (-not (Test-Path -LiteralPath $dest)) { fail "数据备份校验失败：$dest 不存在（已中止部署）"; exit 1 }
        if ((Get-Item -LiteralPath $dest).Length -ne $f.Length) { fail "数据备份字节数不一致：$dest（已中止部署）"; exit 1 }
        ok ("已备份数据库：{0}（{1} B）→ {2}" -f $f.Name, $f.Length, $dest)
    }
    info "备份目录：$backupDir"
    return $backupDir
}

banner "RidgeRiceTalk Windows 部署"
Write-Host "  $CWhite部署目录  $CReset $DeployDir"
Write-Host "  $CWhite源码目录  $CReset $(if ($BundleMode) { "（预构建包模式）$FromBundle" } else { $SourceDir })"
Write-Host "  $CWhite对外地址  $CReset $PublicBase"
Write-Host "  $CWhite端口      $CReset API=$PortApi  控制面板=$PortAdmin  LiveKit WS=$PortLkWs  TCP=$PortLkTcp  UDP=$PortLkUdp"
Write-Host "  $CWhite控制面板  $CReset $AdminUrl"

if ($DryRun) {
    banner "DRY RUN（不做任何改动）"
    if ($BundleMode) {
        info "将校验预构建包成员（拒绝绝对路径 / .. / 符号链接）并解包：$FromBundle → $DeployDir（-Force=$(if ($Force) { 'true' } else { 'false' })）"
        $dryDbs = @(Get-DataDbFiles | Where-Object { $_.Length -gt 0 })
        if ($dryDbs.Count -gt 0) {
            if ($AllowDataReset) { warn "检测到现有数据库（$($dryDbs.Count) 个）：-AllowDataReset 将归档旧 storage 并重置数据" }
            else { warn "检测到现有数据库（$($dryDbs.Count) 个）：全量部署会被拒绝（需 -AllowDataReset 才重置）；升级请用 -SkipDeploy" }
        }
        info "数据保全：server\storage 永不随程序清理；覆盖前自动备份 *.db 到 _data-backup\"
        info "将生成基础 .env.production（RRT_DB_DRIVER=sqlite、RRT_EMBEDDED_DEPS=false，密钥随机）"
        info "将按端口重写运行期配置：$EnvProd / $EnvRuntime"
        if (-not (Test-Path $LiveKitExe)) { warn "包内/部署目录无 livekit\livekit-server.exe → 语音不可用（可用外部 LiveKit）" }
    } else {
        info "将执行：deploy-embedded.ps1（SkipDeploy=$SkipDeploy）"
        info "将生成：$EnvProd / $EnvRuntime（对外端口 + LiveKit 公网地址）"
    }
    info "将生成：$Launcher"
    info "将启动：常驻控制台窗口（$ServerDir\ridgericetalk.exe）"
    info "将安装：桌面控制台 $(Join-Path $DeployDir 'console\ServerConsole.exe')"
    info "将打开：$AdminUrl"
    if (-not $NoFirewall) {
        info "将放通防火墙：TCP $PortApi,$PortAdmin,$PortLkWs,$PortLkTcp / UDP $PortLkUdp"
    }
    exit 0
}

# ============================================================
# 步骤 1/5：部署文件
# ============================================================
step 1 "部署文件$(if ($SkipDeploy) { '（-SkipDeploy 跳过）' } elseif ($BundleMode) { '（解包预构建包）' } else { '（deploy-embedded.ps1）' })"
if ($SkipDeploy) {
    info "已指定 -SkipDeploy，跳过文件部署（复用 $DeployDir 现有内容）"
    info "注意：-SkipDeploy 仍会重写运行期配置（.env / .env.production / 启动器 / console json 与 stop 脚本）并重启服务"
    if (-not (Test-Path (Join-Path $ServerDir "ridgericetalk.exe"))) {
        fail "跳过部署，但缺少 $ServerDir\ridgericetalk.exe"
        exit 1
    }
} elseif ($BundleMode) {
    $bundlePath = $FromBundle
    if (-not [System.IO.Path]::IsPathRooted($bundlePath)) {
        $bundlePath = (Resolve-Path $bundlePath).Path
    } else {
        $bundlePath = (Resolve-Path $bundlePath).Path
    }
    $targetNonEmpty = (Test-Path $DeployDir) -and ((Get-ChildItem $DeployDir -Force | Measure-Object).Count -gt 0)
    if ($targetNonEmpty -and -not $Force -and -not $SkipDeploy) {
        fail "部署目录非空：$DeployDir（确认要覆盖请加 -Force）"
        exit 1
    }
    $top = Test-BundleMembers $bundlePath
    info "预构建包校验通过（顶层目录：$top）"
    $storageAside = $null
    # ── 数据保全（P1）：检测现有数据库 → 自动备份 → 默认拒绝全量覆盖 ──
    $existingDbs = @(Get-DataDbFiles | Where-Object { $_.Length -gt 0 })
    $hasExistingData = $existingDbs.Count -gt 0
    if ($hasExistingData) {
        $dbDesc = ($existingDbs | ForEach-Object { "{0}（{1} B）" -f $_.FullName, $_.Length }) -join '；'
        $backupDir = Backup-DataDbFiles "全量部署前"
        if (-not $AllowDataReset) {
            fail "检测到现有数据库：$dbDesc"
            fail "全量部署会重建程序目录并重置数据（旧版行为）—— 已中止，原始数据文件零改动。"
            if ($backupDir) { info "已自动备份：$backupDir" }
            info "升级（保留数据，推荐）：deploy-windows.ps1 -FromBundle <包> -SkipDeploy -PublicAddress <地址>"
            info "确需重置数据：追加 -AllowDataReset（旧数据归档到 $DeployDir\_storage.bak.<ts>，可回滚）"
            exit 1
        }
        warn "已指定 -AllowDataReset：本次将重置数据（旧数据归档到 $DeployDir\_storage.bak.<ts>，不会直接删除）"
    } elseif (Test-Path $StorageDir) {
        info "现有数据目录无数据库文件，按「保留数据目录」处理"
    }
    if ($targetNonEmpty -and $Force) {
        info "已指定 -Force：清理旧的 server/migrations/console/scripts 后覆盖"
        # 覆盖升级前先停掉本实例：按 ExecutablePath 精确匹配，避免 taskkill /IM 按镜像名误杀同机其它实例。
        $runningExe = Join-Path $ServerDir "ridgericetalk.exe"
        if (Test-Path $runningExe) {
            $runningTarget = [IO.Path]::GetFullPath($runningExe)
            $running = Get-CimInstance Win32_Process -Filter "Name='ridgericetalk.exe'" -ErrorAction SilentlyContinue |
                Where-Object { $_.ExecutablePath -and ([IO.Path]::GetFullPath($_.ExecutablePath) -eq $runningTarget) }
            if ($running) {
                warn "检测到本实例仍在运行，覆盖升级前先停止（否则 SQLite 文件被占用）"
                $running | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
                Start-Sleep -Seconds 2
            }
        }
        # 常驻窗口「启动服务端.cmd」的工作目录在部署目录内，会占住目录导致覆盖失败 → 覆盖前按命令行精确匹配关闭。
        # 覆盖升级前同样要停本部署目录的 LiveKit（否则 livekit-server.exe 占住 livekit\ 目录与端口，覆盖后易出现双实例）
        if (Test-Path $LiveKitExe) {
            $lkTarget = [IO.Path]::GetFullPath($LiveKitExe)
            $lkRunning = Get-CimInstance Win32_Process -Filter "Name='livekit-server.exe'" -ErrorAction SilentlyContinue |
                Where-Object { $_.ExecutablePath -and ([IO.Path]::GetFullPath($_.ExecutablePath) -eq $lkTarget) }
            if ($lkRunning) {
                warn "检测到本部署目录的 LiveKit 仍在运行，覆盖升级前先停止"
                $lkRunning | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
                Start-Sleep -Seconds 1
            }
        }
        $launcherCmds = Get-CimInstance Win32_Process -Filter "Name='cmd.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.CommandLine -and $_.CommandLine.Contains($DeployDir) }
        if ($launcherCmds) {
            warn "检测到部署目录的常驻启动窗口，覆盖升级前先关闭"
            $launcherCmds | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
            Start-Sleep -Seconds 2
        }
        # 桌面控制台（ServerConsole.exe）会锁住 console/*.dll，覆盖升级前按部署目录精确匹配关闭。
        $consoleProcs = Get-CimInstance Win32_Process -Filter "Name='ServerConsole.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -and $_.ExecutablePath.Contains($DeployDir) }
        if ($consoleProcs) {
            warn "检测到本部署目录的桌面控制台在运行，覆盖升级前先关闭"
            $consoleProcs | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
            Start-Sleep -Seconds 1
        }
        # 数据目录 server\storage 绝不随程序清理：先整体移出，解包后按策略恢复 / 保留归档（P1）
        if (Test-Path $StorageDir) {
            $asideTs = Get-Date -Format "yyyyMMdd-HHmmss"
            $storageAside = Join-Path $DeployDir ("_storage.bak." + $asideTs)
            try { Move-Item -LiteralPath $StorageDir -Destination $storageAside -Force -ErrorAction Stop }
            catch { fail "归档数据目录失败：$StorageDir → $storageAside：$($_.Exception.Message)（未删除任何数据，已中止）"; exit 1 }
            info "已将数据目录移出（避免随程序清理）：$storageAside"
        }
        foreach ($d in @("server", "migrations", "console", "scripts")) {
            $p = Join-Path $DeployDir $d
            if (Test-Path $p) {
                try { Remove-Item -Path $p -Recurse -Force -ErrorAction Stop }
                catch {
                    if ($storageAside -and (Test-Path $storageAside) -and -not (Test-Path $StorageDir)) {
                        try { Move-Item -LiteralPath $storageAside -Destination $StorageDir -Force -ErrorAction Stop }
                        catch { fail "清理失败，且数据目录还原失败；旧数据在 $storageAside（未丢失）" }
                    }
                    fail "清理 $p 失败：$($_.Exception.Message)（请先手动停止服务端后重试）"; exit 1
                }
            }
        }
    }
    New-Item -ItemType Directory -Force -Path $DeployDir | Out-Null
    Expand-BundleTo $bundlePath $DeployDir
    foreach ($d in @($ServerDir, (Join-Path $DeployDir 'migrations'), $StorageDir, (Join-Path $ServerDir 'third_party'), (Join-Path $ServerDir 'models'), $LogDir)) {
        New-Item -ItemType Directory -Force -Path $d | Out-Null
    }
    # 数据目录按策略恢复 / 保留归档（P1）
    if ($storageAside -and (Test-Path $storageAside)) {
        if ($hasExistingData -and $AllowDataReset) {
            warn "已 -AllowDataReset：数据目录保持全新（旧数据完整归档在 $storageAside，未删除）"
            info "回滚方式：停服 → 删除 $StorageDir → 把 $storageAside 移回 $StorageDir"
        } else {
            if (Test-Path $StorageDir) {
                try { Remove-Item -Path $StorageDir -Recurse -Force -ErrorAction Stop }
                catch { fail "清理临时数据目录失败：$StorageDir：$($_.Exception.Message)（旧数据在 $storageAside）"; exit 1 }
            }
            try { Move-Item -LiteralPath $storageAside -Destination $StorageDir -Force -ErrorAction Stop }
            catch { fail "恢复数据目录失败：$($_.Exception.Message)（旧数据仍在 $storageAside，未丢失）"; exit 1 }
            ok "保留现有数据目录：$StorageDir"
        }
    }
    if (-not (Test-Path (Join-Path $ServerDir "ridgericetalk.exe"))) {
        fail "解包后仍缺 $ServerDir\ridgericetalk.exe（包损坏？）"
        exit 1
    }
    if (-not (Test-Path (Join-Path $ServerDir "webhost\dist\admin\index.html"))) {
        fail "解包后仍缺 server\webhost\dist\admin\index.html —— admin 将 404"
        exit 1
    }
    # 基础 .env.production（仅当不存在或 -Force）
    if ((-not (Test-Path $EnvProd)) -or $Force) {
        New-BaseEnvContent $PublicBase | Set-Content -Path $EnvProd -Encoding utf8
        ok "已生成基础 .env.production（SQLite + 随机密钥）"
    } else {
        info "已存在 $EnvProd，保留并只覆盖端口/对外地址段"
    }
    if (-not (Test-Path $LiveKitExe)) {
        warn "未发现 livekit\livekit-server.exe：语音功能不可用（可另放该文件或改用外部 LiveKit）"
    }
    ok "预构建包已解包到 $DeployDir"
} else {
    $deployArgs = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$EmbeddedScript`"",
        "-PublicAddress", "`"$PublicBase`"",
        "-DeployDir", "`"$DeployDir`"",
        "-PortApi", "$PortApi", "-PortAdmin", "$PortAdmin",
        "-PortLkWs", "$PortLkWs", "-PortLkTcp", "$PortLkTcp", "-PortLkUdp", "$PortLkUdp"
    )
    if ($SkipBuild)    { $deployArgs += "-SkipBuild" }
    if ($SkipFrontend) { $deployArgs += "-SkipFrontend" }
    if ($Force)        { $deployArgs += "-Force" }

    if ((Join-Path $SourceDir "server") -ne (Join-Path $DeployDir "server")) {
        # deploy-embedded.ps1 用「脚本所在仓库」的上一级作为项目源，因此从源码目录调用即可
        Push-Location $SourceDir
        try {
            $proc = Start-Process powershell.exe -ArgumentList $deployArgs -Wait -PassThru -NoNewWindow
            $code = $proc.ExitCode
        } finally { Pop-Location }
    } else {
        $proc = Start-Process powershell.exe -ArgumentList $deployArgs -Wait -PassThru -NoNewWindow
        $code = $proc.ExitCode
    }

    if ($code -ne 0) {
        fail "deploy-embedded.ps1 失败（exit $code），详见上方输出"
        exit $code
    }
    ok "文件部署完成"
}

# ============================================================
# 步骤 2/5：生成对外端口与 LiveKit 公网地址（.env）
# ============================================================
step 2 "写入运行时配置（$EnvRuntime）"
if (-not (Test-Path $EnvProd)) {
    fail "找不到 $EnvProd（先不带 -SkipDeploy 跑一次）"
    exit 1
}

$override = [ordered]@{
    "RRT_ENV"                  = "production"
    "RRT_DEPLOY_MODE"          = "embedded"
    "RRT_PUBLIC_ADDRESS"       = $PublicBase
    "RRT_PORT"                 = "$PortApi"
    "RRT_ADMIN_PORT"           = "$PortAdmin"
    "RRT_LIVEKIT_URL"          = "${LiveKitScheme}://127.0.0.1:$PortLkWs"
    "RRT_LIVEKIT_PUBLIC_URL"   = "${LiveKitScheme}://${PublicHost}:$PortLkWs"
    "RRT_LIVEKIT_PORT"         = "$PortLkWs"
    "RRT_LIVEKIT_TCP_PORT"     = "$PortLkTcp"
    "RRT_LIVEKIT_UDP_PORT"     = "$PortLkUdp"
    "RRT_CORS_ORIGINS"         = "$PublicBase,${PublicBase}:$PortAdmin,http://localhost:$PortApi,http://localhost:$PortAdmin"
    "RRT_TRUSTED_PROXIES"      = ""
}

$lines = Get-Content -Path $EnvProd -ErrorAction Stop | ForEach-Object { $_ }
$result = New-Object System.Collections.Generic.List[string]
foreach ($line in $lines) {
    $key = ($line -split "=", 2)[0].Trim()
    if ($override.Contains($key)) { continue }
    $result.Add($line)
}
foreach ($key in $override.Keys) {
    if ([string]::IsNullOrEmpty($override[$key])) { continue }
    $result.Add("$key=$($override[$key])")
}
$content = ($result -join "`r`n") + "`r`n"
Set-Content -Path $EnvProd -Value $content -Encoding utf8 -NoNewline
Set-Content -Path $EnvRuntime -Value $content -Encoding utf8 -NoNewline
ok "已写入 $EnvProd（并复制为 $EnvRuntime，服务端启动时自动读取）"

# ============================================================
# 步骤 3/5：防火墙
# ============================================================
step 3 "配置 Windows 防火墙"
if ($NoFirewall) {
    info "已指定 -NoFirewall，跳过"
} else {
    $rulePrefix = "RidgeRiceTalk"
    Get-NetFirewallRule -DisplayName "$rulePrefix*" -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    $rules = @(
        @{ Name = "API";          Port = $PortApi;   Protocol = "TCP" },
        @{ Name = "控制面板";      Port = $PortAdmin; Protocol = "TCP" },
        @{ Name = "LiveKit WS";   Port = $PortLkWs;  Protocol = "TCP" },
        @{ Name = "LiveKit TCP";  Port = $PortLkTcp; Protocol = "TCP" },
        @{ Name = "LiveKit UDP";  Port = $PortLkUdp; Protocol = "UDP" }
    )
    foreach ($r in $rules) {
        New-NetFirewallRule -DisplayName "$rulePrefix-$($r.Name)" -Direction Inbound `
            -Action Allow -Protocol $r.Protocol -LocalPort $r.Port -Profile Any | Out-Null
    }
    ok "已放通：TCP $PortApi,$PortAdmin,$PortLkWs,$PortLkTcp / UDP $PortLkUdp"
    info "公网/NAT 还需在云安全组或端口映射里放行同样的端口"
}

# ============================================================
# 步骤 4/5：生成启动器 + 常驻控制台
# ============================================================
step 4 "生成启动器并启动服务端"
New-Item -ItemType Directory -Path $LogDir -Force | Out-Null

$cmdLines = @(
    "@echo off",
    "chcp 65001 >nul",
    "cd /d `"$ServerDir`"",
    "title RidgeRiceTalk 服务端（关闭此窗口即停止）",
    "echo ===============================================",
    "echo  RidgeRiceTalk 服务端（常驻控制台）",
    "echo  API      : http://127.0.0.1:$PortApi",
    "echo  控制面板 : $AdminUrl",
    "echo  日志     : $LogDir",
    "echo ===============================================",
    "echo.",
    "ridgericetalk.exe",
    "set RC=%ERRORLEVEL%",
    "echo.",
    "echo 服务端已退出（exit code %RC%）",
    "pause"
)
Set-Content -Path $Launcher -Value ($cmdLines -join "`r`n") -Encoding utf8
ok "已生成启动器：$Launcher"

if ($NoStart) {
    info "已指定 -NoStart，不启动服务端"
} else {
    $selfExe = [System.IO.Path]::GetFullPath((Join-Path $ServerDir 'ridgericetalk.exe'))
    $existing = @(Get-CimInstance Win32_Process -Filter "Name = 'ridgericetalk.exe'" -ErrorAction SilentlyContinue | Where-Object {
        $_.ExecutablePath -and ([System.IO.Path]::GetFullPath($_.ExecutablePath) -eq $selfExe)
    })
    if ($existing.Count -gt 0) {
        warn "检测到本部署目录已在运行的 ridgericetalk.exe（PID $($existing.ProcessId -join ',')），先停止旧实例"
        $existing | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
        Start-Sleep -Seconds 2
    }
    # 单实例保证：启动前若本部署目录已有 livekit-server.exe 在跑，先停掉（与 ridgericetalk.exe 同样按 ExecutablePath 精确匹配）
    if (Test-Path $LiveKitExe) {
        $lkSelf = [IO.Path]::GetFullPath($LiveKitExe)
        $lkExisting = @(Get-CimInstance Win32_Process -Filter "Name = 'livekit-server.exe'" -ErrorAction SilentlyContinue | Where-Object {
            $_.ExecutablePath -and ([System.IO.Path]::GetFullPath($_.ExecutablePath) -eq $lkSelf)
        })
        if ($lkExisting.Count -gt 0) {
            warn "检测到本部署目录已在运行的 livekit-server.exe（PID $($lkExisting.ProcessId -join ',')），先停止旧实例"
            $lkExisting | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
            Start-Sleep -Seconds 1
        }
    }
    Start-Process -FilePath "cmd.exe" -ArgumentList "/k", "`"$Launcher`"" | Out-Null
    ok "已在新命令窗口启动服务端（该窗口常驻显示日志，关闭即停止）"
}

# ============================================================
# 步骤 4.5/5：安装桌面控制台（WinForms + WebView2）
# ============================================================
if ($NoConsole) {
    info "已指定 -NoConsole，跳过桌面控制台"
} else {
    $consoleOut = Join-Path $DeployDir "console"
    $consoleExe = Join-Path $consoleOut "ServerConsole.exe"
    $bundledExe = Join-Path $consoleOut "ServerConsole.exe"
    if (-not (Test-Path $bundledExe)) {
        $consoleSrc = Join-Path $PSScriptRoot "..\..\tools\server-console"
        $builtExe   = Join-Path $consoleSrc "dist\ServerConsole.exe"
        if (-not (Test-Path $builtExe)) {
            $buildScript = Join-Path $consoleSrc "build.ps1"
            if (Test-Path $buildScript) {
                info "未找到已构建的桌面控制台，尝试现场构建…"
                try {
                    & powershell -NoProfile -ExecutionPolicy Bypass -File $buildScript | Out-Null
                } catch { warn "桌面控制台构建失败：$($_.Exception.Message)" }
            }
        }
        if (Test-Path $builtExe) {
            New-Item -ItemType Directory -Force -Path $consoleOut | Out-Null
            Copy-Item (Join-Path $consoleSrc "dist\*") $consoleOut -Recurse -Force
        }
    }
    if (Test-Path $consoleExe) {
        New-Item -ItemType Directory -Force -Path $consoleOut | Out-Null
        # 停止脚本：只结束本部署目录下的 ridgericetalk.exe。
        # 用 taskkill /IM ridgericetalk.exe 会按镜像名批量杀，误杀同机其它实例/同名进程（本项目曾因此误杀客户端），
        # 故改为按 ExecutablePath 精确匹配的 PowerShell 脚本。
        $serverExePath = Join-Path (Join-Path $DeployDir "server") "ridgericetalk.exe"
                # 停止脚本：只结束本部署目录下的 ridgericetalk.exe 与 livekit-server.exe（含包内 LiveKit）。
        # 用 taskkill /IM 会按镜像名批量杀，误杀同机其它实例/同名进程（本项目曾因此误杀客户端），
        # 故改为按 ExecutablePath 精确匹配的 PowerShell 脚本。
        $serverExePath = Join-Path (Join-Path $DeployDir "server") "ridgericetalk.exe"
        $stopScript = Join-Path $consoleOut "stop-server.ps1"
        $stopPs1Lines = @(
            '# 由 deploy-windows.ps1 生成：只停止本部署目录下的 ridgericetalk.exe 与 livekit-server.exe（含包内 LiveKit）。'
            '# 按 ExecutablePath 精确匹配，不按镜像名批量杀（避免误杀同机其它实例/同名客户端）。'
            '# 若使用外部 LiveKit（RRT_LIVEKIT_URL 指向别处），本脚本不会影响那台实例。'
            'param()'
            ('$deployDir = "{0}"' -f $DeployDir)
            '$targets = @('
            '    [IO.Path]::GetFullPath((Join-Path $deployDir "server\ridgericetalk.exe"))'
            '    [IO.Path]::GetFullPath((Join-Path $deployDir "livekit\livekit-server.exe"))'
            ')'
            '$procs = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and ($targets -contains [IO.Path]::GetFullPath($_.ExecutablePath)) })'
            '$procs | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }'
            'Start-Sleep -Milliseconds 800'
            '$left = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and ($targets -contains [IO.Path]::GetFullPath($_.ExecutablePath)) })'
            'if ($left.Count -gt 0) { Write-Output ("警告：仍有进程未退出（PID " + (($left.ProcessId) -join ",") + "），请手动结束") } else { Write-Output "已停止本部署目录的 ridgericetalk.exe 与 livekit-server.exe（若在运行）；外部 LiveKit 不受影响" }'
        )
        Set-Content -Path $stopScript -Value $stopPs1Lines -Encoding utf8

        $consoleCfg = [ordered]@{
            title          = "RidgeRiceTalk 服务端控制台"
            adminUrl       = $AdminUrl
            healthUrl      = $HealthUrl
            serverDir      = $DeployDir
            startCommand   = "start `"`" `"$Launcher`""
            stopCommand    = "powershell -NoProfile -ExecutionPolicy Bypass -File `"$stopScript`""
            restartCommand = "powershell -NoProfile -ExecutionPolicy Bypass -File `"$stopScript`" & timeout /t 2 >nul & start `"`" `"$Launcher`""
        }
        $consoleCfg | ConvertTo-Json | Set-Content -Path (Join-Path $consoleOut "server-console.json") -Encoding utf8
        ok "桌面控制台已安装：$consoleExe"
        try {
            $ws = New-Object -ComObject WScript.Shell
            $shortcutPaths = @(
                (Join-Path ([Environment]::GetFolderPath("Desktop")) "RidgeRiceTalk 服务端控制台.lnk"),
                (Join-Path ([Environment]::GetFolderPath("Programs")) "RidgeRiceTalk 服务端控制台.lnk")
            )
            foreach ($lnkPath in $shortcutPaths) {
                $lnk = $ws.CreateShortcut($lnkPath)
                $lnk.TargetPath = $consoleExe
                $lnk.WorkingDirectory = $consoleOut
                $lnk.IconLocation = "$consoleExe,0"
                $lnk.Description = "RidgeRiceTalk 服务端控制台"
                $lnk.Save()
            }
            ok "已创建快捷方式（桌面 + 开始菜单）"
        } catch { warn "创建快捷方式失败：$($_.Exception.Message)" }
    } else {
        warn "未找到桌面控制台组件（console\ServerConsole.exe），仍可用浏览器访问 $AdminUrl"
    }
}

# ============================================================
# 步骤 5/5：等待就绪并打开控制面板
# ============================================================
step 5 "等待服务端就绪并打开控制面板"
if ($NoStart) {
    info "未启动服务端，跳过健康检查与浏览器打开"
} else {
    $ready = $false
    $deadline = (Get-Date).AddSeconds(90)
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 3
            if ($resp.StatusCode -ge 200 -and $resp.StatusCode -lt 300) { $ready = $true; break }
        } catch { Start-Sleep -Seconds 2 }
    }
    if ($ready) {
        ok "服务端就绪：$HealthUrl"
    } else {
        warn "90 秒内未探到 $HealthUrl，请查看常驻窗口与 $LogDir 下的日志"
    }

    $consoleExe = Join-Path $DeployDir "console\ServerConsole.exe"
    if ($NoBrowser) {
        info "已指定 -NoBrowser，请手动访问 $AdminUrl 或运行 $consoleExe"
    } else {
        # 默认：自动用浏览器打开控制面板（用户明确要求「部署完自动跳出面板网页」）
        Start-Process $AdminUrl | Out-Null
        ok "已自动打开控制面板：$AdminUrl"
        if ($WithConsole -and (Test-Path $consoleExe)) {
            Start-Process $consoleExe | Out-Null
            ok "已额外启动桌面控制台（内嵌同一套面板）"
        } elseif (Test-Path $consoleExe) {
            info "桌面控制台已安装但未自动启动（需要时用桌面快捷方式打开）"
        }
    }
}

# ============================================================
# 汇总
# ============================================================
banner "部署完成"
Write-Host "  $CWhite控制面板$CReset   $CGreen$AdminUrl$CReset"
Write-Host "  $CWhiteAPI     $CReset   http://127.0.0.1:$PortApi/api/health"
if (Test-Path $LiveKitExe) {
    Write-Host "  $CWhite语音    $CReset   ${LiveKitScheme}://${PublicHost}:$PortLkWs  （TCP 回退 $PortLkTcp / UDP $PortLkUdp）"
} else {
    Write-Host "  $CWhite语音    $CReset   ${CYellow}不可用（缺 livekit\livekit-server.exe；可另放或用外部 LiveKit）$CReset"
}
Write-Host "  $CWhite数据目录$CReset   $ServerDir\storage"
Write-Host "  $CWhite数据保全$CReset   全量部署检测到数据库会拒绝并自动备份到 $DeployDir\_data-backup\；重置需 -AllowDataReset"
Write-Host "  $CWhite重启方式$CReset   双击 $Launcher"
Write-Host "  $CWhite停止方式$CReset   关闭常驻控制台窗口（或任务管理器结束 ridgericetalk.exe）"
$consoleExeHint = Join-Path $DeployDir "console\ServerConsole.exe"
if (Test-Path $consoleExeHint) {
    Write-Host "  $CWhite桌面控制台$CReset $consoleExeHint（快捷方式：桌面 / 开始菜单）"
}
Write-Host ""
Write-Host "  $CGray首次使用：控制面板首次打开需初始化 Owner，引导令牌打印在常驻窗口 / $LogDir 的启动日志里。$CReset"
Write-Host ""

# ============================================================
# 回写文档与 CHANGELOG（由 Agent 完成，脚本不自动改仓库）
# ============================================================
info "如需开机自启：Win+R 输入 shell:startup，把「启动服务端.cmd」的快捷方式放进去"
exit 0

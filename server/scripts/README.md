# RidgeRiceTalk Server Scripts

服务端运维脚本目录。

## 脚本清单

### 部署相关

| 脚本 | 用途 | 适用场景 |
|------|------|---------|
| `deploy-baremetal.sh` | 一键裸机部署（Debian/Ubuntu） | Docker 不可用 / 资源受限服务器 |
| `uninstall-baremetal.sh` | 裸机部署卸载 | 清理裸机部署 |
| `install-service.sh` | systemd 服务安装（手动） | 手动安装 systemd 服务 |

### 启动相关

| 脚本 | 用途 | 适用场景 |
|------|------|---------|
| `start-server.sh` | 开发环境一键启动（Linux/macOS） | 本地开发 |
| `start-server.ps1` | 开发环境一键启动（Windows） | 本地开发 |
| `setup.sh` | 开发环境初始化 | 首次克隆代码后 |
| `kill-server.sh` | 停止运行中的服务 | 调试/重启 |

### 备份与回滚

| 脚本 | 用途 | 基本用法 |
|------|------|---------|
| `backup-db.sh` | 数据库备份（PostgreSQL custom 格式 `-Fc`，备份后自检 TABLE DATA 条目） | `sudo ./backup-db.sh [--out-dir <dir>] [--keep <n>] [--dry-run]` |
| `backup-storage.sh` | storage 运行态目录备份（tar.gz，含密钥，备份后自检） | `sudo ./backup-storage.sh [--out-dir <dir>] [--keep <n>] [--dry-run]` |
| `rollback-binary.sh` | 服务端二进制回滚（md5 校验 + 健康检查；无备份时自动建立首份备份） | `sudo ./rollback-binary.sh [--list] [--to <file>] [--dry-run]` |

备份默认写入 `/var/backups/ridgericetalk`（二进制备份为 `/var/backups/ridgericetalk/bin`）。
`deploy_server_binary.sh` 替换二进制前也会写入标准回滚备份
（`ridgericetalk-bin-<UTC时间戳>-<md5前8位>`，root:root 600，附同名 `.md5` 旁文件），二者口径一致，形成「部署 → 回滚」闭环。

### 模板文件

| 文件 | 用途 |
|------|------|
| `ridgericetalk.service.tmpl` | 后端 systemd 服务模板 |
| `livekit.service.tmpl` | LiveKit Server systemd 服务模板 |
| `netease-api.service.tmpl` | NeteaseCloudMusicApi systemd 服务模板 |
| `easytier.service.tmpl` | EasyTier 虚拟网络 systemd 服务模板 |
| `ridgericetalk-backup.service.tmpl` | 每日备份 oneshot 服务模板（依次执行 `backup-db.sh`、`backup-storage.sh`） |
| `ridgericetalk-backup.timer.tmpl` | 每日备份 timer 模板（`OnCalendar=*-*-* 04:17:00` + `RandomizedDelaySec=30min`，`Persistent=true`） |

上述 `.tmpl` 均由 `deploy-baremetal.sh` 渲染（`sed` 替换 `{{...}}`，如 `{{SERVER_DIR}}`）后写入 `/etc/systemd/system/`。

### 定时自动备份

`deploy-baremetal.sh` 默认在 Stage 7 安装 **每日自动备份**（两个单元，只启用 timer，不启动 service）：

| 单元 | 作用 |
|------|------|
| `ridgericetalk-backup.timer` | 每日 **04:17**（本地时区，错开整点）触发；`RandomizedDelaySec=30min` 加随机抖动；`Persistent=true` 错过补跑 |
| `ridgericetalk-backup.service` | `Type=oneshot`，依次执行 `backup-db.sh` 与 `backup-storage.sh`（两个都跑完，任一失败则整体非 0 退出）；`Nice=10` + `IOSchedulingClass=idle` 不抢业务资源；日志进 journald |

```bash
# 查看下次触发时间
systemctl list-timers ridgericetalk-backup.timer

# 查看备份日志 / 手动触发一次
journalctl -u ridgericetalk-backup
sudo systemctl start ridgericetalk-backup.service   # 手动跑一次（退出码 0 表示成功）

# 关闭自动备份（二选一）
sudo systemctl disable --now ridgericetalk-backup.timer   # 临时关闭
sudo ./deploy-baremetal.sh --no-backup-timer              # 部署时不再安装该 timer
```

产物落在 `/var/backups/ridgericetalk/`（`ridgericetalk-db-<UTC>.dump` 与 `ridgericetalk-storage-<UTC>.tar.gz`，均 `chmod 600`），各自默认**保留最近 7 份**。保留份数/输出目录的调整见 `backup-db.sh`、`backup-storage.sh` 的 `--keep` / `--out-dir`。

## 裸机一键部署

### 快速开始

```bash
# 标准部署（交互式）
sudo ./deploy-baremetal.sh

# 雨云服务器 NAT 部署（5 端口映射）
sudo ./deploy-baremetal.sh \
  --port-api 50100 --port-admin 50103 \
  --port-lk-ws 50101 --port-lk-tcp 50102 --port-lk-udp 50104 \
  --public-ip <REDACTED-OLD-SERVER-IP>

# 干净服务器一键部署（自动克隆源码）
sudo ./deploy-baremetal.sh --repo-url https://github.com/<org>/ridgericetalk.git
```

### 参数说明

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--stage <name>` | 从指定阶段恢复执行（deps/livekit/build/db/config/systemd/netease/start） | `deps` |
| `--dry-run` | 只打印将要执行的动作，不做任何修改（安全检查/预览用） | `false` |
| `--force` | 强制覆盖配置文件（并重建 `storage/server-state.json`） | `false` |
| `--port-api <port>` | API 端口 | `8080` |
| `--port-admin <port>` | Admin 端口 | `9090` |
| `--port-lk-ws <port>` | LiveKit WebSocket 端口 | `7880` |
| `--port-lk-tcp <port>` | LiveKit TCP 端口 | `7881` |
| `--port-lk-udp <port>` | LiveKit UDP 端口 | `7882` |
| `--public-ip <ip>` | 公网 IP（默认自动检测） | 自动检测 |
| `--repo-url <url>` | 自动 git clone 源码到 /opt/ridgericetalk（若不存在） | 空 |
| `--repo-branch <name>` | 克隆时使用的分支 | `main` |
| `--with-monitoring` | 安装 Netdata 系统监控（可选，默认不启用） | `false` |
| `--no-backup-timer` | 不安装每日自动备份 timer（默认安装 `ridgericetalk-backup.timer`） | `false`（即默认安装） |

### 部署阶段

| 阶段 | 名称 | 说明 |
|------|------|------|
| Stage 0 | 克隆源码 | 可选，通过 `--repo-url` 自动克隆源码 |
| Stage 1 | 检测依赖 | 检测 Go/PostgreSQL/FFmpeg 等依赖 |
| Stage 2 | 安装系统依赖 | 安装 apt 包、Go、Node.js |
| Stage 3 | 下载 LiveKit | 下载 LiveKit Server 二进制 |
| Stage 4 | 编译后端与前端 | go build + 编译版本化迁移工具 ridgericetalk-migrate + npm run build |
| Stage 5 | 初始化 PostgreSQL | 创建数据库与用户 |
| Stage 6 | 生成配置文件 | 生成 .env.production（含 CORS 白名单、LiveKit 公网地址） |
| Stage 7 | 安装 systemd 服务 | 渲染并安装 ridgericetalk/livekit 服务，以及每日备份 timer（`--no-backup-timer` 可跳过） |
| Stage 8 | 部署 NeteaseCloudMusicApi | 音乐机器人依赖（Node.js，监听 127.0.0.1:3300） |
| Stage 9 | 启动服务与健康检查 | 执行数据库迁移、自动开放防火墙、启动所有服务并验证 |

执行 `./deploy-baremetal.sh --help` 查看完整参数列表。

### 详细文档

参见 [部署文档 §6.5 裸机一键部署](../../../文档/05-工程规范与运维/部署文档.md#65-裸机一键部署推荐用于-docker-不可用场景)。

## 开发环境启动

```bash
# 首次初始化
./scripts/setup.sh

# 启动服务（自动检测依赖、生成密钥、启动 LiveKit）
./scripts/start-server.sh
```

详细说明参见 [开发规范文档](../../../文档/05-工程规范与运维/开发规范文档.md)。

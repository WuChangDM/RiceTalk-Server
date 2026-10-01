# ridgericetalk — Go 服务端

> RidgeRiceTalk 的 Go 服务端子仓库，提供 REST API、WebSocket、语音（LiveKit）、Bots、文件、协作功能模块。

本 README 描述本仓库的目录结构与模块职责。完整 Agent 行为规范见 [../AGENTS.md](file:///c:/RICETALK/AGENTS.md)，项目总览见 [../README.md](file:///c:/RICETALK/README.md)。

## 仓库职责定位

- 提供 HTTP REST API（Gin 框架）与 WebSocket 实时通信
- 集成 LiveKit Server SDK 实现语音频道、屏幕共享
- 集成网易云音乐 API 实现音乐机器人，含 TTS 引擎
- 提供文件上传/存储、云文件、共享文档、日程、白板、小游戏、虚拟网络等功能模块
- 提供管理后台 API（Owner 初始化、模块管理、运行时监控、网络配置）
- 提供自包含与裸机一键部署脚本、systemd 服务模板
- 内嵌前端静态资源托管：管理后台 `webhost/dist/admin/` 与**归档前构建**的网页版 voice `webhost/dist/voice/`（不再重建）

## 快速开始

方式一：引导脚本一条流（自动克隆仓库并转交部署脚本）：

```bash
curl -fsSL https://raw.githubusercontent.com/WuChangDM/RiceTalk-Server/main/install.sh | bash
```

方式二：手动克隆 + 部署（两步）：

```bash
git clone --depth 1 https://github.com/WuChangDM/RiceTalk-Server.git && cd RiceTalk-Server
sudo bash server/scripts/deploy-embedded.sh   # 自动探测公网 IP，可加 --public-address 显式指定
```

部署完成后服务自动启动并通过健康检查，日志在 `server/logs/server.log`，停止/重启方法见部署收尾输出。Owner 初始化（bootstrap token）位于 `server/storage/.bootstrap_token`。

### 端口清单

| 端口 | 协议 | 用途 |
|------|------|------|
| 8080 | tcp | 主 API 与网页客户端（HTTP/WebSocket） |
| 9090 | tcp | 管理后台 `/admin` 与 Prometheus 指标（独立端口，production 下不与 API 共享） |
| 7880 | tcp | LiveKit WebSocket 信令（语音/屏幕共享） |
| 7881 | tcp | LiveKit TCP 媒体回退（UDP 不通时走此端口） |
| 7882 | udp | LiveKit 媒体传输主通道（语音/视频流） |
| 5007 | udp | EasyTier 虚拟网络（可选模块，未启用可不开放） |
| 3300 | tcp | netease-api 音乐 API（仅本地回环 127.0.0.1，供服务端内嵌调用，**不对外开放**） |

> 云服务器部署提示：除 3300 外，其余端口需在云厂商安全组与系统防火墙（ufw/firewalld）同时放行；3300 仅监听本机回环，切勿加入安全组。

## 目录结构

```
ridgericetalk/
├── docs/                          # 仓库级文档
│   └── CHANGELOG.md               # 本仓库变更日志
├── livekit/                       # LiveKit Server 配置模板
│   └── livekit.yaml.template      # LiveKit 配置模板（含 {{TCP_PORT}} 等模板变量）
├── music-bot-worker/              # Node.js 音乐播放 Worker（LiveKit Agents、FFmpeg、队列控制）
├── migrations/                    # 数据库迁移文件（golang-migrate 格式，up/down 配对）
├── scripts/                       # 仓库根级辅助脚本
│   ├── fix_openapi.py             # OpenAPI 修复
│   ├── sync_openapi.py            # OpenAPI 同步
│   ├── update_openapi.py          # OpenAPI 更新
│   ├── validate_openapi.py        # OpenAPI 校验
│   ├── phase4_update_openapi.py   # Phase4 OpenAPI 更新
│   └── stress_register.py         # 压测账号注册
├── server/                        # Go 服务端主代码库（详见下文）
├── tests/                         # 跨仓库测试归档（screenshots/、verification-shots/）
├── web/                           # 网页前端源码（admin + shared；网页版 voice 已于 2026-09-09 归档，源码存根 `归档数据/`）
│   ├── admin/                      # 管理后台前端源码（构建产物 → webhost/dist/admin）
│   ├── shared/                     # 跨端共享代码唯一源（同步 → ridgericetalk-win/src/shared）
│   ├── docs/                       # 前端文档（CHANGELOG.md 等）
│   └── tests/                      # 前端测试 / QA 记录
├── webhost/                       # 前端构建产物托管目录
│   └── dist/                      # 含 admin/、voice/，由 server 内嵌托管（voice 为归档前最后构建产物，不再重建）
├── openapi.yaml                   # OpenAPI 接口规范
├── setup.sh                       # 开发环境初始化脚本
├── docker-compose.dev.yml         # 开发环境 Docker Compose（生产部署已废弃 Docker 路径）
├── CONTRIBUTING.md                # 贡献指南
├── SECURITY.md                    # 安全策略
├── CLAUDE.md                      # Claude Code 快速识别入口
└── CHANGE.md                      # 变更说明
```

### `server/` 详细结构

```
server/
├── cmd/                           # 应用入口点
│   ├── server/                    # HTTP 服务入口（main.go、console.go、console_windows.go）
│   └── migrate/                   # 数据库迁移工具
├── core/                          # 跨模块核心能力（与业务无关的基础库）
│   ├── crypto/                    # AES 加密、密码哈希、Token 生成
│   ├── errors/                    # 统一错误码定义
│   ├── httpbind/                  # HTTP 请求参数绑定与校验
│   ├── idgen/                     # Snowflake ID 生成器
│   └── validator/                 # 输入校验、HIBP 密码泄露检查
├── internal/                      # 私有业务代码（按业务域拆分）
│   ├── admin/                     # 管理后台 API、系统信息采集、网络/端口/在线状态/重启管理
│   ├── auth/                      # 认证授权、Owner bootstrap 初始化
│   ├── bots/                      # 音乐机器人、TTS 引擎、音频播放（CGO/无 CGO 双实现）
│   ├── channel/                   # 频道与空间管理
│   ├── config/                    # 配置管理、密钥生成、LiveKit 配置渲染
│   ├── database/                  # 数据库连接（PostgreSQL/SQLite/SQLCipher）、版本化迁移
│   ├── dm/                        # 私信历史遗留代码（当前产品未启用，待独立简化）
│   ├── files/                     # 文件上传
│   ├── infra/                     # 基础设施层：缓存（Ristretto + 内存）、GORM 仓储实现
│   ├── livekitmgr/                # LiveKit Server SDK 管理器
│   ├── logger/                    # 结构化日志（基于 logrus）
│   ├── message/                   # 消息系统（含编辑/删除/已读回执）
│   ├── metrics/                   # Prometheus 指标与中间件
│   ├── model/                     # 数据库模型定义（集中）
│   ├── network/                   # 网络检测、UPnP 端口映射
│   ├── og/                        # Owner/Group 管理
│   ├── realtime/                  # WebSocket Hub（频道订阅与房间订阅双 map）
│   ├── remoteassist/              # 远程协助
│   ├── repositories/              # 仓储接口抽象
│   ├── screenshare/               # 屏幕共享
│   ├── server/                    # 应用核心：装配、生命周期、路由注册、WebSocket 回调
│   ├── serverstate/               # 服务器运行时状态
│   ├── storage/                   # 文件存储抽象（local/s3）
│   ├── user/                      # 用户管理（含在线状态、自定义状态）
│   └── voice/                     # 语音频道、LiveKit webhook 处理、Token 签发与刷新
├── features/                      # 独立功能模块（可按需启用的协作工具）
│   ├── cloudfs/                   # 云文件系统（含文件夹、共享）
│   ├── minigames/                 # 联机小游戏（子包化：tictactoe/gobang/chess/werewolf/ludo/doudizhu + internal/util）
│   ├── schedule/                  # 日程管理
│   ├── sharedoc/                  # 共享文档协同编辑（含 diff 算法、版本管理）
│   ├── virtualnet/                # 虚拟网络（内网穿透/远程协作）
│   └── whiteboard/                # 白板实时同步（Operational Transform）
├── middleware/                    # Gin 中间件层
│   ├── auth.go                    # JWT 认证
│   ├── breakglass.go              # 紧急访问（break-glass）
│   ├── bruteforce.go              # 暴力破解防护
│   ├── cors.go                    # 跨域资源共享
│   ├── csrf.go                    # CSRF 防护
│   ├── init_check.go              # 系统初始化状态检查
│   ├── rate_limit.go              # 速率限制
│   ├── rbac.go                    # 基于角色的访问控制
│   └── security.go                # 安全响应头
├── scripts/                       # 服务端运维脚本
│   ├── deploy-embedded.sh         # 自包含一键部署（Linux/macOS，无需 PostgreSQL/LiveKit/Netease/FFmpeg）
│   ├── deploy-embedded.ps1        # 自包含一键部署（Windows PowerShell）
│   ├── deploy-baremetal.sh        # 裸机一键部署（Debian/Ubuntu，支持 NAT 雨云服务器参数）
│   ├── uninstall-baremetal.sh     # 裸机部署卸载
│   ├── install-service.sh         # systemd 服务手动安装
│   ├── setup.sh                   # 开发环境初始化
│   ├── start-server.sh            # Linux/macOS 一键启动
│   ├── start-server.ps1           # Windows PowerShell 一键启动
│   ├── kill-server.sh             # 停止运行中的服务
│   ├── ridgericetalk.service.tmpl # 后端 systemd 服务模板
│   ├── livekit.service.tmpl       # LiveKit Server systemd 服务模板
│   ├── netease-api.service.tmpl   # NeteaseCloudMusicApi systemd 服务模板
│   └── README.md                  # 脚本目录说明（部署阶段 Stage 0-9 与参数）
├── storage/                       # 运行时数据目录（audio/、avatars/、cloudfs/、uploads/、*.db、livekit.yaml）
├── tests/                         # 服务端集成测试与测试工具
│   ├── integration/               # 集成测试（auth_flow_test.go）
│   └── testutil/                  # 测试工具（db/gin/jwt 工具）
├── webhost/                       # 前端静态资源托管（dist/admin/、dist/voice/；voice 为归档前最后构建）
├── docker/                        # Docker 部署配置（已废弃，仅保留监控配置）
├── docs/                          # 服务端级 CHANGELOG.md
├── go.mod / go.sum                # Go 模块定义（模块路径：ridgericetalk，Go 1.25.0）
├── Makefile                       # 构建脚本（build/run/test/dev/migrate/build-linux/build-windows）
├── DEV_LOG.md                     # 开发日志
├── README.md                      # 服务端深度文档（语音协作设计、前端 UI 设计、实体状态机）
└── start.bat / start.sh           # 启动便捷脚本
```

### `migrations/` 迁移文件序列

采用 `golang-migrate` 格式（`{version}_{name}.up.sql` / `{version}_{name}.down.sql`），当前序列从 000001 到 000033。关键迁移：000001 baseline、000005 voice_independent、000006 bots_independent、000017 add_livekit_tcp_udp_ports、000019 add_message_attachments、000020 add_security_questions、000031 shared_document_comments、000032 remote_assist_sessions、000033 reconcile_user_presence_table。000033 将历史单数表 `user_presence` 统一为规范表 `user_presences`；两表同时存在时拒绝自动合并。私信历史表保留数据但当前路由未注册，不属于当前产品能力。

## 本地开发流程

### 环境要求

- Go 1.25+（推荐 1.25.10）
- CGO 工具链（音频处理需要 `CGO_ENABLED=1`，否则 opus codec 不可用）
- PostgreSQL 或 SQLite（开发默认 SQLite）

### 常用命令

| 用途 | 命令 |
|------|------|
| 开发环境初始化 | `bash setup.sh`（仓库根）或 `bash server/scripts/setup.sh` |
| 启动开发服务 | `cd server && make dev`（air 热重载）或 `make run` |
| 构建服务端 | `cd server && make build`（CGO_ENABLED=1） |
| 运行测试 | `cd server && make test`（`go test -v -race ./...`） |
| 数据库迁移 | `cd server && make migrate`（up）/ `make migrate-down`（回滚） |
| 格式化 | `cd server && make fmt` |
| 静态检查 | `cd server && make vet` |
| Linux 生产构建 | `cd server && make build-linux` |
| Windows 构建 | `cd server && make build-windows` |
| **提交前验证（AGENTS.md 规定）** | `cd server && go build ./cmd/server && go test ./...` |

### 部署

自包含一键部署（推荐，无需手动安装 PostgreSQL / LiveKit / Netease API / FFmpeg）：

```bash
# Linux / macOS
bash server/scripts/deploy-embedded.sh --public-address http://YOUR_IP:8080

# Windows（管理员 PowerShell）
.\server\scripts\deploy-embedded.ps1 -PublicAddress "http://YOUR_IP:8080"
```

传统裸机一键部署（systemd + PostgreSQL）：

```bash
bash server/scripts/deploy-baremetal.sh
```

详见 `server/scripts/README.md`（部署阶段 Stage 0-9 与参数说明）。

> Docker Compose 部署路径已废弃，生产环境使用裸机 systemd 或自包含部署。

## 与其他仓库的依赖关系

- **下游消费方**：`web/admin`（管理后台，仍维护；网页版 voice 已归档停更，现有托管为归档前构建）和 `ridgericetalk-win/`（Windows 客户端）通过 HTTP REST API + WebSocket + LiveKit SDK 接入本服务端
- **前端静态资源托管**：本仓库 `webhost/dist/voice/`、`webhost/dist/admin/` 由服务端内嵌托管；admin 构建产物来源于 `web/admin`，voice 为**归档前最后构建**（源码 `web/voice` 已于 2026-09-09 归档至根 `归档数据/`）
- **LiveKit 配置**：`livekit/livekit.yaml.template` 是 LiveKit Server 配置模板，部署时由 `deploy-baremetal.sh` 渲染
- **OpenAPI 规范**：`openapi.yaml` 是接口契约，前端通过此文件生成类型

## 变更日志

详见 [docs/CHANGELOG.md](file:///c:/RICETALK/ridgericetalk/docs/CHANGELOG.md)。

## 相关文档

- 服务端深度文档：[server/README.md](file:///c:/RICETALK/ridgericetalk/server/README.md)（语音协作设计、前端 UI 设计、实体状态机、环境变量）
- 服务端详细开发文档：`../文档/03-服务端与数据库/服务端详细开发文档.md`
- 数据库设计：`../文档/03-服务端与数据库/数据库设计详细文档.md`
- API 规范：`openapi.yaml` 与 `../文档/03-服务端与数据库/openapi.yaml`
- WebSocket 协议：`../文档/03-服务端与数据库/websocket协议规范.md`
- 实时通信设计：`../文档/03-服务端与数据库/实时通信专项设计文档.md`
- 部署文档：`../文档/05-工程规范与运维/`

## License

自有代码以 **AGPL-3.0** 发布（见 LICENSE）；捆绑的第三方组件保留各自许可证（见 THIRD_PARTY_NOTICES.md）。

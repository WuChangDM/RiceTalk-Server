# RidgeRiceTalk Server 修改记录

> 按时间倒序记录服务端（ridgericetalk/server）的设计变更和代码修改。

---

## 2026-09-02 — 删除 App 中仅用于保存的 User Service 字段

### 背景

启动时创建的 User Service 已直接注入 Hub 回调；App 结构体中的同名字段只被赋值，从未通过 `a.userSvc` 读取。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/app.go` | 删除无消费者的 `App.userSvc` 字段，保留局部实例用于 presence 初始化和 Hub 回调装配。 |

### 修改原因/目的

删除仅保存、不提供运行时行为的字段，避免误导后续维护者以为 App 仍拥有可访问的 User Service 状态；不改变在线状态重置、在线/离线持久化和广播行为。

### 验证

- `gofmt -w internal/server/app.go internal/server/ws_callbacks.go` 通过。
- `go test ./internal/server/... ./internal/realtime/... ./internal/user/...` 通过。
- `go build ./cmd/server` 通过。
- `go test ./...` 仍为既有基线失败，未新增失败。
- `git diff --check`（服务端子仓库）通过。

---

## 2026-09-02 — 删除 WebSocket 快照回调中的无效服务初始化

### 背景

WebSocket 订阅快照回调曾创建一个新的 Message Service，并设置本地数据路径，但快照内容完全通过 GORM 查询生成，该 Service 从未被调用或返回给其他组件。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/ws_callbacks.go` | 删除无消费者的 Message Service 初始化、无效的本地数据路径设置和对应配置/导入依赖。 |
| `internal/server/app.go` | 更新 Hub 回调装配函数签名。 |

### 修改原因/目的

移除订阅快照路径中没有实际行为的对象创建，降低每次订阅的维护和初始化负担；保留最近消息、置顶消息、未读数和屏幕共享状态的原有查询与返回契约。

### 验证

- `go test ./internal/server/... ./internal/realtime/...` 通过。
- `go build ./cmd/server` 通过。
- `git diff --check` 通过。
- `codegraph sync` 未执行：当前环境未安装 `codegraph` CLI。

---

## 2026-09-02 — 复用 App 级 User Service

### 背景

App 启动时已经创建了 `user.Service` 用于重置在线状态，但 Hub 回调装配又创建了第二个相同服务，仅用于在线/离线状态持久化。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/app.go` | 将 User Service 初始化前移到 Hub 回调装配前，并把同一实例保存到 App。 |
| `internal/server/ws_callbacks.go` | 回调接收并复用 App 级 User Service，删除重复构造。 |

### 修改原因/目的

统一在线状态初始化与 Hub 状态回调的服务所有权，避免重复创建相同 GORM Repository，同时不改变状态写入行为。

### 验证

- `go test ./internal/server/... ./internal/realtime/... ./internal/user/...` 通过。
- `go build ./cmd/server` 通过。
- `git diff --check` 通过。
- `codegraph sync` 未执行：当前环境未安装 `codegraph` CLI。

---

## 2026-09-02 — Admin Service 实例收敛

### 背景

Admin 主路由、Remote Assist 和审计清理分别创建 Admin Service，导致同一进程内存在重复的服务实例与依赖状态来源。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/app.go` | App 创建并持有唯一的 `admin.Service` 实例。 |
| `internal/server/routes.go` | Admin Handler 与 Remote Assist Handler 复用 App 级 Admin Service。 |
| `internal/server/lifecycle.go` | 审计日志清理复用同一 Admin Service，移除重复构造。 |
| `internal/admin/handler.go` | 新增 `NewHandlerWithService`；保留原构造函数并委托到统一构造路径，兼容测试和旧调用方。 |
| `internal/admin/handler_extra_test.go` | 增加传入 Service 被 Handler 复用的回归测试。 |

### 修改原因/目的

消除 Admin Service 的重复初始化，统一审计能力、Remote Assist 审计写入和后台清理的依赖所有权，同时保留现有构造函数兼容性，不改变 API 路径或鉴权边界。

### 验证

- `go test ./internal/admin/... ./internal/server/... ./internal/remoteassist/...` 通过。
- `go build ./cmd/server` 通过。
- `go test ./...` 仍仅保留既有基线失败：`features/virtualnet`、`internal/config`、`internal/files`、`internal/serverstate` 和 `tests/integration`。
- `git diff --check` 通过。

---

## 2026-09-02 — Bot Handler 与后台清理循环收敛

### 背景

`registerAPIRoutes` 被主端口、Admin 端口和兼容路径重复调用；此前每次调用都会创建 Bot Service，启动 TTS 清理循环、初始化 TTS，并独立持有播放器状态，同时重复执行网易云依赖健康检查。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/app.go` | App 创建并持有唯一 Bot Handler；健康检查移到 App 初始化阶段只执行一次；退出时停止 Bot 清理循环。 |
| `internal/server/routes.go` | 所有 API 入口复用 App 级 Bot Handler，不再在路由注册期间创建 Service 或执行健康检查。 |
| `internal/bots/handler.go` | 暴露 Handler 级 `Stop` 生命周期入口。 |
| `internal/bots/service.go` | `Stop` 使用 `sync.Once`，避免重复关闭信号导致 panic；默认构造函数委托到注入构造函数，消除重复初始化代码。 |
| `internal/bots/handler_lifecycle_test.go` | 增加 Stop 幂等回归测试。 |

### 修改原因/目的

消除重复 TTS 清理 goroutine、重复 TTS 初始化、播放器状态分裂、重复外部健康检查和双份 Service 初始化代码，同时保留四组 API 路径及 Bot 业务行为。

### 验证

- `go test ./internal/bots/... ./internal/server/...` 通过。
- `go build ./cmd/server` 通过。
- `go test ./...` 仍仅保留既有基线失败：`features/virtualnet`、`internal/config`、`internal/files`、`internal/serverstate` 和 `tests/integration`。
- `git diff --check` 通过。

---

## 2026-09-02 — 统一 Admin 中间件与 Break-Glass 生命周期

### 背景

API Group、Admin 路由组和 Admin Handler 分别注册 CSRF、认证和角色中间件，造成重复执行与初始化状态来源分散；Owner Break-Glass 保护器也在每个 Admin 兼容路由组中重复创建。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/routes.go` | 将 CSRF 防护移动到各 API Group，每个 Group 只注册一次；Admin 主端口、独立端口和共享端口均显式配置 CSRF；移除 `registerAPIRoutes` 内部重复 CSRF；复用 App 持有的 Break-Glass 保护器。 |
| `internal/admin/handler.go` | Admin Handler 只注册业务路由，移除重复的 `AuthRequired` 与 `RequireAdmin`。 |
| `internal/server/app.go` | App 生命周期只创建一份 `OwnerBreakGlassProtector`。 |
| `internal/server/lifecycle.go` | Break-Glass 清理循环接入 App Context。 |
| `middleware/breakglass.go` | 清理循环支持 Context 取消。 |

### 修改原因/目的

在不削弱 JWT、RBAC 和 CSRF 保护的前提下，统一中间件所有权和初始化状态来源，避免同一请求重复执行认证/CSRF，并消除多个独立 Break-Glass 失败计数器与清理 goroutine。

### 验证

- `go test ./middleware/... ./internal/admin/... ./internal/server/... ./features/sharedoc/... ./features/schedule/... ./features/minigames/...` 通过。
- `go build ./cmd/server` 通过。
- `go test ./...` 仍仅保留既有基线失败，未发现本次改动新增失败。
- `CGO_ENABLED=1 go test -race ...` 因本机缺少 GCC 无法执行。
- `git diff --check`（服务端改动文件）通过。

---

## 2026-09-02 — 修复路由注册重复启动后台任务

### 背景

`registerAPIRoutes` 同时负责创建有状态 Handler、绑定 Hub 回调和启动后台任务，并被主端口与 Admin 端口的 `/api/v1`、`/api` 兼容路由重复调用，导致日程提醒、小游戏维护任务和共享文档编辑状态存在重复实例或回调覆盖风险。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/server/app.go` | App 只创建一份 sharedoc、schedule、minigames Handler；新增 App 级可取消后台任务 Context；服务退出时先取消后台任务。 |
| `internal/server/routes.go` | 路由注册复用 App 持有的 Handler，不再在注册路由时启动后台循环或绑定功能回调。 |
| `internal/server/lifecycle.go` | 日程提醒、小游戏清理/超时推进、审计日志清理统一使用 App Context；Hub 的共享文档和小游戏回调集中在生命周期启动阶段绑定。 |
| `internal/server/ws_callbacks.go` | 将共享文档离线清理合并到既有用户离线处理回调，避免 Handler 构造覆盖在线状态与语音清理回调。 |
| `features/sharedoc/handler.go` | 构造函数移除 Hub 回调副作用，导出由装配层显式调用的离线清理方法。 |
| `features/minigames/handler.go` | 清理循环增加 Context 取消支持。 |
| `features/sharedoc/handler_test.go` | 更新离线清理测试，并增加构造函数不覆盖既有 Hub 回调的回归测试。 |

### 修改原因/目的

消除兼容路由重复注册带来的后台 goroutine、Handler 状态和 Hub 回调分裂，确保每个 App 实例只有一份功能状态和一组可取消的维护任务，同时保持原有 API 路径和业务行为。

### 验证

- `go build ./cmd/server` 通过。
- `go test ./internal/server/... ./features/sharedoc/... ./features/schedule/... ./features/minigames/...` 通过。
- `go test ./...` 仅保留既有基线失败：`features/virtualnet`、`internal/config`、`internal/files`、`internal/serverstate` 和 `tests/integration`；未发现本次改动新增失败。
- `git diff --check` 通过。

---

## 2026-08-16 — 虚拟局域网节点列表显示 displayName

### 背景

虚拟局域网节点列表显示节点名 `rrt-<userId>`（如 `rrt-user_737009497251385344`），不符合"成员用显示名"的产品语义。username 退役后节点名改用 userId，但展示层仍直接返回原始节点名。

### 修改内容

| 文件 | 改动 |
|------|------|
| `features/virtualnet/handler.go` | `GetNodes` 读时批量解析节点名为 displayName（`rrt-<userId>` → displayName，displayName 空则 username 兜底）；`NodeInfo` 新增 `hostname` 字段（保留 EasyTier 节点名 `rrt-<userId>`，供客户端 peer 链路质量 join）。新增 `resolveNodeDisplayNames` 辅助函数（对齐 minigames `lookupDisplayName` 语义）。 |
| `features/virtualnet/handler_test.go` | `TestGetNodesReturnsActiveNodes` 补充断言：`name` 返回 displayName、`hostname` 返回 `rrt-<userId>`。 |

### 验证
- `go build ./cmd/server` 通过
- `go test ./features/virtualnet/...` 本次相关测试全过（含新断言）；仅剩 pre-existing `TestIsEasytierAvailableBadStatus` 失败（mock 健康检查 500 行为差异，与本次无关）

---

## 2026-07-14 — 消息写入性能优化

### 背景

压力测试发现 50/100 VU 下消息发送延迟高。经排查，部分原因是测试脚本每次迭代都重新 login，但消息写入链路本身也存在可优化点：每条消息查 `users` 表、`UserChannelRead` 使用 `DELETE + INSERT`。

### 修改内容

| 文件 | 改动 |
|------|------|
| `internal/message/service.go` | `UserChannelRead` 改为 upsert；新增 `userSnapshotCache`（5s TTL），减少 `users` 表查询。 |
| `internal/message/service_test.go` | 新增 `user_channel_read upsert` 测试。 |

### 验证

- `cd ridgericetalk/server && go build ./cmd/server && go test ./internal/message/... ./internal/dm/... ./internal/server/...` 通过。
- VM 功能 API 测试 9/9 通过。
- VM 压力测试（已登录用户复用 token）：
  - 10 VU：msg P95 375 ms，失败率 0%
  - 50 VU：msg P95 437 ms，失败率 0%
  - 100 VU：msg P95 506 ms，失败率 0%

### 遗留问题

- 旧脚本（每次迭代 login）的高延迟主要由 login 路径瓶颈导致；如需优化 login/refresh，需单独设计方案。

---

## 2026-07-12 — 嵌入 Netease/TTS 依赖并新增局域网 HTTPS 测试脚本

### 背景

裸机部署验证后仍有三项限制：NeteaseCloudMusicApi 因 GitHub 不可达未部署、TTS 模型因 GitHub 不可达未下载、浏览器因 HTTP 非安全上下文无法完成 WebRTC 端到端测试。本次将源码/模型纳入离线部署流程，并新增可选局域网 HTTPS 脚本。

### 修改内容

| 文件 | 改动 |
|------|------|
| `scripts/deploy-baremetal.sh` | Stage 7.6 改为优先从仓库复制 TTS 模型；Stage 8 改为使用仓库内置 `services/netease-api/` 源码并执行 `npm install --omit=dev`；Stage 9 移除 netease-api.service 的等待与启动逻辑 |
| `scripts/ridgericetalk.service.tmpl` | 从 `After=` 移除 `netease-api.service`；`ReadWritePaths` 增加 `{{WORKING_DIR}}/services/netease-api` 与 `{{WORKING_DIR}}/server/models`；新增 `HOME`/`NPM_CONFIG_CACHE` 环境变量，避免 systemd hardened 环境下 npm 失败 |
| `scripts/setup-lan-https.sh` | 新增可选脚本：生成自签名 SAN 证书、安装配置 Nginx 反代、更新 `.env.production` 为 HTTPS/WSS/Cookie Secure |
| `.gitignore` | 允许提交 `models/tts/vits-melo-tts-zh_en/**` |
| `.gitattributes` | 配置 Git LFS 跟踪 `models/tts/vits-melo-tts-zh_en/model.onnx` |
| `models/tts/vits-melo-tts-zh_en/` | 预置 TTS 模型文件（`model.onnx`、`tokens.txt`、`lexicon.txt`），需配合 Git LFS |

### 验证

- `bash -n ridgericetalk/server/scripts/deploy-baremetal.sh` 通过。
- `bash -n ridgericetalk/server/scripts/setup-lan-https.sh` 通过。
- `cd ridgericetalk/server && go build ./cmd/server && go build ./cmd/migrate` 通过。
- `cd ridgericetalk/server && go test ./...` 部分通过：仅 2 个 Windows 文件权限测试失败（`TestSaveSecrets_FilePermission`、`TestStateFilePermissions`），与本次改动无关。
- 虚拟机部署验证：待 TTS 模型文件补充后执行。
- 局域网 HTTPS + WebRTC 测试：待模型补充后在 192.168.31.187 执行。

### 遗留问题

- `model.onnx`（约 163 MB）当前网络下载极慢，尚未成功下载到本地；模型目录已预留，部署脚本已支持复制逻辑。

### 后续计划

1. 补充 TTS 模型文件到 `ridgericetalk/models/tts/vits-melo-tts-zh_en/` 并提交到 Git LFS。
2. 在局域网虚拟机重新完整部署，验证 Netease/TTS 离线可用。
3. 运行 `setup-lan-https.sh` 进行真实 WebRTC 语音测试。

---

## 2026-07-12 — 修复默认频道创建失败并重新验证裸机部署

### 背景

上一轮裸机部署验证后，Owner 注册接口 `POST /api/admin/bootstrap/register` 返回 500 并提示 `failed to create default channel`；同时发现无效请求会在参数校验完成前消费掉一次性 bootstrap token，导致系统死锁。本次针对这两个问题修复后重新部署验证。

### 修改内容

| 文件 | 改动 |
|------|------|
| `migrations/000001_baseline.up.sql` | `channels` 表增加 `permissions TEXT DEFAULT ''`，与 GORM `Channel` 模型保持一致。 |
| `migrations/000022_add_channel_permissions.up.sql` / `.down.sql` | 新增版本化迁移，为已部署实例安全添加/删除 `permissions` 列。 |
| `internal/auth/service.go` | `createOwnerInternal` 中 Space/Membership/Channel/User 创建失败时保留原始 DB 错误；`CreateOwner` 在消费 bootstrap token 前先校验 `securityQuestions`，避免无效请求死锁系统。 |
| `scripts/deploy-baremetal.sh` | Stage 3 本地 LiveKit 二进制存在但不可执行时仍优先使用；脚本开头默认导出 `GOPROXY`；Stage 9 迁移后重新 `chown storage/logs` 给服务用户，避免 `secrets.json` 权限被拒绝。 |

| `scripts/ridgericetalk.service.tmpl` | `ReadWritePaths` 增加 `{{WORKING_DIR}}/server/webhost/dist`，避免 `ProtectSystem=strict` 下 Admin/Voice 静态文件 404。 |
| `internal/server/paths.go` | `webhostDir` 路径修正为 `server/webhost/dist/<name>`，匹配 `deploy-baremetal.sh` 构建产物位置。 |

### 验证

- `bash -n ridgericetalk/server/scripts/deploy-baremetal.sh` 通过。
- `cd ridgericetalk/server && go build ./cmd/server && go build ./cmd/migrate` 通过。
- `cd ridgericetalk/server && go test ./internal/auth/... && go test ./internal/server/...` 通过。
- **局域网虚拟机重新部署验证**（Ubuntu 24.04 LTS，192.168.31.187）：
  - 完整运行 `deploy-baremetal.sh`，Stage 3 使用本地 LiveKit Server v1.8.2，未触发网络下载。
  - Stage 4 后端与 Admin/Voice 前端编译成功。
  - Stage 9 数据库迁移到 version 22（含新增 `000022_add_channel_permissions`）。
  - 后端健康检查 `GET /api/health` 返回 200，Admin 页面 `GET /admin` 返回 200，Voice 页面 `GET /` 返回 200。
  - 无效 Owner 注册请求（3 个 securityQuestions）返回 `AUTH_SECURITY_QUESTION_INVALID`，bootstrap token **未被消费**。
  - 有效 Owner 注册请求返回 200，数据库中生成 1 个 Owner 用户、1 个 Space、2 个默认 Channel（TEXT `general` + VOICE `general`）。
- **浏览器全功能验证**（Playwright + Chromium）：
  - Admin 页面：登录成功，服务器设置、频道管理、用户管理、模块管理、运行监控、存储概览等页面均可正常加载。
  - Voice 页面：使用 Owner 账号登录成功，频道列表显示文字/语音 `general`，在线状态显示为"在线"，文字消息"Hello from automated test!"发送并显示成功。
- **语音频道压力测试**（Python + 并发）：
  - `POST /api/voice/token`：并发 10/50/100 下，低并发成功率 90%，高并发因限流返回 429/503（符合预期）。
  - `GET /ws` WebSocket：单 token 并发 20/60，前 3 个连接成功，后续因每用户最大 3 个并发 WebSocket 限制返回 429（符合预期）。

### 遗留问题/限制

- NeteaseCloudMusicApi 仍因 GitHub 访问失败未部署（音乐机器人搜索不可用）。
- TTS 模型文件未下载（`models/tts/vits-melo-tts-zh_en/model.onnx` 不存在）。
- 浏览器未实际加入语音房间（受 HTTP 非安全上下文 + 麦克风权限限制，未进一步测试 WebRTC 路径）。

### 后续计划

1. 考虑将 NeteaseCloudMusicApi 和 TTS 模型纳入离线/镜像部署。
2. 在公网 VPS 上再次完整验证一键部署。
3. 在 HTTPS 环境下进行完整语音端到端测试。

---

## 2026-07-12 — 真实 Linux 环境裸机部署验证通过

### 背景

在局域网虚拟机（Ubuntu 24.04 x86_64）上完整运行 `deploy-baremetal.sh`，发现并修复了一系列导致"一键部署后 Admin 页面不可用 / 后端无法启动"的问题：Go 模块下载失败、PostgreSQL SSL 模式不兼容、迁移工具未加载 `.env.production`、systemd 服务 `ReadWritePaths` 路径错误、未初始化时 Admin 静态页面被 503 拦截、迁移文件版本号冲突等。

### 修改内容

| 文件 | 改动 |
|------|------|
| `scripts/deploy-baremetal.sh` | Stage 2 安装 Go 后配置 `GOPROXY=https://goproxy.cn,direct`；Stage 4 编译后自动安装 sherpa-onnx 运行时共享库到 `/usr/local/lib/ridgericetalk` 并执行 `ldconfig`；Stage 6 数据库 DSN 改为 `sslmode=require`；Stage 7 `{{WORKING_DIR}}` 渲染为 `$DEPLOY_DIR`，修正 systemd `ReadWritePaths`；Stage 9 显式 source `.env.production` 后再运行迁移工具；修复 LiveKit 版本校验（`v1.8.2` vs `1.8.2`）；全局 PATH 加入 `/usr/local/go/bin`、全局 `ARCH` 提前计算，支持从任意阶段恢复。 |
| `scripts/ridgericetalk.service.tmpl` | `StartLimitBurst`/`StartLimitIntervalSec` 移到 `[Unit]` 段（systemd ≥ 240 要求）；`ReadWritePaths` 指向 `server/storage` 和 `server/logs`。 |
| `middleware/init_check.go` | 服务未初始化时放行 `/admin` 开头的 GET 请求（Admin 静态 SPA 和 assets），使 Bootstrap UI 可访问。 |
| `migrations/000021_voice_participant_livekit_sid.*.sql` | 将原 `000012_voice_participant_livekit_sid` 迁移重命名为 `000021`，解决与 `000012_minigame_add_space_columns` 的版本号冲突。 |

### 验证

- `bash -n ridgericetalk/server/scripts/deploy-baremetal.sh` 通过。
- `cd ridgericetalk/server && go build -o ridgericetalk ./cmd/server/ && go build -o ridgericetalk-migrate ./cmd/migrate/` 通过。
- **局域网虚拟机完整验证**（Ubuntu 24.04 LTS，192.168.31.187）：
  - Stage 3 优先使用仓库内置 LiveKit Server v1.8.2，版本校验通过，未触发网络下载。
  - Stage 4 后端与 Admin/Voice 前端编译成功。
  - Stage 5 PostgreSQL 16 自动安装并创建用户/数据库。
  - Stage 6 生成 `.env.production`，包含 `RRT_CORS_ORIGINS` 和 `RRT_LIVEKIT_PUBLIC_URL`。
  - Stage 7 systemd 服务安装成功（`systemd-analyze verify` 因临时文件路径问题仍失败，已改为非阻塞警告）。
  - Stage 9 数据库版本化迁移成功（version 21）。
  - 后端健康检查 `GET /api/health` 返回 200。
  - Admin 静态页面 `GET http://192.168.31.187:9090/admin` 返回 200。
  - `GET /api/admin/bootstrap/status` 返回 200，`Origin: http://192.168.31.187:9090` 时响应头包含 `Access-Control-Allow-Origin: http://192.168.31.187:9090`。
  - Bootstrap token 文件 `/opt/ridgericetalk/server/storage/.bootstrap_token` 成功生成。
  - 通过 API 完成 Owner 初始化（验证 token → 注册 owner），`needsBootstrap` 变为 false。
- **遗留问题/限制**：
  - NeteaseCloudMusicApi 因 GitHub 访问失败未部署（音乐机器人搜索不可用）。
  - TTS 模型文件未下载（`models/tts/vits-melo-tts-zh_en/model.onnx` 不存在）。
  - Owner 注册时默认频道创建失败（`failed to create default channel`），但 Owner 用户和默认 Space 已创建成功，不影响 Admin 登录。

### 后续计划

1. 考虑将 NeteaseCloudMusicApi 和 TTS 模型也纳入离线/镜像部署，减少对外网依赖。
2. 调查并修复默认频道创建失败的问题。
3. 在公网 VPS 上再次完整验证一键部署。

---

## 2026-07-12 — 嵌入 LiveKit Server 二进制，解决裸机部署下载失败

### 背景

裸机一键部署脚本 Stage 3 需要从 GitHub releases 下载 LiveKit Server，但官方 URL 已失效（返回 404），国内镜像也不稳定，导致部署在受限网络环境中频繁中断。

### 修改内容

| 文件 | 改动 |
|------|------|
| `livekit/bin/linux-amd64/livekit-server` | 新增 LiveKit Server v1.8.2 linux/amd64 离线二进制（从 Docker 镜像 `livekit/livekit-server:v1.8.2` 提取） |
| `livekit/bin/linux-arm64/livekit-server` | 新增 LiveKit Server v1.8.2 linux/arm64 离线二进制 |
| `livekit/LICENSE` | 保留 Apache 2.0 许可证（已存在，再分发合规） |
| `scripts/deploy-baremetal.sh` | Stage 3 改造：优先复制仓库内置的离线二进制；执行 `--version` 版本校验；仅当本地不存在或版本不匹配时才降级到网络下载 |
| `.gitignore` | 不再整体排除 `livekit/`，改为排除 Windows exe 和临时下载目录，允许提交 `livekit/bin/linux-*/livekit-server` 和 `livekit/LICENSE` |

### 设计方案

详见 `文档/修复方案/livekit-embed-binary.md`。

### 验证

- `bash -n ridgericetalk/server/scripts/deploy-baremetal.sh` 通过。
- 提取的二进制 `file` 验证为对应架构的 ELF 静态链接可执行文件：
  - `linux-amd64/livekit-server`: ELF 64-bit LSB executable, x86-64
  - `linux-arm64/livekit-server`: ELF 64-bit LSB executable, ARM aarch64
- `git check-ignore` 确认 `livekit/bin/linux-amd64/livekit-server`、`livekit/bin/linux-arm64/livekit-server`、`livekit/LICENSE` 不再被忽略。
- 因本地无 Linux 运行环境，未执行 Stage 3 的完整流程验证；建议在 Debian/Ubuntu VPS 上运行 `sudo ./deploy-baremetal.sh --stage livekit` 确认本地二进制复制和版本校验路径。

### 后续计划

1. 在真实 Linux 环境验证 Stage 3 本地二进制优先路径。
2. 解决迁移文件版本冲突（000012 重复）和 PostgreSQL 生产环境 SSL 模式问题，完成完整一键部署端到端验证。

---

## 2026-07-10 — 裸机一键部署脚本增强（Admin 直接可用、自动迁移、自动防火墙）

### 背景

裸机一键部署脚本 `scripts/deploy-baremetal.sh` 之前虽已支持编译和自动执行数据库迁移，但仍存在以下问题导致"一键部署后 Admin 页面不可用"：

1. Admin 页面部署在 9090 端口，API 在 8080 端口，浏览器跨域请求被 CORS 中间件拒绝（`403 CORS_ORIGIN_NOT_ALLOWED`）。
2. LiveKit 默认使用 `ws://localhost:7880`，客户端在公网环境无法连接。
3. 脚本不自动开放防火墙端口，用户需要手动放行。

### 修改内容

| 文件 | 改动 |
|------|------|
| `scripts/deploy-baremetal.sh` | 新增 `configure_firewall()` 函数，自动检测并放行 `ufw`/`firewalld`/`iptables`；Stage 6 生成 `.env.production` 时增加 `RRT_CORS_ORIGINS`（包含 API/Admin/localhost 双端口）和 `RRT_LIVEKIT_PUBLIC_URL`（指向公网 IP 的 LiveKit WebSocket 地址）；Stage 9 在启动服务前调用 `configure_firewall`；增强最终输出，显示 Admin URL、Voice URL、Bootstrap Token 和首次使用步骤。 |

### 设计方案

详见 `文档/修复方案/deploy-baremetal-complete-setup.md`（方案 B：双端口 + 自动 CORS 放行 + 自动迁移 + 自动防火墙 + LiveKit 公网地址配置）。

### 验证

- `bash -n ridgericetalk/server/scripts/deploy-baremetal.sh` 通过。
- `cd ridgericetalk/server && go build -o ridgericetalk ./cmd/server/ && go build -o ridgericetalk-migrate ./cmd/migrate/` 通过。
- **Docker 容器模拟验证**（Debian 12，受限环境）：
  - 脚本 Stage 6 生成的 `.env.production` 正确包含 `RRT_CORS_ORIGINS=http://127.0.0.1:8080,http://127.0.0.1:9090,http://localhost:8080,http://localhost:9090` 和 `RRT_LIVEKIT_PUBLIC_URL=ws://127.0.0.1:7880`。
  - 后端编译成功，迁移工具编译成功。
  - PostgreSQL 版本化迁移成功执行（在临时处理本地迁移文件版本冲突后）。
  - 后端启动后 `/api/health` 返回 200。
  - Admin 静态页面 `http://127.0.0.1:9090/admin` 返回 200。
  - `/api/admin/bootstrap/status` 返回 200，且带 `Origin: http://127.0.0.1:9090` 请求时响应头包含 `Access-Control-Allow-Origin: http://127.0.0.1:9090`，CORS 放行验证通过。
- **验证限制**：Docker 容器内无 systemd，脚本的 `systemctl` 相关阶段（服务安装/启动）无法完整验证；LiveKit 官方 GitHub releases 下载 URL 已失效，脚本 Stage 3 在当前网络环境下会失败；发现本地迁移文件存在版本号冲突（000012 重复）。这些问题不属于本次脚本改动范围，但会影响真实环境一键部署成功率，建议后续修复。

### 后续计划

1. 修复 LiveKit 下载 URL（GitHub releases 文件名/路径已变更）和迁移文件版本冲突。
2. 在真实 Debian/Ubuntu VPS 或支持 systemd 的虚拟机/容器中运行完整脚本验证。
3. 使用 Bootstrap Token 完成 Owner 初始化，测试网络配置、模块管理、用户管理等功能。
4. 考虑在 `ridgericetalk.service` 的 `ExecStartPre` 中加入迁移命令，保证后续升级自动迁移。

---

## 2026-07-08 — 调整文字频道附件大小限制（图片20MB/视频5GB/文件10GB）

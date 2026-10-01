# RidgeRiceTalk Server 开发修改日志

> 按时间倒序记录所有代码修改

---

## 2026-06-22

### Phase 3 屏幕共享 Windows 客户端集成（T18/T46）

依据 `kimi/designs/batch-4-p1-voice-media.md` 与 `kimi/designs/batch-6-p2-frontend.md`，完成 Windows 客户端屏幕共享端到端闭环。

#### 修改内容

- `ridgericetalk-win/src/shared/api-core.ts`
  - 所有 API 请求统一附加 `X-Client-Type: desktop`，使服务端能按设计文档区分桌面端与 Web 端。
- `ridgericetalk-win/src/renderer/stores/voice/voiceStore.ts`
  - 新增 `setScreenshareVideoElement` action，保存当前用于渲染的 `<video>` 元素。
  - `handleTrackSubscribed` 收到 `screen_share` 视频轨道时，自动 attach 到 `<video>` 元素。
  - `handleTrackUnsubscribed` / `leaveVoice` / `cancelJoin` 时 detach 屏幕共享轨道并清理引用。
  - `joinVoice` 成功后调用 `getScreenshareStatus`，若当前有共享则提前显示"观看屏幕共享"提示。
  - `toggleScreenshare` 在 API 调用失败时回滚 LiveKit 屏幕共享状态，并向上抛出错误。
- `ridgericetalk-win/src/renderer/pages/MainLayout/VoiceRoom/VoiceRoom.tsx`
  - 进入观看模式后通过 `useEffect` 将 `<video>` ref 注册到 store。
- `ridgericetalk-win/src/renderer/pages/MainLayout/MainLayout.tsx`
  - "共享"按钮捕获 `toggleScreenshare` 错误，并通过 toast 提示用户。
- `ridgericetalk/server/internal/screenshare/handler_test.go`
  - 新增服务端 handler 单元测试，覆盖桌面端允许发起共享、Web 端返回 `SCREENSHARE_WEB_FORBIDDEN`、status/stop 正常返回。

#### 验证结果

- `go test ./internal/screenshare`：新增测试通过。
- `go test ./...`：全部通过。
- `npx tsc --noEmit`（Windows 客户端）：通过。
- `npm test`（Windows 客户端）：89 个测试全部通过。
- `npx vite build`（Windows 客户端）：构建成功。
- 浏览器验证：
  - `web/voice` Playwright E2E 测试：6 个测试全部通过（注册、登录、频道 API、落地页等）。
  - 管理端 `http://localhost:9090/admin` 复测：页面加载正常，登录表单可交互，无 API 错误。
- 手动 API 验证：
  - `GET /api/screenshare/status` 返回 `active: false`。
  - `POST /api/screenshare/start` 带 `X-Client-Type: desktop` 返回 200。
  - `POST /api/screenshare/start` 带 `X-Client-Type: web` 返回 403 `SCREENSHARE_WEB_FORBIDDEN`。
  - `POST /api/screenshare/stop` 返回 200。

---

## 2026-06-22

### Phase 2 稳定性修复验证与测试补漏

本阶段原计划覆盖 CSRF 强制校验、Admin 登录角色检查前移、WebSocket Hub 并发安全、认证与密码重置安全、JWT 算法校验、启动健壮性、事务与代码质量补漏。

经代码与测试复查，上述能力在现有实现中**已覆盖**，因此本阶段以“验证 + 补测试 + 文档”为主，未改动核心逻辑。

#### 验证结果

- `go test ./...`：全部通过。
- `go test -race -count=1 ./...`：全部通过，无数据竞争。
- `go build ./...`：构建成功。
- 浏览器复测 `http://localhost:9090/admin`：页面加载正常，登录表单可交互，无 API 404/403 错误。

#### 新增测试

- `middleware/middleware_test.go`
  - `TestCSRFProtectionAdminLoginSkip`：验证 `/admin/login` 公开登录接口跳过 CSRF，避免 Admin SPA 初始化后无法登录。
  - `TestParseTokenAlgorithmConfusion`：验证 `alg=none` 与 `HS512` 等算法混淆攻击被 `ParseToken` 拒绝。
- `internal/realtime/hub_test.go`
  - `TestHubConcurrentRegisterUnregister`：并发注册/订阅/注销压力测试，验证 Hub map 操作无线程安全问题。

---

## 2026-05-27

### 安全检查与架构审查全面修复（P0/P1）

基于后端安全检查报告和前端实现审查报告，修复了 **4 个高危安全问题** 和 **4 个中危问题**，以及 **3 个构建脚本路径问题**。

#### 🔴 高危修复

**1. Bootstrap Token 明文日志泄露**
- **文件**: `cmd/server/main.go`, `scripts/start-server.ps1`
- **修改**:
  - 移除 `log.Info("Bootstrap Token", "token", token)` 明文日志输出
  - 改为将 token 写入受保护文件 `storage/.bootstrap_token`（权限 0600）
  - `start-server.ps1` 从该文件读取 token 而非日志解析
  - `ValidateBootstrapToken` 验证成功后自动删除该文件
- **风险消除**: 日志文件中不再包含任何 bootstrap token 明文

**2. Refresh Token 刷新后旧 Token 仍有效**
- **文件**: `internal/auth/service.go`
- **修改**: `RefreshToken` 生成新 token pair 前，先执行 `s.db.Where("refresh_token = ?", tokenHash).Delete(&model.UserSession{})`
- **风险消除**: 旧的 refresh token 在刷新后立即失效，防止盗用 token 无限期使用

**3. 速率限制中间件未实际启用**
- **文件**: `cmd/server/main.go`, `internal/auth/handler.go`
- **修改**:
  - 全局 API 路由添加 `middleware.RateLimit(globalLimiter)`（60 req/min, burst 10）
  - `/auth/*` 路由组额外添加 `middleware.RateLimit(middleware.AuthRateLimit())`（5 req/min, burst 3）
- **风险消除**: 认证端点和全局 API 均受速率限制保护，防止暴力破解和 DDoS

**4. 通用文件上传缺少类型验证**
- **文件**: `internal/files/handler.go`
- **修改**:
  - 上传时读取前 512 字节进行 magic number 检测（`storage.ValidateFileType`）
  - 白名单限制：图片(jpeg/png/gif/webp/svg)、音频(mp3/wav/ogg)、视频(mp4)、文档(pdf/zip)
  - 拒绝可执行文件、脚本、HTML 等危险类型
  - 使用检测到的 MIME 类型替代客户端提供的不可信 `Content-Type`
- **风险消除**: 防止上传恶意文件（PHP、HTML、可执行文件、SVG XSS 等）

#### 🟠 中危修复

**5. 生产环境强制 JWT/CSRF 密钥**
- **文件**: `internal/config/config.go`
- **修改**: `cfg.Env == "production"` 时，若 `JWTSecret` 或 `CSRFTokenSecret` 为空，直接返回错误而非使用硬编码默认值
- **风险消除**: 防止生产环境使用可预测的默认密钥

**6. RateLimiter 内存泄漏**
- **文件**: `middleware/rate_limit.go`
- **修改**:
  - 添加 `defaultMaxClients = 10000` 上限
  - `Allow()` 中当客户端数达到上限时触发 `cleanupLocked()`
  - `cleanupLocked()` 删除超过 2 个时间窗口未活跃的客户端
- **风险消除**: 防止分布式攻击或长期运行导致 OOM

**7. WebSocket 空 Origin 允许**
- **文件**: `internal/realtime/hub.go`
- **修改**: `CheckOrigin` 中，生产环境（`RRT_ENV=production`）拒绝空 Origin 请求
- **风险消除**: 防止从本地文件、代理环境、旧版浏览器发起的 CSRF 攻击

**8. MFA 敏感操作无密码二次确认**
- **文件**: `internal/auth/handler.go`
- **修改**:
  - `SetupMFA` 请求体增加 `password` 字段，验证密码后才生成 TOTP Secret
  - `DisableMFA` 请求体增加 `password` 字段，验证密码后才允许禁用
- **风险消除**: 防止 Access Token 被盗后攻击者绑定/解绑 MFA 设备

**9. 安全问题答案明文存储**
- **文件**: `internal/auth/service.go`
- **修改**: 注册时对 `SecurityAnswer1` 和 `SecurityAnswer2` 使用 `crypto.HashPassword`（bcrypt）哈希后存储
- **风险消除**: 数据库泄露时安全问题答案不会明文暴露

#### 🔧 构建脚本修复

**10. Makefile 输出路径统一**
- **文件**: `server/Makefile`
- **修改**: `build`/`build-linux`/`build-windows` 统一输出到 `build/` 目录
- **清理**: `clean` 同时清理 `build/` 目录和遗留的根目录二进制

**11. start-dev.sh 路径不一致**
- **文件**: `server/scripts/start-dev.sh`
- **修改**: 编译和执行路径统一为 `build/ridgericetalk-dev.exe`

**12. start-server.ps1 二进制查找路径**
- **文件**: `server/scripts/start-server.ps1`
- **修改**: 查找路径改为 `build/ridgericetalk.exe`，token 读取改为 `storage/.bootstrap_token` 文件

---

## 2026-05-27

### Windows 双击启动体验优化
- **需求**: 双击 `ridgericetalk.exe` 后窗口应持久存在，启动成功显示访问信息，启动失败显示错误并等待按键关闭
- **文件**: `cmd/server/console_windows.go`, `cmd/server/console.go`, `cmd/server/main.go`
- **检测方式**: `kernel32.GetConsoleProcessList` 返回控制台进程数，1 个 = 双击启动，≥2 个 = 命令行启动
- **退出保护**: `exitWithPause(code)` 仅在 `ownConsole && code != 0` 时 pause，正常 `Ctrl+C` 退出不触发
- **启动信息**: `printStartupInfo(cfg)` 在双击启动成功后输出环境、端口、LiveKit、访问地址等横幅

---

## 2026-05-19

### 音乐机器人播放器重新设计（频道选择）
- **需求**: 播放器区域右侧取消歌词显示，改为音乐机器人频道选择
- **文件**: `web/voice/src/App.tsx`, `ridgericetalk/Web端 UI 设计文档 - Voice.md`, `ridgericetalk/Windows 客户端 UI 设计文档.md`
- **文档更新**:
  - Voice 文档 9.4 节：大播放器布局 ASCII 图右侧改为"播放频道"列表
  - Windows 文档 §10.3：大播放器右侧从歌词区域改为播放频道选择
- **代码修改**:
  - 新增 `botChannelId` state，默认 `'default'`
  - 所有 Bot API 调用（`getBotStatus`/`getBotQueue`/`addToBotQueue`/`botQueueClear`）从硬编码 `'default'` 改为使用 `botChannelId`
  - 大播放器右侧：移除 `bpl-lyrics` div，替换为语音频道选择列表（`channels.filter(c => c.type === 'VOICE')`）
  - 选中态高亮 + "当前"标注，空态显示"暂无语音频道"
  - Bot 状态刷新 useEffect 加入 `botChannelId` 依赖，切换频道后自动重载

### UI 设计文档增量更新（Voice + Admin）
- **文件**: `ridgericetalk/Web端 UI 设计文档 - Voice.md`
- **修改**: 按当前实际实现更新文档中不一致的部分
  - 成员栏：移除搜索框/折叠描述，改为平铺展示
  - 延迟显示：更新为真实 HTTP RTT 测量说明
  - 登录认证：更新为 refresh token 自动刷新机制，"记住我"标签
  - 网易云扫码：添加未配置 API 时的提示说明
  - Bot 面板本地上传：补充完整文件上传交互流程
  - 登出流程：补充清除 token 和同步离线状态
  - 修改记录：新增 M-33 ~ M-39 条目

### 成员栏简化 + 网易云扫码提示 + 本地上传实现
- **问题1**: 成员栏有搜索框和在线/离线折叠功能，用户要求取消
- **问题2**: 未配置网易云 API 时，扫码显示 SVG 占位图而非二维码
- **问题3**: 本地上传按钮点击无反应（前端 TODO + 后端空桩函数）
- **文件**: `web/voice/src/App.tsx`, `web/shared/api-core.ts`, `ridgericetalk/server/internal/bots/handler.go`, `ridgericetalk/server/internal/bots/service.go`
- **修改**:
  - 成员栏：移除搜索框、在线/离线分组、展开/收起按钮，改为按角色+在线状态排序平铺展示
  - 网易云扫码：检测返回的 `qrimg` 是否为 SVG 占位图（`data:image/svg+xml` 开头），是则显示"网易云 API 未配置"提示
  - 本地上传前端：添加隐藏 `<input type="file">`，点击上传按钮触发选择，调用 `uploadAudio` API
  - 本地上传后端：`UploadAudio` 解析 multipart 保存到 `./storage/uploads/audio/` 并写入 `BotUploadAudio` 表；`GetUploads` 查询列表；`DeleteUpload` 删除文件和记录
- **预期**: 成员栏简洁展示；未配置 API 时明确提示；本地上传可正常选择、上传、删除

### 退出语音频道后用户仍显示在参与者列表
- **问题**: 用户点击"离开频道"后，左侧频道列表和语音房间中仍然显示该用户的头像/信息
- **文件**: `web/voice/src/App.tsx`
- **根因**: `handleLeaveVoice` 和 `handleCancelJoin` 只调用了 `leaveVoiceChannel` API 并重置了本地状态，但没有从 `channelParticipants` state 中移除当前用户；且 `channelParticipants` 的定时刷新 `useEffect` 条件是 `voiceChannelId && isInVoiceRoom`，离开后刷新停止，导致旧数据残留
- **修改**:
  - `handleLeaveVoice`: 调用 API 后，从 `channelParticipants[voiceChannelId]` 中过滤掉 `user.id`
  - `handleCancelJoin`: 同上
  - `handleJoinVoice`: 切换频道离开旧频道时，也从旧频道的 `channelParticipants` 中移除当前用户
- **预期**: 离开频道后，用户立即从参与者列表中消失

### 记住密码 / 自动登录修复（Refresh Token 机制）
- **问题**: JWT Access Token 默认有效期仅 15 分钟，用户勾选"记住密码"后刷新页面，token 已过期导致无法自动登录；前端未实现 Refresh Token 自动续期
- **文件**: `web/shared/api-core.ts`, `web/voice/src/App.tsx`
- **修改**:
  - `api-core.ts`: 新增 `refreshAccessToken()` 函数，带并发控制（`refreshingPromise` 确保多个请求同时过期时只触发一次刷新）
  - `api-core.ts`: 改造 `api()` 函数，检测到 `AUTH_TOKEN_EXPIRED` 时自动调用 `/api/auth/refresh` 获取新 token，并重试原请求
  - `api-core.ts`: 更新 `login`/`register` 返回类型，包含 `refreshToken`
  - `App.tsx`: 登录/注册成功后同时保存 `rrt_token` 和 `rrt_refresh_token`
  - `App.tsx`: 登出时同时移除 `rrt_refresh_token`
  - `App.tsx`: 自动登录（`getMe`）成功后同步设置 `"online"` 状态
  - `App.tsx`: 复选框标签 `"记住密码（自动登录）"` → `"记住我（自动登录）"`
  - `App.tsx`: 修复不勾选 rememberMe 时 token 被过早移除导致 `apiPut` 设置状态失败的问题（现在始终保存 token，仅控制 email 是否保存）
- **预期**: token 过期后前端自动静默刷新，用户无感知；刷新页面后自动登录正常

---

## 2026-05-19

### 延迟显示修复
- **问题**: `navigator.connection.rtt` 返回浏览器估算值（150ms+），不是实际 HTTP RTT
- **文件**: `web/voice/src/App.tsx`
- **修改**: 移除 `navigator.connection.rtt` 优先逻辑，始终使用 `fetch('/api/health')` HEAD 请求测量实际 RTT
- **预期**: 本地服务器延迟显示为 1-5ms

### 成员栏在线状态同步
- **问题**: 用户登录后成员栏显示离线，因为从未更新 `UserPresence` 状态
- **文件**: `web/voice/src/App.tsx`
- **修改**:
  - 登录成功后调用 `PUT /api/users/:id/status` 设置 `"online"`
  - 登出时调用设置 `"offline"`
  - 添加 `beforeunload` 事件，页面关闭前发送 `sendBeacon` 设置 `"offline"`
  - 添加 30 秒间隔定期刷新成员列表

### DEV_LOG.md 创建
- **文件**: `ridgericetalk/server/DEV_LOG.md`
- **说明**: 建立修改日志文件，追溯变更历史

---

## 2026-05-19

### 登录字段名不一致修复
- **问题**: 前端发送 `{ email, password }`，后端 `LoginRequest` 期望 `{ username, password }`
- **文件**: `web/shared/api-core.ts`, `ridgericetalk/server/internal/auth/service.go`
- **修改**:
  - 前端 `login()` 改为发送 `{ username: email, password }`
  - 后端 `Login` 服务支持用 username 或 email 查找用户（fallback 查询）

---

## 2026-05-19

### 登录页面演示账号清理
- **问题**: 登录页存在硬编码演示账号 `user@example.com / password`
- **文件**: `web/voice/src/App.tsx`
- **修改**: 移除邮箱/密码输入框的 `defaultValue`，删除演示账号提示文字

---

## 2026-05-19

### NoRoute 静态资源 404 修复
- **问题**: 不存在的静态资源（如旧缓存的 JS）返回 `index.html`，导致浏览器解析错误、白屏
- **文件**: `ridgericetalk/server/cmd/server/main.go`
- **修改**:
  - 对 `/assets/*`、`.js`、`.css`、`.svg` 等静态资源，文件不存在时返回 404
  - SPA fallback 返回 `index.html` 时添加 `Cache-Control: no-cache, no-store, must-revalidate`

---

## 2026-05-19

### 网易云 QR 登录 + 语音参与者 + 延迟 + 成员栏修复（第二轮）
- **文件**:
  - `ridgericetalk/server/internal/bots/handler.go` — 添加 `/netease/*` 路由别名
  - `ridgericetalk/server/internal/voice/handler.go` — `GetParticipants` 返回 `{ participants: [...] }`
  - `web/voice/src/App.tsx` — 添加 QR 状态轮询 useEffect、修复延迟测量为 `apiGet`
  - `ridgericetalk/server/internal/bots/service.go` — 修复 QR 占位图 base64 编码
  - `web/voice/src/components/MinigamesPanel.tsx` — 清理硬编码 mock 数据

---

## 2026-05-19

### Voice 前端功能缺陷修复（第一轮）
- **问题**: 成员列表数据格式不匹配、延迟硬编码、网易云 QR 404、小游戏 mock 未清理
- **文件**:
  - `web/shared/api-core.ts` — `getMembers` 返回类型改为 `data: any[]`
  - `web/voice/src/App.tsx` — 修复成员解析、添加 HTTP RTT 测量
  - `ridgericetalk/server/internal/bots/handler.go` — 添加网易云 QR 路由
  - `ridgericetalk/server/internal/bots/service.go` — 添加网易云 QR 代理实现
  - `web/voice/src/components/MinigamesPanel.tsx` — 改为从 API 获取游戏列表

---

## 2026-05-18

### 管理端初始化安全修复
- **问题**: Bootstrap Token 明文存储、无过期、密码策略弱
- **文件**:
  - `ridgericetalk/server/internal/auth/handler.go` — bcrypt 哈希 + 24h 过期
  - `ridgericetalk/server/internal/auth/service.go` — 两步验证流程
  - `ridgericetalk/server/middleware/initcheck.go` — InitCheck 白名单
  - `ridgericetalk/server/pkg/errors/errors.go` — JSONError 状态码映射

---

## 2026-05-18

### 启动脚本
- **文件**: `ridgericetalk/server/scripts/start.sh`, `kill-server.sh`, `start-dev.sh`
- **功能**: 自动清理旧进程、编译启动

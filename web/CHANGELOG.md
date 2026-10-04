# RidgeRiceTalk Web 修改记录

> 按时间倒序记录 web 仓库（web/voice、web/admin、web/web-win）的设计变更和代码修改。

---

## 2026-10-02 — shared/admin：语音音质档类型正式化（C 档 U20 批次配套）

### feat(shared): Channel 新增 voiceQuality 正式类型；admin 移除 as any 断言

- **修改类型**：新增/变更
- **修改位置**：`shared/types.ts`、`admin/src/components/ChannelsPanel.tsx`
- **背景（问题）**：DES-20261002-01 §5 C 档——win 客户端上一批（3b60106）实现语音音质档时 `@shared/types` 的 Channel 尚未声明 `voiceQuality` 字段，win 侧只能结构化断言读取（且连带发现 channelStore 映射丢字段致频道档位覆盖失效，win 仓同批修复）；本批把类型缺口在真源补齐，正式化两端契约。
- **修改内容**：①`shared/types.ts` 新增 `VoiceQuality = 'fluent' | 'standard' | 'high' | 'ultra'` 枚举（服务端权威：POST /channels 落库 voice_quality、GET /channels 回传；创建语音频道默认 standard）并在 `Channel` 接口补 `voiceQuality?: VoiceQuality`（仅 VOICE 频道，老服务端/历史数据可缺省）；②`admin/ChannelsPanel.tsx` 音质下拉的 `(c as any).voiceQuality` 断言随正式类型移除。win 仓 `src/shared` 镜像经 `node scripts/sync-shared.mjs` 同步；未动 `ridgericetalk/web/`（编排方统一对齐）。
- **验证结果**：web/admin `tsc && vite build` 通过（D 盘副本 `D:\RiceTalk-D\rrt-web\admin`，shared+ChannelsPanel 同步后构建）；win 侧 `npx tsc --noEmit` 通过，既有 voiceQuality.test.ts 5 用例（含非法值回落）不改全绿，D 盘全量 vitest 833 passed / 8 failed（均为基线已知红，无回归）。

---

## 2026-10-02 — shared：B7 自定义服务器表情 API（B7 客户端面）

### feat(shared): 新增空间自定义表情 API 封装（getEmojis/createEmoji/deleteEmoji/fetchEmojiBlob）与 ServerEmoji 类型

- **修改类型**：新增
- **修改位置**：`shared/api-core.ts`、`shared/types.ts`
- **背景（问题）**：DES-20261002-01 §4.7（B7 轻量版）——服务端 `/api/v1/emojis` 三路径已就绪（另一代理完成），客户端面需要 shared 层封装；`GET /emojis/:id/file` 需要 Authorization 鉴权，`<img src>` 带不了 header，必须提供「鉴权 fetch → Blob」通道供 emojiStore 转 objectURL 缓存。
- **修改内容**：①`shared/types.ts` 新增 `ServerEmoji`（id/name/spaceId/creatorId/url/createdAt；url 为相对路径可缺省，客户端按 `/api/v1/emojis/:id/file` 兜底）；②`shared/api-core.ts` 新增 `getEmojis(spaceId?)`（GET，spaceId 省略走 JWT 当前空间）、`createEmoji(spaceId, name, file)`（multipart name+file，走既有 upload 通道：Bearer/CSRF/401 刷新/XHR）、`deleteEmoji(id)`（DELETE）、`fetchEmojiBlob(url)`（薄封装复用 `downloadBlob` 的鉴权与 401 刷新，objectURL 生命周期由调用方管理）。业务错误码契约：EMOJI_PERMISSION_DENIED/EMOJI_NOT_FOUND/EMOJI_NAME_TAKEN/EMOJI_LIMIT_REACHED/EMOJI_FILE_TOO_LARGE/EMOJI_FILE_INVALID/EMOJI_NAME_INVALID。win 仓 shared 镜像经 `node scripts/sync-shared.mjs` 同步；未动 `ridgericetalk/web/`（编排方统一对齐）。
- **验证结果**：web/admin `tsc && vite build` 通过（D 盘副本 `D:\RiceTalk-D\rrt-web\admin`，shared 源同步后构建）；win 侧 `npx tsc --noEmit` 通过 + apiCoreEmojiContract.test.ts 9 用例（GET 路径与 spaceId query/multipart 字段与头/DELETE 路径/错误码透传/鉴权 blob 通道）。

---

## 2026-10-02 — shared：消息搜索与邻域页 API（B4/U18）

### feat(shared): 新增 searchAllMessages 全局消息搜索，getMessages 支持可选 around 参数

- **修改类型**：新增
- **修改位置**：`shared/api-core.ts`、`shared/types.ts`
- **背景（问题）**：DES-20261002-01 §4.3（B4/U18）——服务端已有 `GET /api/v1/messages/search`（全局、按调用者可见文字频道过滤）与 `GET /api/v1/channels/:id/messages?around=<messageId>`（邻域页），客户端 api-core 无对应封装，Windows 客户端搜索入口无法落 shared 纪律（§7：api-core 唯一真源在 web/shared）。
- **修改内容**：①`shared/api-core.ts` 新增 `searchAllMessages(q, limit=25)`——GET `/api/messages/search?q=&limit=`，按服务端 `GlobalSearchResult` wire 形状（snake_case：message_id/channel_id/channel_name/author_id/author/content/created_at，见 server/internal/message/service.go）归一化为 camelCase `MessageSearchResult`，total 缺省回退 results.length；②`getMessages` 第二参升级为 `opts?: string | { cursor?, around?, limit? }` 对象（around 走邻域页语义：含目标、createdAt 升序、目标不存在/跨频道 404），旧签名 `(channelId, cursor?, limit?)` 保持兼容（已归档 voice 恢复后旧调用不破坏）；③`shared/types.ts` 新增 `MessageSearchResult` 接口。win 仓 shared 镜像经 `node scripts/sync-shared.mjs` 同步。
- **验证结果**：web/admin `tsc && vite build` 通过（D 盘副本 `D:\RiceTalk-D\rrt-web\admin`，Z 盘构建不可用 E1；先复制 admin+shared 排除 node_modules/dist，npm install 后构建）；win 侧 `npx tsc --noEmit` 通过 + apiCoreSearchContract.test.ts 6 用例（归一化/limit 透传/URL 编码/around 透传/旧签名兼容/无参数不带 query）。

---

## 2026-10-02 — U14：admin bootstrap 网络步初值智能默认

### fix(admin): 初始化向导网络步初值改从 /server/info 推导，废除 443+HTTPS 假值

- **修改类型**：修复
- **修改位置**：`admin/src/components/LoginForm.tsx`、`admin/src/components/__tests__/LoginForm.test.tsx`、`shared/types.ts`
- **背景（问题）**：bootstrap 向导网络步表单初值为 `EMPTY_NETWORK_CONFIG`（显式 443+TLS:true 假值），远程部署新手直接完成初始化会把 443 写进 server-state，对外广播地址全错并覆盖服务端智能默认（N25 已让服务端缺省字段取实际端口，但表单显式值会覆盖之）。
- **修改内容**：①进入网络步时调用 `GET /server/info` 推导初值——externalHost 取 location.hostname 兜底、HTTP 端口取 `apiPort`、管理端口取 `adminPort`、LiveKit WS 端口优先取新增的 `livekitPort`（回退解析 livekitUrl，再回退 7880）、媒体 UDP 取 `mediaUdpPort`、HTTPS 取 `useHttps`；接口不可达时保持 location 兜底，不再注入 443 假值。②`shared/types.ts` 的 `ServerInfoResponse['data']` 补 `livekitPort?: number`（服务端配套字段见 ridgericetalk 仓 `1685df0`）——**注意**：/api/server/info 实际返回裸字段对象（无 {code,data} 信封），组件按双形态兼容读取；win 仓 shared 镜像已同步。③新增组件测试：mock 裸形态响应，断言网络步初值 18080/17880/17882 且 HTTPS 未勾选。
- **验证结果**：web/admin vitest 92/92（含新用例 5/5）+ `tsc && vite build` 通过；演练机 79a6ca8-u14 实例 GUI 实测（browser-use）：全新实例向导网络步默认值 host=192.168.31.187、HTTP=18080、WS=7880、UDP=7882、HTTPS 未勾选，全程未碰表单完成初始化后 server-state.json 落盘 `{18080, 7880, 7882, 19090, useHttps:false, clientAccessEnabled:true}` 零 443，admin 直接进入主界面。

---

## 2026-10-01 — shared：会话管理撤销与日程邀请 API（A4-S3/A8-S3）

### fix(admin): 初始化表单补显示名字段（N24 配套）

- **修改类型**：修复
- **修改位置**：`admin/src/components/LoginForm.tsx`、`shared/api-core.ts`
- **背景（问题）**：N24——admin bootstrap 表单只有 用户名/邮箱/密码/空间名称，而后端 CreateOwner 强制 displayName → 全新部署初始化必 400。
- **修改内容**：①表单新增「显示名（可选，默认同用户名）」输入框并随请求提交；②shared 的 adminBootstrapRegister 参数类型补 `displayName?: string`。
- **验证结果**：tsc 通过、vite build 通过（D 盘 RiceTalk-D 工作副本）；演练机 GUI 实测表单出现新字段、显示名留空提交成功（服务端缺省取 username）。服务端配套修复见 ridgericetalk 仓 2deabee。


### 修改类型
新功能（跨端共享源）

### 修改位置
`web/shared/api-core.ts`

### 修改内容
`revokeOtherAuthSessions`（POST revoke-others）与 `AuthSessionItem.ip`；`ScheduleEventInvite` 类型、`getEventInvites`、`respondEventInvite`、`createScheduleEvent` 的 `inviteeIds` 透传（不传不含该键，旧服务端兼容）。

### 验证结果
win 侧 sync 后三处 diff 一致；web/admin tsc 过；win 侧契约用例 8 例绿。

---

## 2026-10-01 — admin：频道分组内联编辑（A1-S4）与全局分享子视图（E4-UI）

### 修改类型
新功能（管理后台）

### 修改位置
`web/admin/src/components/ChannelsPanel.tsx`、`web/admin/src/components/StoragePanel.tsx`、`src/types/admin.ts`、`src/utils/format.ts`、两个面板测试文件

### 背景（问题）
①服务端 A1-S1（c81e26f）开放频道 sortGroup 读写后，管理端无编辑入口；②服务端 E4（同提交）提供 `/api/admin/cloudfs/shares` 全局列表/撤销端点后，管理端无对应视图（known_issues E4）。

### 修改内容
- ChannelsPanel：文字/语音表各加「分组」列（内联编辑，Enter/Esc，服务端 400 行内 `role=alert` 且保持编辑态）；列表按 未分组在前→组名码点序→position 排序（对齐服务端 SQL 规则）。
- StoragePanel：「概览/全局分享」子视图切换（既有内容不动）；全局分享懒加载+分页（limit 50，末页按本页条数<50 判定——服务端无 total 字段）+9 列表格+撤销 confirm→DELETE→刷新；404/已撤销幂等按成功。
- 类型 `AdminCloudShareItem` 与服务端字段一一对应；`formatShareTime` UTC 可读格式。admin 分享 API 未进 web/shared（管理端专属，StoragePanel 内用共享原语 apiGet/apiDelete 拼路径，避免 shared/win 端联动）。

### 验证结果
`npx tsc --noEmit` 0 错误；vitest 10 文件 85/85（新增 10 用例，变异 2 项红后恢复）；`npx vite build` 通过（D 盘副本，Z 盘 vite 因 E1 环境债不可用）。

---

## 2026-09-30 — shared：云文件分享链接 API 与纯函数（DES-2026-0912-05 §9，任务 T10）

### 修改类型

新增（跨端共享源 `web/shared/api-core.ts`，桌面端经 `sync:shared` 同步）

### 修改内容

- **新增类型**：`CloudFileShareCreateOptions`（fileId/expiresInDays/password/maxDownloads）、`CloudFileShareCreated`（含仅此一次的明文 `token`、`sharePath`、PublicAddress 下发时才有 `url`）、`CloudFileShareItem`（列表条目，永不含 token/链接）。
- **管理三条（鉴权，与既有 CloudFS 同 `/api` 前缀）**：`createCloudFileShare`（POST `/api/cloudfs/share`）、`getCloudFileShares`（GET `/api/cloudfs/shares?fileId=`）、`revokeCloudFileShare`（DELETE `/api/cloudfs/share/:shareId`）。注意公开三条（元信息/verify/download）只在 `/api/v1/share` 下、**无 legacy 别名**，链接必须以服务端拼好的 `sharePath` 为准。
- **纯函数**：`buildShareLink`（链接优先级：服务端 `url` > API base + `sharePath` > 相对 sharePath）、`validateShareForm`（与服务端 ShareOptions 同口径的本地预校验：有效期 0=默认 7 天/1..30、密码 ≤128、次数 ≥0）、`describeShareStatus`（已撤销>已过期>次数用尽>文件已删除>有效中）、`formatShareDownloads`、`mapShareError`（chinese_message 优先透传，技术性文案按错误码映射中文兜底）。

### 验证结果

`npm run build` 因 NAS 环境不可用（已知环境债，按任务约定跳过）；以 `diff web/shared/api-core.ts ridgericetalk-win/src/shared/api-core.ts` 逐字节一致为准。消费方实现与全量回归见 `ridgericetalk-win/docs/CHANGELOG.md` 同日条目（测试台 438/438，含 api-core 分享纯函数 13 例）。

---

## 2026-09-30 — shared：markRead 封装 + 消息契约修复（M9 第②③条，任务 T5）

### 修改类型

修复 + 新增（跨端共享源 `web/shared/api-core.ts`，桌面端经 `sync:shared` 同步）

### 修改内容

- **新增 `markChannelRead(channelId, lastMessageId?)`**：POST `/api/channels/:id/mark-read`（M16 路由，此前无任何客户端封装）。lastMessageId 缺省发 `{}`（服务端自动取频道最新一条）；读到中间某条时传该条 ID，服务端按「该条之后的消息」继续计未读（定位点语义）。成功后服务端向本人广播 `unread_count_changed`（含 firstUnreadMessageId，服务端同批补字段）。
- **M9-① `sendMessage` 真正实现 `files` 形参**：此前第 5 形参 `files?: File[]` 被静默丢弃（函数体只 apiPost JSON）。现按服务端 `CreateMessage` 双绑定契约实现：有文件走 `upload` multipart（FormData：content/clientMessageId/parentId + attachments 多文件），无文件保持原 JSON body，行为向后兼容。
- **`sendMessageWithFiles` 补 `parentId` 尾参**：线程回复带附件不再需要调用点自行组装 FormData（服务端 multipart 分支本就支持 parentId 表单字段）；参数追加在末位，既有调用（channelId/content/files/clientMessageId/onProgress/signal）不受影响。
- **M9-② `getThread` 按服务端实际键归一化**：服务端 `GetThread` 返回 `data.messages`（非 items），原实现只归一化 `items` 导致 `normalizeMessage` 从不执行。现以 `messages` 为准（保留 `items` 读取防御），归一化结果同时写回两个键。

### 验证结果

`npm run build` 因 NAS 环境不可用（已知环境债，按任务约定跳过）；以 `diff web/shared/api-core.ts ridgericetalk-win/src/shared/api-core.ts` 逐字节一致为准。桌面端消费方与全量回归见 `ridgericetalk-win/docs/CHANGELOG.md` 同日条目（测试台 388/388）；服务端广播字段与 openapi 对齐见 `ridgericetalk/docs/CHANGELOG.md` 同日条目。

---

## 2026-09-14 — shared：云文件第二批 API（回收站 / 搜索 / 下载校验值）

### 修改类型

新增（跨端共享源，桌面端 `ridgericetalk-win/src/shared` 经 `sync:shared` 同步）

### 修改内容

- **回收站**（DES-2026-0912-02 §4.3）：`getCloudFSTrash()`、`restoreCloudFSTrash({id|path})`、`purgeCloudFSTrash({id|path})`、`emptyCloudFSTrash()`，对应 `GET /api/cloudfs/trash`（返回 `{items, retainDays}`）、`POST /api/cloudfs/trash/restore`、`DELETE /api/cloudfs/trash?id=|path=`、`DELETE /api/cloudfs/trash/all`；导出 `CloudFSTrashItem`（`deletedAt`/`expiresAt` 是服务端格式化的 `"YYYY-MM-DD HH:mm"`，**非 ISO**，客户端解析前需把空格换成 `T`）。
- **搜索**（§4.4）：`searchCloudFS(query, path?)` → `GET /api/cloudfs/search?q=&path=`，返回 `{items, query}`；导出 `CloudFSSearchItem`（`type` 恒为 `file`，可选 `path` 为完整路径，跨目录命中时用于定位）。
- **下载校验值**（§4.6）：新增 `CLOUDFS_CHECKSUM_HEADER = 'X-File-Checksum'`、`parseChecksumHeader()`、`sha256Hex()`、`verifyBlobChecksum()`、`downloadBlobWithChecksum()`、`downloadCloudFSWithChecksum()`、`previewCloudFSWithChecksum()`。`downloadCloudFS` 复用上述路径，**校验不一致即抛错**；`previewCloudFS` 契约不变（仍返回 `Blob`）。
- 服务端仅在**有值时**下发该头，历史文件没有：`verifyBlobChecksum` 在 `checksum === null` 时返回 `{verified:false}` 且**不报错**，调用方不得据此判失败。

### 验证结果

`cd web/admin && node node_modules/vitest/vitest.mjs run` → 6 文件 / 27 用例全过（admin 不消费这些 API，仅回归确认无破坏）；`diff -q web/shared/api-core.ts ridgericetalk-win/src/shared/api-core.ts` 逐字节一致；桌面端消费方见 `ridgericetalk-win/docs/CHANGELOG.md` 同日条目（206 → 221 用例）。

---

## 2026-09-14 — shared：新增 E2EE 密钥获取 API `getE2EEKey`

### 修改类型

新增（跨端共享源，桌面端 `ridgericetalk-win/src/shared` 经 `sync:shared` 同步）

### 修改内容

- `shared/api-core.ts` 新增 `getE2EEKey(channelId)`：`GET /api/voice/e2ee/key?roomId=<id>`，返回 `{code, data:{key}}` 信封，`key` 为服务端下发的加密口令串（hex）。
- 用途：桌面端 E2EE 接线第一期（DES-2026-0912-06），由客户端在 `room.connect` 前拉取密钥交给 `ExternalE2EEKeyProvider`。
- 同步后 `web/shared/api-core.ts` 与 `ridgericetalk-win/src/shared/api-core.ts` 逐字节一致（`diff -q` 已验证）。

### 验证结果

`cd web/admin && node node_modules/vitest/vitest.mjs run` → 4 文件 / 15 用例全过（admin 不消费该 API，仅回归确认无破坏）。

---

## 2026-09-14 — 管理后台 6 项 P1「承诺与实现不符」缺陷修复

### 修改类型

缺陷修复（管理后台承诺对齐 + 测试补齐）

### 修改位置

`admin/src/App.tsx`、`admin/src/components/{ChannelsPanel,UsersPanel,ModulesPanel,ServerSettingsPanel}.tsx`、`shared/api-core.ts`、新增 `admin/src/components/__tests__/ChannelsPanel.test.tsx`、新增 `admin/src/test/api-core-admin.test.ts`

### 背景（问题）

管理后台存在 6 项「UI 承诺与后端实现不符」的 P1 缺陷：假列/占位长期展示 `-`、`0.0%`、`success` 这类「看起来有、其实没有」的状态。

### 修改内容

1. **P1-1 频道管理改用 admin 专用接口**：此前用成员级 `/api/channels`（越权风险 + 用户端接口变更会连带弄坏管理页），而 `internal/admin` 的 4 条 admin 频道接口完全未被使用。`shared/api-core.ts` 新增 `getAdminChannels` / `createAdminChannel` / `updateAdminChannel` / `deleteAdminChannel`（路径参数是 **channelId**），`ChannelsPanel` 全部改用它；成员级函数保留给 Windows 客户端（`channelStore.ts` 在用）。同时把创建体收敛为 admin 接口的形状（`name` / `type` 小写 / `audioQuality`，id/spaceId/position 由服务端生成），并**移除失败时的本地伪造**（创建失败不再往列表塞假频道、改名/删除失败不再本地乐观改状态，一律以服务端列表为准）。
2. **P1-2「立即禁用」文案对齐**：后端没有任何「立即禁用」语义——`action != "enable"` 都会走 `ToggleModule(false)` 并**重新种一个 5 分钟宽限期**（`admin/service.go`）。按钮改为「开始禁用（5 分钟宽限期）」，调用改用 `toggleModule(name, 'disable')`，成功后把倒计时重置为 300s 并如实提示「模块将在 5 分钟宽限期结束后完全禁用」，不再把模块标成 disabled、不再谎报「已立即禁用」。
3. **P1-3 移除硬编码的「版本更新」卡片**：服务端 `GET /api/admin/update/check` 是无发布渠道的占位实现（`update_available:false` + `latest_version=当前版本` + 「当前已是最新版本」）。管理页不再展示该卡片，`App.tsx` 停止轮询该接口，`shared/api-core.ts` 移除已无调用方的 `getAdminUpdateCheck`。（服务端侧同步返回 `implemented:false`，见 ridgericetalk `docs/CHANGELOG.md`。）
4. **P1-4 移除用户表 `IP` / `延迟` 两列**：`model.User` 没有这两个字段，服务端也未采集（补采集涉及隐私与合规），不再长期显示 `-`。
5. **P1-5 移除模块 `CPU / MEM` 显示**：`admin/service.go` 建默认模块时 CPU/Memory 恒为 0 且无任何采样任务回填，按模块采样成本高收益低，直接移除该行。
6. **P1-6 重启提示不再显示 `success`**：信封顶层 `message` 恒为 `"success"`（`core/errors.Success`），真正的文案在 `data.message`。`restartAdminService` 改为直接返回可展示文案（`data.message`，缺失时回落中文提示），`ServerSettingsPanel` 直接使用该字符串。

### 修改原因/目的

按「假列/占位只有两个合法归宿：接通真数据，或明确标注为未实现并从 UI 移除」的原则消除管理页的虚假状态；管理面与用户端接口解耦。

### 验证结果

- `node node_modules/vitest/vitest.mjs run`：**6 个文件 / 27 个用例全过**（基线 4 文件 15 用例；新增 `ChannelsPanel.test.tsx` 6 例、`api-core-admin.test.ts` 6 例）。
- `node node_modules/typescript/bin/tsc --noEmit`：**0 错误**。
- 服务端侧：`go build ./cmd/server` 通过，`go test ./internal/admin/... ./internal/channel/...` 全过（admin 75 个 Test）。
- 共享源一致性：`npm run sync:shared` 后 `web/shared/api-core.ts` 与 `ridgericetalk-win/src/shared/api-core.ts` **完全一致**；`ridgericetalk-win` 的 `npx tsc --noEmit` 仍为 0 错误。

### 存疑（记录不修）

- `channel.Service.DeleteChannel`（服务端 `internal/channel/service.go:318`）对 `screen_share_sessions` 用了不存在的列 `bind_channel_id`，导致 `DELETE /api/admin/channels/:channelId` **必然 500**；因此管理页的「删除频道」在本次改动后会如实报错（旧代码是靠本地伪造删除掩盖失败）。该缺陷已有既有用例 `TestHandlerAdminDeleteChannelFailure` 记录在案，且属另一模块，未在本轮范围内修改。
- `ChannelsPanel` 文字频道表的「消息数」列仍是硬编码 `-`（同类假列，未在本次 6 项清单内）。
- 管理端 `App.tsx` 监听的是 `rrt:module_disabled` 事件与 WS 的 `module_disabled` 消息，而后端 `ModuleAction` 广播的 `type` 是 `module_status_changed`，宽限期横幅无法由服务端推送触发（仅本地路径生效），未在本轮范围内修改。

### 文档同步

`ridgericetalk/docs/CHANGELOG.md` 同步记录服务端侧改动。

## 2026-09-09 — 网页端 voice 暂停开发并归档（仅保留 admin + shared）

### 修改类型

移除/归档（项目范围收敛）

### 修改内容

- 网页版主应用 `web/voice` 从项目移除并归档：完整源码（含 e2e/Vitest/Playwright）移出本目录与 `ridgericetalk/web/`，存入根 `归档数据/2026-09-09-网页端voice-归档/`（`workspace-web-voice/` 最新版 + `server-repo-web-voice/` 部署构建源快照 + 旧快照）。
- 保留 `admin/`（管理后台）与 `shared/`（跨端共享代码唯一源，桌面端同步依赖）。
- 服务端运行时不受影响：`webhost/dist/voice` 已构建产物继续托管，voice 不再迭代。
- 详见 `归档数据/2026-09-09-网页端voice-归档/README.md`。

## 2026-09-02 — 统一跨端共享源

### 修改类型

简化（共享代码维护边界）

### 修改位置

`shared/api-core.ts`、`shared/types.ts`

### 修改内容

- 将 `displayName` 字段、音效库/音效设置、虚拟网络 IP 上报和语音虚拟身份 API 纳入共享源。
- Windows 客户端改为直接复制共享源，不再依赖下游补丁追加桌面端 API。

### 修改原因/目的

让 Web、Windows 和同步脚本共用同一份 API/类型契约，消除同步后能力被覆盖的风险；Web 端业务 UI 仍遵循暂停开发策略，不在本条目扩展实现。

### 验证结果

- Web voice 类型检查与 Vite 构建通过。
- Web admin 类型检查与 Vite 构建通过。
- Windows 客户端共享同步、类型检查与构建通过。

---

## 2026-07-21 — ISSUE-077 修复：多人同时屏幕共享 web 端只能收到一路

### 背景

局域网部署实测发现：win 客户端与 web 端同时发起屏幕共享时，web 观看端只能收到一路（通常先入局的先收到），后到一路的屏幕轨"已发布但未订阅/未解码"，`pc.getReceivers()` 中缺失、UI 不显示"N 人正在共享"多路入口。win×win 互看正常、web 单路正常。提交者：Claude。

### 根因

web 端 `useVoiceConnection.ts` 创建 LiveKit `Room` 时启用 `adaptiveStream: true`。本应用屏幕共享画面只有一个按需出现的 `<video>` 承载元素（观看者点击"观看"后才挂载，未观看/切换共享者时处于不可见或未 attach 状态），`adaptiveStream` 基于 IntersectionObserver/ResizeObserver 的可见性·尺寸管理会对这类不可见视频下发 `setSubscribed(false)` 或订阅 0 层，导致多人同时共享时后到的第二路屏幕轨被订阅抑制。单路时唯一屏幕轨会被 attach 到可见 `<video>`，故不触发。

### 变更清单

| # | 文件/模块 | 变更内容 |
|---|-----------|---------|
| 1 | `voice/src/hooks/useVoiceConnection.ts` | `new Room({ adaptiveStream: true→false })`：关闭基于可见性的订阅抑制，所有已发布屏幕轨按 `autoSubscribe`（默认 true）全部下发订阅 |
| 2 | `voice/src/hooks/useVoiceConnection.ts` | 切换观看目标的 `useEffect`：对选中共享者的 `screen_share`/`screen_share_audio` publication 显式 `setSubscribed(true)` 兜底 |
| 3 | `ridgericetalk/web/voice/src/hooks/useVoiceConnection.ts` | 同步服务端部署副本（与 #1/#2 相同改动） |

`dynacast: true` 保留（仅影响发布端层数，web 为观看端不受影响）。仅改多路订阅，未动 ISSUE-078 共享按钮状态、ISSUE-079 音乐 UX。

### 验证结果

- ✅ `cd web/voice && npm run build`（tsc + vite build）通过
- ✅ 已部署测试 VM `/opt/ridgericetalk/server/webhost/dist/voice/`（备份 `.bak-pre-issue077`）
- ✅ 实测 lantest1(win/Electron)+lantest2(web) 同时共享、lantest3/lantest4(web) 观看：两端均收到 2 路 `inbound-rtp` 视频且有帧、主 `<video>` `readyState=4`、侧栏显示 2 个共享者可切换、切换到第二路正常出画面

---

## 2026-07-14 — 白板支持多白板协作、存档与缩略图

### 背景

白板原实现以 `channelId` 为维度，导致切换频道或功能区后笔迹丢失；多人协作广播格式与前端监听不一致，无法真正同步。本次随服务端白板改造，同步实现 Web 端多白板管理、按白板实时协作与缩略图封面。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 文件/模块 | 变更内容 |
|---|-----------|---------|
| 1 | `shared/types.ts` | 更新 `Whiteboard` 与 `WhiteboardStroke` 类型，新增缩略图、笔迹数、最后绘制时间字段；笔迹字段改为扁平结构 |
| 2 | `shared/api-core.ts` | 新增 `listWhiteboards`、`createWhiteboard`、`updateWhiteboard`、`deleteWhiteboard`、`uploadWhiteboardThumbnail`；`getWhiteboardStrokes`/`createWhiteboardStroke` 改为按 `whiteboardId` 调用 |
| 3 | `voice/src/store/whiteboardStore.ts` | 新增 Zustand store：维护白板列表、当前白板、各白板笔迹、加载状态；支持增删改、切换白板、幂等追加笔迹 |
| 4 | `voice/src/hooks/useWebSocket.ts` | 新增 `subscribeWhiteboard(whiteboardId\|null)`，向服务端发送独立白板订阅/取消订阅 |
| 5 | `voice/src/hooks/useWsMessageHandler.ts` | 白板事件按 `whiteboardId` 过滤；新增 `whiteboard_clear` 事件分发 |
| 6 | `voice/src/App.tsx` | 将 `subscribeWhiteboard` 传入 `WhiteboardPanel` |
| 7 | `voice/src/components/WhiteboardPanel.tsx` | 重写为侧边栏白板列表（缩略图）+ 主画布；支持创建/重命名/归档/删除、撤销/重做、协作加入/离开、离席缩略图上传 |

### 验证结果

- ✅ `cd web/voice && npm run build` 通过（`SharedDocsPanel.tsx` 7 个预存类型错误与本改动无关）
- ⏳ 运行时多用户协作验证待部署后确认

---

## 2026-06-27 — 修复语音 LiveKit token 过期导致断线重连（自动刷新机制）

### 背景

后端返回的 LiveKit token `expiresIn` 为 480 秒（8 分钟），token 过期后 LiveKit 连接断开，导致语音频道"每过一会就会断线重连"。原前端虽有定时刷新，但使用固定 6 分钟 `setInterval`，未基于实际 `expiresIn` 提前 60 秒刷新，且刷新失败时无用户提示。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 文件/模块 | 变更内容 |
|---|-----------|---------|
| 1 | `voice/src/App.tsx` | 将 token 自动刷新从固定 6 分钟 `setInterval` 改为基于 `(expiresIn - 60) * 1000` 的递归 `setTimeout`；刷新成功后基于新 `expiresIn` 重置定时器；检查 LiveKit SDK 是否支持运行时 `updateToken`，不支持时记录警告（不强制断开）；刷新失败时显示中文 toast "语音连接即将断开，请重新加入"；ref 类型与所有清理点统一为 `clearTimeout` |

### 验证结果

- ✅ `npm run build` 通过（tsc + vite build，exit code 0）
- ⏳ 运行时验证待部署后确认（定时器在 token 过期前 60 秒触发刷新）

---

## 2026-06-26 — 修复 voice API 字段名不匹配导致 VOICE_ROOM_NOT_FOUND（ISSUE-042）

### 背景

服务器部署测试发现语音频道加入失败，`POST /api/voice/token` 返回 404 `VOICE_ROOM_NOT_FOUND`。根因：前端发送 `{ channelId }` 字段，后端期望 `{ roomId }` 字段，导致 `body.RoomID` 为空，`findOrCreateVoiceRoom("")` 返回 NOT_FOUND。同时修复 `ridgericetalk-win` 的 ISSUE-006。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 文件/模块 | 变更内容 |
|---|-----------|---------|
| 1 | `shared/api-core.ts` | 5 个 voice API 函数 JSON 字段从 `{ channelId }` 改为 `{ roomId: channelId }`：getVoiceToken、refreshVoiceToken、joinVoiceChannel、leaveVoiceChannel、kickVoiceParticipant |

### 验证结果

- ✅ `npm run build` 通过（tsc + vite build）
- ✅ 部署到服务器 `/opt/ridgericetalk/server/webhost/dist/voice/`
- ✅ 浏览器端到端验证：`POST /api/voice/token` 返回 200（原 404），UI 显示"语音已连接"延迟 47ms

---

## 2026-06-25 — 删除邮箱服务（admin/voice/shared，1 个提交）

### 背景

执行 `remove-email-service` spec：删除邮箱服务功能（SMTP 发送、邮箱验证码、密码重置邮件、账户锁定邮件、admin 邮箱配置 tab、忘记密码流程）。**保留** User.Email 字段作为用户标识与登录依据、JWT email claim、用户 profile 修改 email 能力、登录支持 username 或 email。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 文件/模块 | 变更内容 |
|---|-----------|---------|
| 1 | `admin/src/App.tsx` | 删除邮箱服务 tab：移除 lucide-react `Mail` 导入、api-core 4 个邮箱 API 导入、`EmailConfigStatus`/`EmailConfig`/`EMPTY_EMAIL_CONFIG` 类型、Tab 类型 `'email'` 值、10 个邮箱 state、loadAdminData 中 emailStatus 加载、`statusLabel`/`statusColor` 映射、3 个邮箱处理函数、navItems 中 email tab 项、邮箱服务 tab JSX 渲染区块。共删除约 242 行 |
| 2 | `voice/src/App.tsx` | 删除忘记密码流程：移除 `forgotPassword`/`resetPassword` 导入、`forgotStep` state、4 个 forgot refs、mode 类型 `'forgot'` 值、'forgot' 模式 UI 区块、"忘记密码？"链接。共删除约 53 行 |
| 3 | `shared/api-core.ts` | 删除 `forgotPassword`/`resetPassword` 函数与 5 个 admin 邮箱配置 API 函数（`updateEmailConfig`/`testEmailConfig`/`sendEmailTestCode`/`verifyEmailTestCode`/`getEmailConfigStatus`）。共删除 22 行 |
| 4 | `tests/verification-shots/remove-email-service/` | 新增 5 张浏览器 E2E 验证截图 |

### 验证结果

- `cd web/admin && npx tsc --noEmit` 通过（exit 0）
- `cd web/voice && npx tsc --noEmit` 通过（exit 0）
- `cd ridgericetalk-win && npx tsc --noEmit` 通过（exit 0）
- voice 登录页 DOM 文本确认无"忘记密码？"链接；注册页含 email 输入框正常
- admin App.tsx 源码 Grep 匹配 `Mail|emailConfig|EmailConfig|updateEmailConfig|邮箱服务` 数为 0
- voice App.tsx 源码 Grep 匹配 `forgotPassword|resetPassword|forgotStep|忘记密码` 数为 0

### Commit

| # | Hash | Message |
|---|------|---------|
| 1 | （本次提交） | `refactor: 删除邮箱服务功能` |

---

## 2026-06-25 — P3 收尾补充：admin vite dev server API 代理（1 个提交）

### 背景

Task 5 浏览器端到端验证时发现 admin vite dev server 缺少 API 代理，导致 `/api/admin/login` 等请求返回 404。添加 dev 代理转发 `/api` 与 `/ws` 到后端 8080 端口。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 提交 | 类型 | 文件/模块 | 变更内容 |
|---|------|------|-----------|---------|
| 1 | `fix: admin vite dev server 添加 /api 与 /ws 代理` (6288cb7) | 修复 | `admin/vite.config.ts` | 新增 `server.proxy` 配置：`/api → http://localhost:8080`（changeOrigin）、`/ws → ws://localhost:8080`（ws:true）。仅影响 dev server，不影响生产构建 |

### 验证结果

- `curl http://localhost:5174/api/server/info` 返回 HTTP 200 + 完整 JSON（代理工作正常）
- admin 登录页可正常加载，登录请求成功转发到后端（404 已消除，仅因 MFA + 密码变更未完成实际登录）

---

## 2026-06-25 — P3 收尾：小游戏扩展、sharedoc e2e、useModalFocus、web-win 集成（5 个提交）

### 背景

P3 阶段收尾工作：web 端小游戏面板扩展、sharedoc-schedule 端到端测试、useModalFocus 模态框焦点管理 Hook、web-win App 集成小游戏面板与样式优化、测试截图整理。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 提交 | 类型 | 文件/模块 | 变更内容 |
|---|------|------|-----------|---------|
| 1 | `feat: web 端小游戏面板扩展至 4 款游戏（井字棋在线+国际象棋+五子棋+2048）` | 新增 | `voice/src/components/MinigamesPanel.tsx`、`voice/src/components/games/ChessLocalGame.tsx`、`voice/src/components/games/Game2048.tsx`、`voice/src/components/games/GomokuGame.tsx`、`voice/src/components/games/TicTacToeOnline.tsx` | 小游戏面板从单款扩展至 4 款（井字棋在线对局 + 国际象棋本地对战 + 五子棋 + 2048） |
| 2 | `test: 新增 sharedoc-schedule 端到端测试` | 新增 | `voice/e2e/sharedoc-schedule.spec.ts` | 新增 sharedoc-schedule 端到端测试用例 |
| 3 | `feat: 新增 useModalFocus 模态框焦点管理 Hook` | 新增 | `shared/useModalFocus.ts` | 抽取通用模态框焦点管理 Hook，供 web 端复用 |
| 4 | `feat: web-win 集成小游戏面板与样式优化` | 变更 | `web-win/src/App.tsx`、`web-win/src/styles.css`、`voice/src/styles.css` | web-win App 集成小游戏面板；voice/web-win 样式优化 |
| 5 | `chore: 整理测试截图到 tests 目录` | 变更 | `tests/verification-shots/p3-minigames-e2e/schedule-after-save.png`、`tests/verification-shots/p3-minigames-e2e/webbridge-schedule.png` | 将 voice/test-results 下的 P3 e2e 测试截图整理至 web/tests/verification-shots/p3-minigames-e2e/ |

### 验证结果

- `npx tsc --noEmit` 通过（web/voice、web/admin、web/web-win 均 exit 0，零错误）
- `npm test -- --run` 单元测试通过（3 tests passed，2 test files）
- 未提交 `.last-run.json` 等临时文件

---

## 2026-06-25 — Spec 实施汇总（P0/P1/P2，2 个提交）

### 背景

按 spec 规范提交本轮前端 spec 改动，拆分为 2 个 logical commit。提交者：五常大米 <3206361480@qq.com>。

### 变更清单

| # | 提交 | 类型 | 文件/模块 | 变更内容 |
|---|------|------|-----------|---------|
| 1 | `test: P1 配置前端 vitest coverage 目标 70%` | 变更 | `voice/vitest.config.ts`、`voice/package.json`、`voice/tsconfig.json` | voice 端 coverage 阈值 70% + 脚本/类型调整 |
| 1 | 同上 | 变更 | `admin/vite.config.ts`、`admin/package.json`、`admin/tsconfig.json` | admin 端 coverage 配置同步 |
| 1 | 同上 | 变更 | `web-win/tsconfig.json` | web-win 测试类型配置同步 |
| 2 | `feat: 同步 P0 麦克风测试与 P2 远程协助 Web 端 spec 实现` | 变更 | `voice/src/App.tsx` | 新增 handleMicTest + UI（P0 Task 4）；导入 RemoteAssistPanel + 条件渲染 + WebSocket 事件转发（P2 Task 2） |
| 2 | 同上 | 新增 | `voice/src/components/RemoteAssistPanel.tsx` | 远程协助面板组件 |

### 验证结果

- 2 个提交均成功创建。
- 未提交 test-results 截图、无关的游戏组件等非 spec 文件。

---

## 2026-06-25 — P2 中优先级功能 + P1 浏览器验证 bug 修复

### 修改类型：新增 + 修复

### 影响范围
- `web/shared/api-core.ts` — `detectAdminNetwork` 返回类型修正；`api` 函数 Error 附加 `code` 属性；`setApiBase`/`getApiBase` 新增（Web SRV 解析支持动态 API 基址）
- `web/admin/src/App.tsx` — 文件大小限制配置 UI；网络配置页显示穿透方案推荐；**Bug 1 修复**：转移所有权弹窗从 users tab 移到 channels tab；**Bug 2 修复**：网络探测数据解构从 `res.data?.result` 改为 `res.result`
- `web/voice/src/App.tsx` — 新增 `resolveBySRV`/`resolveServerAddress`/`tryFallbackPorts` 函数 + `ServerSelectScreen` 组件（DoH SRV 解析 + fallback）
- `web/voice/src/components/CloudFilesPanel.tsx` — 上传错误识别 `FILE_TOO_LARGE` 显示"文件超过大小限制"

### 修改内容

#### P2 新增功能
1. **文件大小限制 UI**：系统设置基本配置增加"最大文件大小(MB)"输入框，保存到 AdminConfig
2. **穿透方案推荐 UI**：网络配置页"自动探测网络"后显示推荐方案列表（优先级序号 + 方案名 + 说明）
3. **Web 端 SRV 解析**：`resolveBySRV` 通过 DoH（dns.google）查询 `_rrt._tcp.<domain>` SRV 记录；`resolveServerAddress` 支持 IP 直连、域名 SRV 解析、显式端口直连三种模式；`ServerSelectScreen` 服务器选择界面；fallback 链 443 → 8080
4. **上传超限提示**：`api` 函数抛出的 Error 附加 `code` 属性（向后兼容），CloudFilesPanel 识别 `FILE_TOO_LARGE` 显示提示

#### Bug 修复
- **Bug 1（P1 Task 9 验证发现）**：频道转移所有权弹窗 JSX 被错误放在 `{tab === 'users' && (...)}` 块内，但触发按钮在 channels tab。点击按钮时 users tab 不渲染，弹窗无法显示。修复：将弹窗 JSX 移到 channels tab 内（deleteChannelConfirm 弹窗之后）
- **Bug 2（P2 验证发现）**：后端 `DetectNetwork` 直接返回 `{ success, result, recommendations }`（无 `{code, data}` 包装层），但前端用 `res.data?.recommendations` 访问导致取不到值。修复：`detectAdminNetwork` 返回类型改为 `{ success, result, recommendations }`，前端解构改为 `res.result`/`res.recommendations`

### 验证结果
- `npx tsc --noEmit` 通过（web/admin、web/voice 均 exit 0）
- 浏览器实测：频道管理页点击"转移所有权"按钮后弹窗正常显示（含用户选择下拉框）
- 浏览器实测：网络配置页"自动探测网络"后正常显示 3 条推荐方案（frp/cloudflare_tunnel/tailscale）

---

## 2026-06-25 — 管理后台 P1 功能补齐（前端）

### 修改类型：新增

### 影响范围
- `web/shared/api-core.ts` — 新增 3 个 admin API 方法，`getAdminUsers` 增加 keyword 参数
- `web/admin/src/App.tsx` — 用户管理（启用/禁用、服务端搜索、重置密码弹窗）、频道管理（转移所有权弹窗）

### 修改内容
1. **用户启用/禁用 UI**：用户列表每行增加"禁用/启用"按钮，根据 `is_active` 状态切换，调用 `PATCH /admin/users/:uid/status` 后刷新列表；新增"状态"列显示●正常/●已禁用
2. **服务端 keyword 搜索**：`getAdminUsers` 增加 keyword 参数；搜索框改为回车触发服务端搜索（`GET /admin/users?keyword=xxx`），移除纯客户端过滤；使用 refs 避免 interval 刷新的 stale closure；stats 卡片改用服务端返回的 summary
3. **管理员重置密码**：新增 `resetUserPassword(uid, newPassword)` API；用户列表增加"重置密码"按钮 + 弹窗（新密码+确认密码，≥8位校验、一致性校验），成功后提示"密码已重置"
4. **频道转移所有权**：新增 `transferChannelOwnership(channelId, newOwnerId)` API；文本/语音频道列表均增加"转移所有权"按钮 + 用户选择弹窗，成功后刷新频道列表

### 验证结果
- `npx tsc --noEmit` 通过（exit 0，零错误）

---

## 2026-06-25 — Voice 前端 Bug 修复与设计优化

### 修改类型：修复 + 优化

### 影响范围
- `web/voice/src/App.tsx` — 健康检查退避、注册流程、消息渲染（悬停操作+时间分隔符+表情/@提及）、设置弹窗 Escape 关闭
- `web/voice/src/styles.css` — 浅色主题强调色、弹窗 max-height、消息悬停/紧凑模式、输入区增强、白板工具栏分组、游戏房间卡片
- `web/voice/src/components/MinigamesPanel.tsx` — 游戏房间空状态引导 UI
- `web/voice/src/components/WhiteboardPanel.tsx` — 工具栏分组布局重构

### 修改内容
1. **Bug 修复**: 健康检查指数退避（3s→60s）、设置弹窗 Escape 关闭 + max-height 约束、语音按钮激活态增强、注册成功后返回登录
2. **设计优化**: 消息悬停操作按钮（回复/表情/更多）、智能时间分隔符（5分钟分组+紧凑模式）、输入区表情/@提及按钮、浅色主题强调色（#1a9e91）、游戏房间空状态引导、白板工具栏分组布局

### 验证结果
- TypeScript 编译通过（零错误）
- 浏览器自动化验证：页面加载正常、消息区域渲染、设置弹窗打开/Escape关闭、消息操作按钮存在

---

## 2026-06-23 — 小游戏独立房间：状态同步修复与双账号 E2E 验证

### 新增/修改清单

| # | 模块 | 说明 | 类型 |
|---|------|------|------|
| 1 | 小游戏 WebSocket 同步 | 修复 `move_made` 事件：`data.nextPlayer` 正确映射为本地 `currentPlayer`，确保对战双方回合切换 | 修复 |
| 2 | 井字棋组件 | 观战者状态文本优先显示“观战模式中” | 修复 |
| 3 | E2E 测试 | 新增 `e2e/minigame-tictactoe.spec.ts`：双玩家创建房间、加入、开始对局、落子同步；第三账号以观战者身份加入并实时看到落子 | 测试 |

### 修改文件

- `web/voice/src/App.tsx`
- `web/voice/src/components/games/TicTacToeOnline.tsx`
- `web/voice/e2e/minigame-tictactoe.spec.ts`（新建）

### 验证结果

- `npm run build` 通过
- `npx playwright test e2e/minigame-tictactoe.spec.ts --workers=1` 通过
- 使用 Kimi WebBridge 在真实浏览器中完成端到端验证：
  - 玩家 A 注册登录后创建井字棋房间
  - 玩家 B（通过后端 API 注册并加入同一房间）加入后，玩家 A 的浏览器实时显示玩家列表从 1/2 变为 2/2 并出现“开始游戏”按钮
  - 点击“开始游戏”后进入棋盘，玩家 A 看到“轮到你了”
  - 玩家 B 落子后，玩家 A 的棋盘实时显示对手的 `O`
  - 玩家 A 点击棋盘落子 `X` 后状态切换为“等待对手落子...”

---

## 2026-06-22 — Phase 1：共享文档与共享日程（Web）

### 新增/修改清单

| # | 模块 | 说明 | 类型 |
|---|------|------|------|
| 1 | 共享文档面板 | 新增在线编辑者、版本历史恢复、expectedVersion 冲突检测、自动保存、远程更新提示 | 新增 |
| 2 | 共享日程面板 | 新增提醒下拉框（不提醒/开始时/5/15/30/60/自定义），事件卡片显示铃铛图标 | 新增 |
| 3 | API 层 | `updateSharedoc` 增加 `expectedVersion`；新增 `getSharedocEditors` / `getSharedocVersions` / `restoreSharedocVersion`；日程 API 增加 `reminderMinutes` | 新增 |
| 4 | App 事件路由 | 分发 `sharedoc:updated` / `sharedoc:collab` / `schedule_reminder` / `schedule_invite_updated` 事件 | 新增 |
| 5 | 日程时间格式 | 修复 `startTime` 缺少时区导致后端 `bad request`，统一拼接 `:00Z` | 修复 |
| 6 | 测试 | 新增 `e2e/sharedoc-schedule.spec.ts`，覆盖共享文档与日程的浏览器端到端验证 | 新增 |

### 修改文件

- `web/voice/src/components/SharedDocsPanel.tsx`
- `web/voice/src/components/SchedulePanel.tsx`
- `web/voice/src/App.tsx`
- `web/shared/api-core.ts`
- `web/voice/e2e/sharedoc-schedule.spec.ts`（新建）

### 验证结果

- `npm run build` 通过
- `npx playwright test e2e/sharedoc-schedule.spec.ts` 通过
- 使用 Kimi WebBridge 在真实浏览器中完成导航、点击、截图验证

---

## 2026-06-21 — P3 L-09/L-10 Admin 后台 2 项 Low 修复

### 修复问题清单（2项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | L-09 | Admin 后台 4 个空操作按钮（清理日志/清理临时文件/查看更多日志/导出日志） | 修复 | 低 |
| 2 | L-10 | Admin 音频质量设置仅本地 state，未调用后端 API 持久化 | 修复 | 低 |

### 修改详情

#### L-09 — 空操作按钮禁用

**修改文件**：`web/admin/src/App.tsx`
- 4 个空操作按钮添加 `disabled` 属性
- 添加 `title="暂未实现"` 鼠标悬停提示
- 添加 `style={{ opacity: 0.5, cursor: 'not-allowed' }}` 视觉提示
- 位置：
  - `App.tsx:1127-1128`：清理日志文件 / 清理临时文件
  - `App.tsx:1272-1273`：查看更多日志 / 导出日志

#### L-10 — 音频质量持久化（前端部分）

**修改文件**：`web/admin/src/App.tsx:817-828`
- 音频质量下拉框 onChange 改为 async 函数
- 先 `setAudioQuality` 更新本地 state
- 然后调用 `updateChannel(c.id, { audioQuality: newQuality })` 持久化到后端
- 失败时 `alert` 提示用户

### 验证结果

- `npx tsc --noEmit` 通过（exit code 0）

---

## 2026-06-21 — P3 L-01/L-02/L-03/L-04 Web 网页版 4 项 Low 修复

### 修复问题清单（4项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | L-01 | 版本信息硬编码 `v1.0.0-beta` / `2026-05-13.abcdef` | 修复 | 低 |
| 2 | L-02 | 语音设备列表硬编码（默认麦克风/耳机麦克风等） | 修复 | 低 |
| 3 | L-03 | 屏幕共享状态 `isScreenSharing = false` 硬编码 | 修复 | 低 |
| 4 | L-04 | beforeunload 请求缺鉴权头 | 修复 | 低 |

### 修改详情

#### L-01 — 版本信息动态化

**修改文件**：
1. `web/voice/vite.config.ts`：新增 `import pkg from './package.json'`，通过 `define` 注入 `__APP_VERSION__` 和 `__BUILD_TIME__`
2. `web/voice/src/vite-env.d.ts`：新增 `declare const __APP_VERSION__: string` 和 `__BUILD_TIME__: string` 类型声明
3. `web/voice/src/App.tsx:790-791`：版本/构建信息改为 `{__APP_VERSION__}` / `{__BUILD_TIME__}`

#### L-02 — 语音设备动态枚举

**修改文件**：`web/voice/src/App.tsx`
- 新增 `useAudioDevices()` hook，调用 `navigator.mediaDevices.enumerateDevices()` 过滤 audioinput/audiooutput
- 监听 `devicechange` 事件实时刷新
- 在 `SettingsModal` 内调用 hook，设备下拉框改为动态渲染
- 设备列表为空时显示"请授权麦克风/扬声器权限"

#### L-03 — 屏幕共享状态

**修改文件**：`web/voice/src/App.tsx`
- 新增 `screenSharingUserIds: Set<string>` state
- `RoomEvent.TrackSubscribed` 中 `screen_share` 视频轨道触发时添加用户 ID
- `RoomEvent.TrackUnsubscribed` 中移除用户 ID
- 替换 `isScreenSharing = false // TODO` 为 `screenSharingUserIds.has(p.userId)`

#### L-04 — beforeunload 鉴权头

**修改文件**：`web/voice/src/App.tsx:1317-1335`
- 从 localStorage 读取 `rrt_token`
- 构造 `authHeaders` 包含 `Authorization: Bearer ${token}`
- `/api/voice/leave` 和 `/api/users/:id/status` 两个 fetch 请求使用 `authHeaders`

### 验证结果

- `npx tsc --noEmit` 通过（exit code 0）

---

## 2026-06-21 — P2 M-03 修复 Web MinigamesPanel 房间数据本地化

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | M-03 | `MinigamesPanel.tsx` 房间创建仅本地 state，无 API 调用 | 修复 | 中 |

### 修改详情

#### M-03 — MinigamesPanel 房间功能调整

**背景**：MinigamesPanel 中"创建房间"仅本地 `setRooms(prev => [...prev, newRoom])`，无 API 调用。后端 minigames API 需要 channelId 上下文（`POST /api/v1/minigames/join` body 含 channelId），但 Web 网页版 MinigamesPanel 无 channelId prop。

**修改文件**：

1. `web/voice/src/components/MinigamesPanel.tsx`
   - 移除本地房间创建功能（创建房间输入框、房间列表、加入/观战按钮）
   - rooms 视图改为提示"请在语音频道内启动游戏"
   - 清理未使用的 state（rooms/showCreateRoom/newRoomName）和 import（Plus/Users/Play/Eye/Dices/Car/X）

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过

---

## 2026-06-21 — P2 M-04 实现 Web 踢出语音频道功能

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | M-04 | `App.tsx` 踢出按钮 `// TODO: call kick API`，仅 console.log | 修复 | 中 |

### 修改详情

#### M-04 — 踢出语音频道功能实现

**背景**：App.tsx 中管理员踢出语音频道成员的按钮仅打印日志，未调用后端 API。后端已实现 `POST /api/voice/participants/:id/kick`（RequireAdmin）。

**修改文件**：

1. `web/shared/api-core.ts`
   - 新增 `kickVoiceParticipant(userId, channelId)` 函数

2. `web/voice/src/App.tsx`
   - import 新增 `kickVoiceParticipant`
   - 踢出按钮 onClick 改为 async，调用 `kickVoiceParticipant(p.userId, voiceChannelId)`
   - 失败时 alert 错误信息

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过

---

## 2026-06-21 — P2 M-02 修复 Web CloudFilesPanel 存储配额硬编码

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | M-02 | `CloudFilesPanel.tsx` 中 `USED_GB=0, TOTAL_GB=0` 硬编码，未调用后端 `/api/cloudfs/usage` | 修复 | 中 |

### 修改详情

#### M-02 — CloudFilesPanel 存储配额真实化

**背景**：CloudFilesPanel 顶部存储配额显示硬编码 `0GB / 0GB (0%)`，后端已实现 `GET /api/v1/cloudfs/usage` 返回 `{used, quota, usedPercent}`（单位：字节）。

**修改文件**：

1. `web/shared/api-core.ts`
   - 新增 `getCloudFSUsage()` 函数

2. `web/voice/src/components/CloudFilesPanel.tsx`
   - 移除 `USED_GB`/`TOTAL_GB` 常量
   - 新增 `usage` state 和 `formatBytes` 函数（动态单位 B/KB/MB/GB/TB）
   - useEffect 中调用 `getCloudFSUsage()` 加载真实配额
   - 配额显示改为 `${formatBytes(used)} / ${formatBytes(quota)} (xx.x%)`

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过

---

## 2026-06-21 — P2 M-07 覆盖 Admin update/check 路由

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | M-07 | `GET /api/admin/update/check` 后端已实现但前端未覆盖 | 修复 | 中 |

### 修改详情

#### M-07 — Admin update/check 路由覆盖

**背景**：后端 `admin/handler.go` 已实现 `GET /admin/update/check` 返回版本信息，但前端 api-core.ts 和 App.tsx 均未调用。

**修改文件**：

1. `web/shared/api-core.ts`
   - 新增 `getAdminUpdateCheck()` 函数，调用 `GET /api/admin/update/check`

2. `web/admin/src/App.tsx`
   - import 新增 `getAdminUpdateCheck`
   - 新增 `updateInfo` state
   - `loadAdminData` 中并行加载 `getAdminUpdateCheck()`
   - server tab 新增"版本更新"卡片，显示当前版本/最新版本/更新状态/changelog

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过

---

## 2026-06-21 — P2 M-08 移除 Admin 后台 9 处 MOCK 常量

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | M-08 | `web/admin/src/App.tsx` 9 处 MOCK 常量作为 useState 初始值，API 失败时 fallback 到 mock 误导用户 | 修复 | 中 |

### 修改详情

#### M-08 — Admin MOCK 常量清理

**背景**：App.tsx 顶部定义了 9 处 MOCK 常量（MOCK_CONFIG/PERF/USERS/CHANNELS/EMAIL_CONFIG/AUDIO_QUALITY/MODULES/STORAGE/LOGS），作为各 useState 的初始值。当 API 调用失败时（`.catch(() => null)`），state 保留 mock 数据，管理员看到的是假数据而非错误提示。

**修改文件**：

1. `web/admin/src/App.tsx`
   - 移除 9 处 MOCK 常量定义（约 120 行）
   - `config` useState 改为 `useState<ServerConfigSnapshot | null>(null)`
   - `performance` useState 改为 `useState<AdminPerformanceSnapshot | null>(null)`
   - `users`/`channels`/`modules`/`storage`/`auditLogs`/`audioQuality` useState 初始值改为空数组/空对象
   - `emailConfig` useState 初始值改为 `EMPTY_EMAIL_CONFIG`（空字段）
   - server tab 和 runtime tab 加 null 检查，config/performance 为 null 时显示"加载中..."或"暂无数据"

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过
- ✅ `npm run build` 构建成功（252.60 kB）

---

## 2026-06-21 — P1 H-10 修复 Admin 运行监控硬编码

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | H-10 | 运行监控 4 个区域全部硬编码假数据（吞吐量图表/实时概况/累计流量/机器人状态） | 修复 | 高 |

### 修改详情

#### H-10 — Admin 运行监控硬编码清理

**背景**：运行监控页面 4 个区域全部硬编码假数据：3 个吞吐量图表（10 个时序点）、6 个实时概况指标（在线用户 42、当前语音 18、消息速率 156、WebSocket 67、平均响应 28ms、运行时长 14d）、4 行累计流量表（8080/7880/9090/51820 端口）、机器人状态（"晴天 - 周杰伦"，队列 3 首）。后端 `GetRuntime` 只返回 CPU/内存/磁盘/网络/uptime，不提供这些细分数据。

**修改文件**：

1. `web/admin/src/App.tsx`
   - 新增 `formatBytesFromBytes(bytes)` 函数（后端 network.rx/tx 返回字节）
   - 新增 `formatUptime(seconds)` 函数（替换旧的中文格式版本）
   - 移除未使用的 `Pause`/`SkipForward`/`Play` import
   - **吞吐量图表**：移除 3 个假时序 MiniChart，替换为网络速率卡片（显示 `performance.network.rx/tx`）
   - **实时概况**：6 个指标改为 2 个真实（网络接收、运行时长）+ 4 个"暂无数据"（在线用户、当前语音、消息速率、WebSocket）
   - **累计流量表**：移除假表格，改为接收/发送总计
   - **机器人状态**：移除假播放信息和控制按钮，改为提示"请前往机器人频道查看实时播放状态"

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过
- ✅ `npm run build` 构建成功（1.00s）

---

## 2026-06-21 — P1 H-09 修复 Admin 后台登出未调用 API

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | H-09 | admin 登出只清前端 state，未调用 `POST /api/admin/logout`，服务端 session 未失效 | 修复 | 高 |

### 修改详情

#### H-09 — Admin 登出调用后端 API

**背景**：`App.tsx:586` 登出按钮 `onClick={() => setSession(null)}` 只清前端 state，未调用后端 `POST /api/admin/logout`，服务端 session 未失效，token 被盗后无法通过登出撤销。

**修改文件**：

1. `web/shared/api-core.ts:170`
   - 新增 `adminLogout = () => apiPost('/api/admin/logout')`

2. `web/admin/src/App.tsx`
   - import 新增 `adminLogout`
   - 新增 `handleAdminLogout` 函数：先调 `adminLogout()`，finally 中清 `setSession(null)`（API 失败也清 state，避免用户卡住）
   - 登出按钮 onClick 从 `() => setSession(null)` 改为 `handleAdminLogout`

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过
- ✅ `npm run build` 构建成功（1.11s）

---

## 2026-06-21 — P1 H-04 修复 Web Bot 状态码判断错误

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | H-04 | App.tsx 判断 res.code === 'SUCCESS'，但后端返回 'OK' | 修复 | 高 |

### 修改详情

#### H-04 — 状态码判断修复

**背景**：`App.tsx:2004` 在加入语音频道后立即拉取参与者列表时，判断 `res?.code === 'SUCCESS'`，但后端 `core/errors/errors.go:535-542` 的 `Success` 函数返回的 code 字段是 `"OK"`。这导致条件永远为 false，参与者列表不刷新，只能依赖 WebSocket 广播（有收敛延迟）。

**修改文件**：

1. `web/voice/src/App.tsx:2004`
   - `'SUCCESS'` → `'OK'`

**全局扫描**：`web/voice/src` 目录下只有这一处使用了错误的 `'SUCCESS'`。

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过
- ✅ `npm run build` 构建成功（1.75s）

---

## 2026-06-21 — P1 H-03 修复 Web VirtualNetPanel 全 mock 数据

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | H-03 | VirtualNetPanel 全部数据硬编码（假邀请码/IP/延迟/节点列表） | 修复 | 高 |

### 修改详情

#### H-03 — VirtualNetPanel 接入真实 API

**背景**：`VirtualNetPanel.tsx` 全部数据为硬编码假数据（`INVITE_CODE = 'RIDGE-VNET-8X2K9P'`、`subnet = '10.0.0.0/24'`、延迟 `23ms`、IP `10.0.0.100`），节点列表永远为空，且"Web 不支持"提示与可点击的连接按钮自相矛盾。后端 `/api/virtualnet/` 4 个端点均已实现，但前端只调用了 status。

**修改文件**：

1. `web/shared/api-core.ts:423-429`
   - 新增 `connectVirtualNet(networkCidr?)` → POST `/api/virtualnet/connect`
   - 新增 `disconnectVirtualNet()` → POST `/api/virtualnet/disconnect`
   - 新增 `getVirtualNetNodes()` → GET `/api/virtualnet/nodes`

2. `web/voice/src/components/VirtualNetPanel.tsx`（完全重写）
   - 删除 `INVITE_CODE` 常量、硬编码 `subnet`、硬编码延迟 `23ms`、假 IP `10.0.0.100`
   - `useEffect` 挂载时调用 `getVirtualNetStatus()` 获取真实状态
   - `connState` 由后端 `status` 字段驱动
   - `localIp` 由后端 `ip` 字段驱动
   - `nodes` 由 `getVirtualNetNodes()` 返回的真实节点列表驱动
   - 连接按钮调用 `connectVirtualNet()`，成功后刷新状态和节点列表
   - 断开按钮调用 `disconnectVirtualNet()`
   - `headscaleAvailable === false` 时显示"服务未配置"提示（替代矛盾的"Web 不支持"提示）
   - 连接成功后如返回 WireGuard `config`，显示配置和复制按钮（浏览器无法建立隧道，需桌面客户端使用）

### 验证结果

- ✅ `npx tsc --noEmit` 类型检查通过
- ✅ `npm run build` 构建成功（1596 modules transformed，1.79s）

---

## 2026-06-20 — P0-2 Web 网页版 Zustand 迁移和 react-router v7 实现（C-02 渐进式）

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | C-02 | Web 网页版 Zustand store 是死代码，react-router v7 未实现 | 重构 | 高 |

### 修改详情

#### C-02 — Web 网页版状态管理和路由迁移（渐进式第一步）

**背景**：审查发现 Web 网页版（`web/voice/`）的 Zustand store 文件已创建但完全未被使用（死代码），App.tsx 仍有 100+ useState 集中在根组件。react-router v7 仅完成 BrowserRouter 包裹，未定义 Routes/Route。违反设计文档 A-01（react-router v7）和 A-02（Zustand）。

**策略**：渐进式迁移，先补全 store 文件使死代码变为可用状态，再实现路由结构，最后逐步替换 useState。保持 App.tsx 业务逻辑不变，避免破坏 3587 行的复杂逻辑。

**修改文件**：

1. `voice/src/store/voiceStore.ts`（新增）
   - 语音状态管理：voiceChannelId、isInVoiceRoom、status、muted、speakerMuted、micVolume、outputVolume、botVolume、connectionLatency、reconnectAttempt、screenshareActive、screenshareWatching、isSharingScreen、screenShareQuality、activeSpeakers、participantAudioState、participantVolumes
   - 参考 ridgericetalk-win/src/renderer/stores/voice/voiceStore.ts

2. `voice/src/store/messageStore.ts`（新增）
   - 消息状态管理：messages、messageDraft、replyTo、threadOpenId、threadMessages、pinnedMessage、linkPreviews
   - 从 channelStore 拆分独立的消息状态管理

3. `voice/src/store/botStore.ts`（新增）
   - 机器人状态管理：currentTrack、botPlaying、botQueue、playMode、currentTime、botSourceTab、botFeatureTab、queueExpanded、botChannelId、netease 相关状态、lyricLines、ttsText

4. `voice/src/store/featureStore.ts`（新增）
   - 功能面板状态：featurePanelTab（whiteboard/cloudfs/sharedoc/schedule/minigames/virtualnet）

5. `voice/src/store/index.ts`
   - 导出全部 7 个 store（原 3 个 + 新增 4 个）

6. `voice/src/main.tsx`
   - 实现 react-router v7 Routes/Route 路由结构
   - 当前 App 作为根路由组件处理认证和布局切换
   - 预留未来拆分后的路由结构注释（/login、/register、/channels/:channelId 等）

### 验证结果

- ✅ `npx tsc --noEmit` TypeScript 编译通过（exit code 0，无错误）
- ✅ `npx vitest run` 单元测试通过（3 tests passed）
- ✅ store 文件从死代码变为可用状态（7 个 store 全部导出）
- ✅ react-router v7 Routes/Route 路由结构已实现
- ✅ App.tsx 业务逻辑保持不变（3587 行未修改）

### 影响范围

- **状态管理**：store 文件可用，后续可逐步将 App.tsx 的 useState 替换为 store 调用
- **路由**：main.tsx 定义了 Routes/Route 结构，为未来组件拆分奠定基础
- **向后兼容**：所有现有功能保持不变，无破坏性改动

### 后续工作

- 逐步将 App.tsx 的 100+ useState 替换为 store 调用
- 拆分 App.tsx 为独立的 AuthScreen 和 MainLayout 路由组件
- 实现 URL 路由可分享（/channels/:channelId 等）

---

## 2026-06-20 — P0-4 Admin 后台邮箱功能接入真实 API（C-04）

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | C-04 | Admin 后台邮箱配置保存/测试全部未接入后端 API，"发送验证码"按钮伪造状态 | 修复 | 中 |

### 修改详情

#### C-04 — Admin 后台邮箱功能接入真实 API

**背景**：审查发现 Admin 管理后台邮箱功能完全未接入后端 API："保存配置"按钮为空操作（`/* save */`），"发送验证码"按钮仅本地 `setEmailConfig({status:'verified'})` 伪造状态。后端已有完整路由 `PUT /api/admin/email-config` 和 `POST /api/admin/email-config/test`。

**修改文件**：

1. `shared/api-core.ts`
   - 新增 `updateEmailConfig(data)` 函数，调用 `PUT /api/admin/email-config`
   - 新增 `testEmailConfig(to)` 函数，调用 `POST /api/admin/email-config/test`

2. `admin/src/App.tsx`
   - import 新增 `updateEmailConfig`、`testEmailConfig`
   - 新增 state：`testEmailRecipient`、`emailSaving`、`emailTesting`、`emailMessage`
   - 新增 `handleSaveEmailConfig` 函数：调用 `updateEmailConfig` API，映射前端字段到后端字段（smtpServer→smtpHost, senderEmail→smtpUser/smtpFrom）
   - 新增 `handleSendTestEmail` 函数：调用 `testEmailConfig` API，成功后设置 status 为 verified
   - "保存配置"按钮：onClick 改为 `handleSaveEmailConfig`，添加 disabled 和 loading 文案
   - "发送测试邮件"按钮：onClick 改为 `handleSendTestEmail`，移除伪造状态逻辑
   - 移除原"6位验证码"输入框和"验证"按钮（后端 test 接口直接发送测试邮件，无需验证码）
   - 新增操作反馈消息区域（成功/失败提示）
   - 新增使用提示文字

### 验证结果

- ✅ `npx tsc --noEmit` TypeScript 编译通过（exit code 0，无错误）
- ✅ 保存配置按钮调用真实 API `PUT /api/admin/email-config`
- ✅ 发送测试邮件按钮调用真实 API `POST /api/admin/email-config/test`
- ✅ 移除伪造状态逻辑（`setEmailConfig({status:'verified'})`）
- ✅ 添加操作反馈和 loading 状态

### 影响范围

- **管理员体验**：现在可以真正保存 SMTP 配置并发送测试邮件
- **字段映射**：前端 smtpServer→后端 smtpHost，前端 senderEmail→后端 smtpUser+smtpFrom
- **UI 简化**：移除不必要的验证码输入框（后端 test 接口直接发送邮件）

---

## 2026-06-20 — P0-3 标记 Web Windows 版为废弃（C-03）

### 修复问题清单（1项）

| # | 编号 | 问题 | 类型 | 风险 |
|---|------|------|------|------|
| 1 | C-03 | Web Windows 版核心功能全部 mock，不具备实际使用价值 | 废弃 | 低 |

### 修改详情

#### C-03 — 标记 Web Windows 版为废弃

**背景**：审查发现 Web Windows 版（`web/web-win/`）仅 35% 合规：语音通话为 setTimeout 模拟，5 个功能面板全部 mock 数据，音乐机器人使用 MOCK_TRACKS，无 WebSocket 实时更新。由于 Windows 桌面客户端（`ridgericetalk-win/`，89% 合规）和 Web 网页版（`web/voice/`）已提供完整体验，此应用为重复的精简 UI 壳，维护成本高但价值低，决定标记为废弃。

**修改文件**：

1. `web-win/package.json`
   - 新增 `"deprecated"` 字段，说明废弃原因和替代方案

2. `web-win/index.html`
   - `<title>` 添加"（已废弃）"后缀
   - 新增可见的废弃提示横幅（橙色背景，固定顶部），引导用户使用 Windows 桌面客户端或 Web 网页版

3. `web-win/src/App.tsx`
   - 顶部新增 `@deprecated` JSDoc 注释，说明废弃原因、审查发现、替代方案

4. `web-win/README.md`（新增）
   - 说明废弃原因、审查发现的不合规项、替代方案对比表

### 验证结果

- ✅ 废弃标记正确显示（index.html 横幅 + package.json deprecated 字段 + App.tsx 注释 + README）
- ✅ 不影响其他仓库（仅修改 web-win/ 目录）
- ✅ 代码保留供参考，不删除

### 影响范围

- **用户体验**：访问 Web Windows 版会看到明显的废弃提示横幅
- **维护**：此应用不再接受功能更新或 bug 修复
- **替代方案**：用户被引导至 ridgericetalk-win/ 或 web/voice/

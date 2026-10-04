# RidgeRiceTalk Server

Go-based backend server for RidgeRiceTalk — an integrated voice chat platform with music bots, screen sharing, whiteboard, and more.

## Quick Start

```bash
# Install dependencies
go mod download

# Run with setup script (Linux/macOS)
./scripts/setup.sh

# Or on Windows PowerShell
./scripts/start-server.ps1

# Or directly
go run cmd/server/main.go
```

The server will start on `http://localhost:8080`.

### Windows

- **推荐测试启动**: 双击 `start.bat` 或运行 `./scripts/start-server.ps1`
- **直接运行二进制**: 双击 `ridgericetalk.exe`
  - 启动成功 → 显示访问信息横幅，窗口持久运行，按 `Ctrl+C` 停止
  - 启动失败 → 显示错误日志，按 `Enter` 关闭窗口
- **命令行启动** (cmd/PowerShell): 原有日志输出，无横幅、无 pause

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RRT_ENV` | `development` | Environment mode |
| `RRT_PORT` | `8080` | HTTP server port |
| `RRT_DATABASE_URL` | `./storage/ridgericetalk.db` | Database connection string |
| `RRT_DB_DRIVER` | `sqlite` | Database driver (`sqlite` or `postgres`) |
| `RRT_JWT_SECRET` | auto-generated | JWT signing secret |
| `RRT_CSRF_TOKEN_SECRET` | auto-generated | CSRF token secret |
| `RRT_LIVEKIT_URL` | `ws://localhost:7880` | LiveKit server URL |
| `RRT_LIVEKIT_API_KEY` | `devkey` | LiveKit API key |
| `RRT_LIVEKIT_API_SECRET` | `secret` | LiveKit API secret |
| `RRT_STORAGE_TYPE` | `local` | File storage (`local` or `s3`) |
| `RRT_LOCAL_DATA_PATH` | `./storage` | Local storage path |

See `.env.example` for the full list.

## Development

```bash
# Run tests
make test

# Format code
make fmt

# Build binary
make build

# Run with hot reload (requires air)
make dev
```

## Deployment

### Self-Contained Deployment (Recommended)

For a self-contained deployment that requires no manual installation of
LiveKit, NeteaseCloudMusicApi, or FFmpeg, use the embedded deployment scripts:

**Linux / macOS**

```bash
sudo ./scripts/deploy-embedded.sh --public-address http://YOUR_IP:8080
```

**Windows (Administrator PowerShell)**

```powershell
.\scripts\deploy-embedded.ps1 -PublicAddress "http://YOUR_IP:8080"
```

Both scripts:

- Check for Go (required), Node.js, npm, and git
- Copy the project to the deployment directory (`/opt/ridgericetalk` on
  Linux/macOS, `C:\ProgramData\RidgeRiceTalk` on Windows)
- Build the voice and admin frontends
- Build the Go server binary
- Generate `.env.production` with SQLite, auto-generated JWT/CSRF secrets, and
  `RRT_EMBEDDED_DEPS=true`
- Initialize storage directories

After deployment, start the server:

```bash
# Linux / macOS
cd /opt/ridgericetalk/server && ./ridgericetalk

# Windows
cd "C:\ProgramData\RidgeRiceTalk\server"; .\ridgericetalk.exe
```

Then visit `/admin` and use the bootstrap token from the logs to create the
owner account.

### Bare-Metal Deployment

For a traditional bare-metal deployment that uses systemd and PostgreSQL, use
the bare-metal deploy script:

```bash
sudo ./scripts/deploy-baremetal.sh --repo-url https://github.com/<org>/ridgericetalk.git
```

See [Deployment Doc §6.5](../文档/05-工程规范与运维/部署文档.md).

## Version & Update

RidgeRiceTalk uses **Semantic Versioning** (`MAJOR.MINOR.PATCH`).

- **Server version**: compile-time ldflags (`go build -ldflags "-X main.Version=1.2.3"`)
- **Client compatibility**: Server declares `min_client_version`, clients check on connect
- **Update check**: GitHub Releases API (configurable: stable/beta/rc channel)
- **API endpoints**:
  - `GET /api/server/version` — server version + feature flags
  - `GET /api/server/compatibility` — compatibility matrix
  - `GET /api/admin/update/check` — admin checks for server updates
  - `GET /api/client/update` — client checks for available updates

**Update levels**: `critical` (security fix) → `recommended` (bug fix) → `optional` (minor improvement)

## Voice Collaboration Design

### Voice Quality / Bitrate

| Quality | Bitrate | Channels | Sample Rate | Use Case |
|---------|---------|----------|-------------|----------|
| `fluent` | 24kbps | mono | 16kHz | Gaming, low-bandwidth |
| `standard` | 64kbps | stereo | 24kHz | Daily calls |
| `high` | 128kbps | stereo | 48kHz | Music sharing |
| `ultra` | 256kbps | stereo | 48kHz | Lossless music |

Configured per channel via `Channel.VoiceQuality`. LiveKit token generation includes matching room config.

### Voice State Synchronization

**Architecture**: LiveKit webhooks → Server processing → WebSocket broadcast

```
LiveKit Server          RidgeRiceTalk Server           Clients
      │                         │                         │
      │ participant_active      │                         │
      │ track_muted             │                         │
      │ track_unmuted           │                         │
      │ participant_disconnected│                         │
      └───────────Webhook──────→│                         │
                                │ 1. Update DB            │
                                │    VoiceParticipant     │
                                │ 2. Build WS message     │
                                │ 3. Broadcast to channel │
                                └────────WebSocket───────→│
                                                          │
                                                          │ Update UI
                                                          │ (speaking indicator,
                                                          │  mute icon, etc.)
```

**WebSocket Events**:

| Event | Direction | Trigger | Payload |
|-------|-----------|---------|---------|
| `voice_state_change` | S→C | LiveKit webhook | `{user_id, channel_id, event, timestamp}` |
| `raise_hand` | C→S | User clicks hand | `{user_id, channel_id, action}` |
| `raise_hand` | S→C | Broadcast to all | `{user_id, channel_id, action, raised_at}` |
| `screenshare_started` | S→C | Screen share start | `{user_id, username, channel_id, started_at}` |
| `screenshare_stopped` | S→C | Screen share stop | `{user_id, channel_id}` |

**Source of truth**: LiveKit webhooks
- `participant_active` → `IsSpeaking = true`
- `participant_inactive` → `IsSpeaking = false`
- `track_muted` → `IsMuted = true`
- `track_unmuted` → `IsMuted = false`
- `participant_disconnected` → Remove from VoiceParticipant

### Token Refresh Flow

```
Client              Server
  │                    │
  │ 1. Join voice      │
  │    GET /voice/token│
  │───────────────────→│
  │                    │ Generate token (5min expiry)
  │←───────────────────│ Return token
  │                    │
  │ 2. After 4min      │
  │    POST /voice/token/refresh
  │───────────────────→│
  │                    │ Validate: user still in channel?
  │                    │ Generate new token
  │←───────────────────│ Return new token
  │                    │
  │ 3. Client switches │
  │    to new token    │
```

**Rules**:
- Token valid for 5 minutes
- Client refreshes at 4 minutes (1 minute buffer)
- Refresh endpoint validates user is still in the voice channel
- Old token remains valid until expiry (graceful transition)

### Voice Channel Moderation

**Permission Matrix**:

| Action | OWNER | ADMIN | MEMBER (self) | MEMBER (others) |
|--------|-------|-------|---------------|-----------------|
| Join channel | ✅ | ✅ | ✅ | ✅ |
| Leave channel | ✅ | ✅ | ✅ | ✅ |
| Mute self | ✅ | ✅ | ✅ | ✅ |
| Deafen self | ✅ | ✅ | ✅ | ✅ |
| Mute others | ✅ | ✅ | ❌ | ❌ |
| Unmute others | ✅ | ✅ | ❌ | ❌ |
| Kick others | ✅ | ✅ | ❌ | ❌ |
| Start recording | ✅ | ✅ | ❌ | ❌ |
| Stop recording | ✅ | ✅ | ❌ | ❌ |
| Set presenter mode | ✅ | ✅ | ❌ | ❌ |
| Approve raised hand | ✅ | ✅ | ❌ | ❌ |

**Flow: Force Mute**

```
Admin clicks "Mute User"
  │
  ▼
POST /voice/participants/{user_id}/mute
  │
  ▼
Server:
  1. Check admin permission (OWNER/ADMIN)
  2. Check target user is in channel
  3. Send LiveKit API: MuteParticipantTrack
  4. Update VoiceParticipant.IsMuted = true
  5. Broadcast voice_state_change to channel
  6. Notify target user via WebSocket
```

**Flow: Kick User**

```
Admin clicks "Kick User"
  │
  ▼
POST /voice/participants/{user_id}/kick
  │
  ▼
Server:
  1. Check admin permission
  2. Send LiveKit API: RemoveParticipant
  3. Delete VoiceParticipant record
  4. Broadcast voice_state_change (event: left)
  5. Notify kicked user via WebSocket
```

**Presenter Mode**:
- Only host (OWNER/ADMIN) can enable
- All non-host users auto-muted on enable
- Host can approve raised hands to allow speaking
- Users can raise hand via WebSocket `raise_hand` event

### Recording & Transcription

**Recording Flow**:

```
Admin clicks "Start Recording"
  │
  ▼
POST /voice/recordings
  │
  ▼
Server:
  1. Check admin permission
  2. Check not already recording
  3. Send LiveKit API: StartRoomCompositeEgress
     (records audio + optional screen share)
  4. Create Recording record (status: recording)
  5. Broadcast recording_started to all participants
  6. Show recording indicator in UI

... time passes ...

Admin clicks "Stop Recording"
  │
  ▼
DELETE /voice/recordings
  │
  ▼
Server:
  1. Check admin permission
  2. Send LiveKit API: StopEgress
  3. Wait for file upload to complete
  4. Update Recording record:
     status: processing → completed
     file_url, duration_seconds, file_size_mb
  5. Broadcast recording_stopped
  6. If transcription enabled:
     Queue STT job (async)
```

**Recording Config**:

```toml
[recording]
enabled = true
storage_path = "./storage/recordings"
retention_days = 7
max_file_size_mb = 1024
format = "mp3"          # mp3, ogg, wav
bitrate_kbps = 128
include_screen_share = true
auto_transcribe = false
transcribe_language = "zh-CN"
```

**Transcription Flow**:

```
Recording completes
  │
  ▼
Server queues STT job
  │
  ▼
STT Worker (async):
  1. Download audio file
  2. Split into segments (30s each)
  3. Call STT API (Whisper/Azure)
  4. Combine results with timestamps
  5. Speaker diarization (who spoke when)
  6. Save Transcript record
  7. Link to Recording.transcript_id
```

**Privacy**:
- Recording indicator visible to all participants (red dot + text)
- User can opt-out: Settings → Privacy → "Don't include me in recordings"
- Opted-out users: audio excluded from recording (LiveKit track filter)
- Recordings accessible to all channel members (or admin-only, configurable)
- Auto-deleted after retention period

### Whiteboard Real-Time Sync

**Architecture**: Operational Transform (OT) with WebSocket broadcast

```
User A draws stroke          User B draws stroke
  │                            │
  ▼                            ▼
Client A sends              Client B sends
WS: stroke_draw             WS: stroke_draw
  │                            │
  └──────────┐    ┌──────────┘
             ▼    ▼
         Server Hub
             │
             ▼
    Operational Transform
    (merge concurrent ops)
             │
             ▼
    Broadcast to channel
    WS: stroke_draw (to all)
             │
      ┌──────┴──────┐
      ▼             ▼
  Client A      Client B
  (renders    (renders
   both)        both)
```

**Stroke Data Format**:

```json
{
  "type": "stroke_draw",
  "payload": {
    "stroke_id": "stk_001",
    "user_id": "usr_abc",
    "layer": 0,
    "tool": "pen",
    "points": [
      {"x": 100, "y": 200, "pressure": 0.8},
      {"x": 105, "y": 205, "pressure": 0.9}
    ],
    "color": "#FF5733",
    "width": 3,
    "timestamp": 1705313400
  }
}
```

**Conflict Resolution**:
- Each stroke has unique `stroke_id` (no conflicts on create)
- `stroke_erase` removes by `stroke_id`
- Concurrent draws: both strokes displayed (no merge needed)
- Concurrent erase + draw: last-write-wins for same `stroke_id`

**Sync Strategy**:
- Client sends strokes immediately (optimistic)
- Server validates and broadcasts
- Client maintains local stroke log for undo/redo
- On reconnect: client requests full stroke list, merges with local buffer

### Bot Voice Interaction

**Voice Command Flow**:

```
User says "机器人，播放周杰伦的晴天"
  │
  ▼
Client:
  1. VAD detects speech
  2. Keyword wake: "机器人" detected
  3. Record 5-second audio chunk
  4. Send to server: POST /bots/voice-command
  │
  ▼
Server:
  1. Receive audio (Opus/WebM)
  2. Call STT API (Whisper / Azure)
  3. Parse intent:
     - "播放 [歌曲名]" → Search → Add to queue
     - "下一首" → Skip
     - "暂停" → Pause
     - "继续" → Resume
     - "音量 [高/中/低]" → Adjust volume
  4. Execute command
  5. TTS response: "已添加周杰伦的《晴天》到播放队列"
  6. Play TTS audio via LiveKit
```

**Supported Voice Commands**:

| Command | Intent | Action |
|---------|--------|--------|
| "播放 [歌曲名]" | play_track | Search and add to queue |
| "下一首" | skip | Skip to next track |
| "上一首" | previous | Go to previous track |
| "暂停" | pause | Pause playback |
| "继续/播放" | resume | Resume playback |
| "音量高/中/低" | set_volume | Set volume (80/50/30) |
| "循环/随机/顺序" | set_mode | Change play mode |
| "清空队列" | clear_queue | Clear all tracks |

**TTS Configuration**:

```toml
[tts]
provider = "edge-tts"      # edge-tts, azure, google
voice = "zh-CN-XiaoxiaoNeural"
language = "zh-CN"
rate = "+0%"               # speech rate
volume = "+0%"             # speech volume
stream = true              # streaming mode (low latency)
```

**Volume Normalization**:

```
All audio files → FFmpeg replaygain scan → Apply gain adjustment → Normalize to -14 LUFS
```

## Frontend UI Design

> **注**：本节原以网页版 voice 前端代码（`web/voice/src/App.tsx`）为依据编写；该前端已于 2026-09-09 归档（源码存根 `归档数据/`），本节作为设计参考保留。

### 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│ TopBar: [服务器名称] [语音|功能] Toggle  ·  [主题] [设置]        │
├────────┬───────────────────────────────────┬────────────────────┤
│左侧栏   │           中央区域                  │     成员列表        │
│        │                                    │                   │
│ Bot状态 │  文字频道 → 消息列表 + 输入框      │  角色排序          │
│ ────── │  文字频道 → 消息列表 + 输入框      │  OWNER > ADMIN     │
│ 文字频道│  语音频道(双击加入)                │  > MEMBER          │
│  # 综合│  语音频道 → 语音房间界面            │  在线优先          │
│  # 技术│  Bot频道 → Bot控制面板             │  状态指示器        │
│ ────── │  功能模式 → 白板/文件/文档/       │  UserCard弹出      │
│ 语音频道│            日程/游戏              │                   │
│  🔊 大厅│                                    │                   │
│    └参与者│                                  │                   │
│  🔊 开黑│                                    │                   │
│ ────── │                                    │                   │
│ 底部    │                                    │                   │
│ 语音面板│                                    │                   │
└────────┴───────────────────────────────────┴────────────────────┘
```

**两种模式**（通过 TopBar 切换）：
- **语音模式**（`activeArea='voice'`）：左侧显示频道列表 + 底部语音控制栏
- **功能模式**（`activeArea='feature'`）：左侧显示功能模块列表（白板/云文件/文档/日程/小游戏）

---

### TopBar

```
┌──────────────────────────────────────────────┐
│ RidgeRiceTalk  [🔊 语音] [📐 功能] | ☀️ ⚙️  │
└──────────────────────────────────────────────┘
```

- 服务器名称显示
- 语音/功能模式切换按钮（`activeArea` 状态控制）
- 主题切换按钮（dark/light，写入 localStorage）
- 设置按钮（打开 SettingsModal）

---

### 左侧栏 — 语音模式

```
┌─────────────────────────────┐
│ 🎵 ⏸ 未在播放          [▼] │  ← Bot 状态栏（点击进入 Bot 面板）
├─────────────────────────────┤
│ 文字频道               [+] │
│  # 综合讨论                 │  ← 点击切换到消息区
│  # 技术分享                 │
├─────────────────────────────┤
│ 语音频道               [+] │
│  🔊 语音大厅           [3]  │  ← 点击选中 / 双击加入语音
│   ├ 张三(你)                │  ← 展开参与者列表
│   ├ 李四 🔇                 │  ← 静音图标
│   └ 王五 [绿圈]             │  ← 正在说话
│  🔊 游戏开黑           [2]  │
├─────────────────────────────┤
│ ┌─────────────────────────┐│
│ │ 已连接 · 语音大厅 · 8ms ││  ← 语音控制面板
│ │ [🔇] [🎧] [📹] [📞]   ││
│ └─────────────────────────┘│
└─────────────────────────────┘
```

**频道列表**：
- 文字频道带 `#` 图标，语音频道带 `🔊` 图标
- 语音频道显示 `channelParticipants` 人数角标
- 语音频道展开显示参与者：头像 + 用户名 + 说话绿圈 + 静音/拒听图标
- `[+]` 按钮弹出创建频道 Modal（文字/语音选择）

**语音参与者显示规则**：
- 从 `members` 中筛选出 `channelParticipants[c.id]` 中的用户
- 自己标记 `(你)` + 特殊样式
- 正在说话：`activeSpeakers.has(m.userId)` → 绿色边框高亮
- 已静音：显示 `MicOff` 图标
- 已拒听：显示 `HeadphoneOff` 图标

---

### 左侧栏 — 功能模式

```
┌─────────────────────────────┐
│ 功能模块                     │
│  🎨 白板                     │  ← WhiteboardPanel
│  📄 云文件                    │  ← CloudFilesPanel
│  📝 文档                     │  ← SharedDocsPanel
│  📅 日程                     │  ← SchedulePanel
│  🎮 小游戏                   │  ← MinigamesPanel
└─────────────────────────────┘
```

5 个功能模块，点击后在中央区域渲染对应 Panel 组件。

---

### 语音控制面板

**已连接状态**（`isInVoiceRoom=true`）：

```
┌─────────────────────────────────────────┐
│ 🟢 语音已连接                           │  ← 状态指示器
│ 语音大厅 · 8ms                          │  ← 频道名 + 延迟
├─────────────────────────────────────────┤
│ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐   │
│ │ 🔇   │ │ 🎧   │ │ 📹   │ │ 📞   │   │
│ │ 100% │ │ 80%  │ │共享   │ │离开   │   │
│ └──────┘ └──────┘ └──────┘ └──────┘   │
└─────────────────────────────────────────┘
```

**状态指示器**（`voiceStatus` 状态机）：
| 状态 | 显示 |
|------|------|
| `connecting` | 🟡 "连接中" |
| `connected` | 🟢 "语音已连接" + 延迟（ms） |
| `reconnecting` | 🟡 "重新连接中" |
| `reconnect_failed` | 🔴 "连接失败" |

**按钮**：
- **静音**（`muted`）：左键切换麦克风开/关，右键调出音量滑条（0-100%）
- **拒听**（`speakerMuted`）：左键切换扬声器开/关，右键调出音量滑条
- **屏幕共享**（`isSharingScreen`）：激活时高亮
- **取消/离开**（`handleCancelJoin` / `handleLeaveVoice`）

**未连接状态**（`isInVoiceRoom=false`）：

```
┌─────────────────────────────────────────┐
│ [头像] 张三                              │  ← 当前用户信息
│        🟢 在线                           │  ← presence状态 + 颜色指示器
│                                          │  ← 点击弹出状态切换菜单
│ ┌──────┐ ┌──────┐                       │
│ │ 🔇   │ │ 🎧   │                       │  ← 预览按钮
│ └──────┘ └──────┘                       │
└─────────────────────────────────────────┘
```

**在线状态切换**：点击头像旁的颜色圆点弹出菜单：
- 🟢 在线 (`online`) → `#22c55e`
- 🟡 离开 (`away`) → `#f59e0b`
- 🔴 请勿打扰 (`dnd`) → `#ef4444`
- ⚫ 隐身 (`invisible`) → `#6b7280`

**状态同步**：30 秒心跳 + 页面可见性切换 + `beforeunload` sendBeacon

---

### 成员列表（右侧栏）

```
┌──────────────────────────┐
│ 成员 — 8                 │
├──────────────────────────┤
│ 👑 赵六        [头像🟢] │  ← OWNER，在线优先
│ 🛡️ 张三        [头像🟢] │  ← ADMIN
│    🎯 专注开发           │  ← customStatus
│ 👤 李四        [头像🟢] │  ← MEMBER
│    🎮 GTA V              │  ← gameStatus
│ 👤 王五        [头像⚫] │  ← 离线
│ 👤 周七        [头像⚫] │
└──────────────────────────┘
```

**排序规则**（代码实现）：
1. `online=true` 的在前
2. 角色优先：OWNER → ADMIN → MEMBER
3. 用户名字母序

**每条成员信息**：
- `UserAvatar` 组件（带 `showStatusDot` 在线/离线指示器）
- `username` + `(你)` 标记自身
- 角色图标：👑 OWNER / 🛡️ ADMIN / 👤 MEMBER
- 在线时显示 `customStatus` 文本
- 点击/右键 → 弹出 `UserCard` 组件

**UserCard 弹出卡片**：
- 大头像 + 在线状态 + 角色标签
- 自定义状态文本
- `@提及` 按钮（点击后自动填入输入框 `@username `）
- 点击外部自动关闭

---

### 消息区（文字频道）

**消息组件状态**：`activeArea='voice'` + `TEXT` 频道 → 渲染 `renderMessages()`

**消息输入框**：
- 支持 @提及（通过 UserCard 按钮触发）
- 消息发送（Enter 键或发送按钮）

---

### 语音房间界面（语音频道）

**界面状态**：双击语音频道加入 → `renderVoiceRoom()`
- LiveKit 音频流管理
- 参与者音量独立控制（`participantVolumes` 持久化到 localStorage）
- Talking 指示器（`activeSpeakers` Set）

---

### Bot 控制面板

**入口**：点击左侧栏 Bot 状态栏或 Bot 频道

**子面板**：
- **播放队列**：展开/收起，显示当前播放歌曲、队列列表、清空按钮
- **播放模式**：顺序/随机/单曲循环/列表循环
- **音源 Tab**：网易云 / QQ音乐 / 本地上传 / TTS
- **网易云**：搜索、每日推荐、歌单、个人信息（二维码登录）
- **本地上传**：文件选择 + 上传 API
- **TTS**：文本转语音输入

**输出频道选择**：Dropdown 选择 Bot 音频输出到哪个语音频道

---

### 设置面板

入口：TopBar 设置按钮 → `SettingsModal`

**Tab 页**：
| Tab | 内容 |
|-----|------|
| 个人资料 | 修改昵称、自定义状态、主题 |
| 语音设备 | 麦克风/扬声器设备选择 |
| 语音设置 | 输入模式（VAD/PTT）、AEC、AGC、屏幕共享质量 |
| 快捷键 | 快捷键列表 |
| 通知 | 桌面通知开关（新消息/@提及/语音频道/屏幕共享） |
| 界面 | 主题切换 |
| 关于 | 版本信息 |

### 主题

**CSS 变量定义**（`styles.css`）：

| 变量 | 深色 | 浅色 |
|------|------|------|
| `--bg-primary` | `#0e1013` | `#ffffff` |
| `--bg-secondary` | `#171a1d` | `#f5f6f7` |
| `--bg-tertiary` | `#1e2226` | `#eaebec` |
| `--text-primary` | `#e0e0e0` | `#1a1a2e` |
| `--text-secondary` | `#8d9991` | `#6b7280` |
| `--accent` | `#29b6a8` | `#29b6a8` |
| `--error` | `#e74c3c` | `#e74c3c` |

切换方式：`[data-theme="light"]` / `[data-theme="dark"]` 属性

---

### 功能组件清单

| 组件 | 文件 | 说明 |
|------|------|------|
| `App` | `App.tsx` | 主应用，全部状态和布局 |
| `LoadingScreen` | 内联 | 加载中动画 |
| `AuthScreen` | 内联 | 登录/注册/忘记密码 |
| `SettingsModal` | 内联 | 设置面板（7 个 Tab） |
| `renderMessages` | 内联 | 文字频道消息区 |
| `renderVoiceRoom` | 内联 | 语音频道房间界面 |
| `renderBotPanel` | 内联 | Bot 控制面板 |
| `renderFeatureContent` | 内联 | 功能模块路由 |
| `UserAvatar` | `components/UserAvatar.tsx` | 头像组件（支持在线状态指示器） |
| `UserCard` | `components/UserCard.tsx` | 用户名片弹出卡片（@提及功能） |
| `WhiteboardPanel` | `components/WhiteboardPanel.tsx` | 白板 |
| `CloudFilesPanel` | `components/CloudFilesPanel.tsx` | 云文件 |
| `SharedDocsPanel` | `components/SharedDocsPanel.tsx` | 共享文档 |
| `SchedulePanel` | `components/SchedulePanel.tsx` | 日程 |
| `MinigamesPanel` | `components/MinigamesPanel.tsx` | 小游戏 |
| `ErrorBoundary` | `components/ErrorBoundary.tsx` | 错误边界 |

## Architecture

```
server/
├── cmd/              # Application entrypoints
│   ├── server/       # HTTP server
│   └── migrate/      # Database migration tool
├── internal/         # Private application code
│   ├── model/        # Database models
│   ├── database/     # Database connection
│   ├── config/       # Configuration management
│   ├── logger/       # Structured logging
│   ├── auth/         # Authentication & authorization
│   ├── user/         # User management
│   ├── channel/      # Channel & space management
│   ├── message/      # Messaging system
│   ├── voice/        # LiveKit voice integration
│   ├── bots/         # Music bot & TTS
│   ├── realtime/     # WebSocket hub
│   ├── admin/        # Admin panel API
│   └── ...           # Other feature modules
├── pkg/              # Public packages
│   ├── errors/       # Unified error codes
│   ├── crypto/       # Password hashing & tokens
│   ├── idgen/        # Snowflake ID generator
│   └── validator/    # Input validation
├── middleware/       # Gin middleware
├── webhost/          # Static frontend files
└── docker/           # Docker configuration

```

项目根目录：

```
migrations/           # Versioned SQL database migrations (used by cmd/migrate)
```
```

## 实体状态设计 (Entity State Design)

所有核心实体均定义了状态机。状态变更由明确的事件触发，服务端为唯一可信源。

### 用户在线状态 (UserPresence)

```
offline(离线) → online(在线) → away(离开) → online → dnd(勿扰) → online → offline
```

- **多设备聚合**：任意设备在线 = 显示 online，全部离线 = offline
- **闪断保护**：WebSocket 最后一个连接断开后等待 30 秒再标记 offline（允许重连）
- **心跳检测**：WebSocket ping/pong（30 秒）+ 前端 presence 心跳（30 秒）
- **自动离开**：5 分钟无操作自动变为 away
- **状态广播**：`PUT /api/users/:id/status` 成功后通过 WebSocket `presence_update` 实时广播
- **隐身隔离**：`invisible` 状态对外统一显示为 `offline`

**实现要点**:
- 服务端通过 `Hub.userClientCount` 维护每个用户的活跃连接数。
- 首个连接建立时触发 `OnUserCameOnline`，自动置为 `online` 并广播。
- 最后一个连接断开时启动 30 秒闪断保护定时器，超时后触发 `OnUserFullyOffline`，置为 `offline` 并广播。
- HTTP 状态接口与 WebSocket `presence_update` 消息共用同一持久化和广播路径。
- 持久化表：`user_presences`；启动时所有非 offline 状态会被重置为 offline。

### 消息生命周期 (Message)

```
客户端视角：
sending(发送中) → sent(已发送) → delivered(已送达) → read(已读)
   ↓
failed(发送失败，可重试)

服务端视角：
active(正常) → edited(已编辑，24h内，保留历史) → deleted(已删除，软删除)
```

- **去重**：ClientMessageID（UUID）防止重复发送
- **编辑窗口**：发送后 24 小时内可编辑
- **删除窗口**：发送后 24 小时内可自行删除，超期需 ADMIN 权限
- **已读回执**：每条消息每个用户一条 ReadReceipt 记录

### 语音房间 (VoiceRoom)

```
inactive(空闲) → active(活跃，第一人加入) → inactive(最后一人离开，延迟5分钟) → destroyed(销毁)
```

- **房间复用**：用户离开后房间保留，下次加入复用
- **延迟销毁**：最后一人离开 5 分钟后无新用户才销毁
- **锁定**：管理员可锁定房间，禁止新用户加入

### 音乐机器人播放 (BotPlayer)

```
idle(空闲) → buffering(缓冲中) → playing(播放中) → paused(暂停) → playing → idle(队列空)
         ↓
      error(错误，3次失败后停止)
```

- **单一状态字段**：`Status` 枚举替代 `Playing + Paused` 两个 bool，消除矛盾状态
- **自动切歌**：播放完毕自动加载下一首
- **错误恢复**：播放失败自动尝试下一首，连续 3 次失败后停止并通知

### 用户账号生命周期 (UserAccount)

```
pending(待验证) → active(正常)
                                    ↓
                              frozen(管理员冻结) → active(解冻)
                                    ↓
                              deactivated(自行注销，30天宽限期) → deleted(永久删除)
```

- **软删除**：用户删除后消息保留，作者显示为"已注销用户"
- **宽限期**：30 天内可登录恢复账号

### WebSocket 连接

```
connecting(连接中) → authenticating(认证中，10秒超时) → active(活跃) → subscribed(已订阅频道) → disconnected(已断开)
```

- **单用户最多 3 个并发连接**
- **异常断开保留状态 30 秒**，允许闪断重连恢复

### 其他实体状态

| 实体 | 关键状态 | 说明 |
|------|---------|------|
| **FileUpload** | uploading → processing → available / failed → deleted | 上传超时 5 分钟 |
| **MinigameSession** | waiting → starting → playing → paused / finished / abandoned | 等待超时 5 分钟 |

### 状态不一致处理

当数据库状态与实际状态不一致时，以**实际观测为准**：

- **WebSocket 断开 → 用户离线**：以断开事件为准
- **LiveKit 事件 → 语音状态**：以 LiveKit webhook 为准
- **定时任务修正**：每 5 分钟运行一次状态清理任务

## License

MIT

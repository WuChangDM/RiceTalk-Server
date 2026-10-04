# ridgericetalk-music-bot-worker

> RidgeRiceTalk 音乐 Bot 的 Node.js Agent Worker，基于 LiveKit Agents Framework。

## 职责定位

本模块是音乐 Bot 重构后的播放器后端，负责：

- 接收 Go 服务端的播放指令（HTTP API）
- 将音频转码为 Opus 格式并发布到 LiveKit
- 管理播放状态（playing/paused/currentTime/volume/playMode）
- 实现四种播放模式（order/repeat-all/repeat-one/random）
- 播放前向 Go 服务端懒解析短期音源 URL
- 在真人发言时平滑降低音乐音量，静默后恢复

**与 Go 服务端的关系**：Go 服务端保留队列持久化、权限校验、HTTP API 路由、WebSocket 广播；播放逻辑全部迁移到本 Worker。

## 目录结构

```
music-bot-worker/
├── src/
│   ├── index.ts              # HTTP 服务器 + 入口
│   ├── agent.ts              # LiveKit Agent 定义
│   ├── config.ts             # 配置管理（环境变量）
│   ├── logger.ts             # 日志（pino）
│   ├── audio/
│   │   └── voiceDucking.ts   # 真人发言检测与音量包络
│   ├── managers/
│   │   └── botManager.ts     # 会话、播放、队列、失败熔断
│   ├── providers/
│   │   └── musicSourceResolver.ts # 播放前懒解析音源
│   ├── routes/
│   │   ├── health.ts         # 健康检查 GET /internal/health
│   │   └── botRoutes.ts      # Bot 控制接口（play/stop/pause/skip...）
│   ├── tests/                # Node test 稳定性单元测试
│   └── utils/audioDecode.ts  # FFmpeg 重连、背压与 PCM 解码
├── package.json
├── tsconfig.json
├── .env.example              # 配置示例
└── README.md
```

## 本地开发

### 环境要求

- Node.js >= 20.0.0
- 正在运行的 LiveKit Server（可与 Go 服务端共享）
- 正在运行的 Go 服务端（用于接收状态回调）

### 安装依赖

```bash
cd ridgericetalk/music-bot-worker
npm install
```

### 配置

```bash
cp .env.example .env
# 编辑 .env，填入 LiveKit 和 Go 服务端的配置
```

### 开发运行

```bash
npm run dev   # 热重载模式
npm run build # 编译 TypeScript
npm test      # 编译并运行稳定性单元测试
npm start     # 运行编译后的代码
```

### 验证

```bash
# 健康检查
curl http://127.0.0.1:5012/internal/health

# 预期输出：
# {
#   "status": "ok",
#   "service": "ridgericetalk-music-bot-worker",
#   "version": "0.1.0",
#   "timestamp": "...",
#   "uptime": 1.23
# }
```

## 部署

### systemd 服务

服务模板文件位于 `systemd/ridgericetalk-music-bot.service`，部署时会被安装到 `/etc/systemd/system/`。

```bash
# 启动
sudo systemctl start ridgericetalk-music-bot

# 查看状态
sudo systemctl status ridgericetalk-music-bot

# 查看日志
sudo journalctl -u ridgericetalk-music-bot -f
```

## 与 Go 服务端的交互

### Go → Worker（HTTP API）

| 路由 | 方法 | 说明 |
|------|------|------|
| `/internal/health` | GET | 健康检查 |
| `/internal/bots/:botId/play` | POST | 播放指定曲目 |
| `/internal/bots/:botId/stop` | POST | 停止播放 |
| `/internal/bots/:botId/pause` | POST | 暂停 |
| `/internal/bots/:botId/resume` | POST | 恢复播放 |
| `/internal/bots/:botId/skip` | POST | 下一首 |
| `/internal/bots/:botId/previous` | POST | 上一首 |
| `/internal/bots/:botId/seek` | POST | 跳转 |
| `/internal/bots/:botId/volume` | POST | 音量 |
| `/internal/bots/:botId/play-mode` | POST | 播放模式 |
| `/internal/bots/:botId/status` | GET | 查询状态 |
| `/internal/bots/:botId/disconnect` | POST | 断开 LiveKit |

### Worker → Go（内部解析）

| 路由 | 方法 | 说明 |
|------|------|------|
| `/api/v1/internal/music-bot/resolve` | GET | 在播放前解析网易云临时 URL 或上传文件绝对路径 |

Go 与 Worker 应配置相同的 `RRT_MUSIC_BOT_WORKER_TOKEN` /
`GO_SERVER_INTERNAL_TOKEN`。未配置 token 时，双方只接受回环地址调用。

## 重构进度

参考 [DES-2026-0728-01 设计文档](../../文档/设计/DES-2026-0728-01-livekit-agents-music-bot-refactor.md)。

- [x] 阶段 1：搭建基础设施
- [x] 阶段 2：核心播放能力（Play/Stop）
- [x] 阶段 3：播放控制（Pause/Resume/Seek/Volume）
- [x] 阶段 4：队列与四种播放模式
- [x] 阶段 5：播放前音源懒解析与内部鉴权
- [x] 阶段 6：真人发言音乐 ducking
- [ ] 阶段 7：清理 Go 服务端旧代码

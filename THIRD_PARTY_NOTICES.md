# Third-Party Notices

RidgeRiceTalk 服务端使用了以下开源组件。各组件版权归其原作者所有，
本仓库自有代码以 **GNU AGPL-3.0** 许可发布（见 LICENSE）；各第三方组件依其自身许可分发。

## 服务端（Go，见 server/go.mod）

| 组件 | 用途 | 许可 |
|------|------|------|
| gin-gonic/gin | HTTP 框架 | MIT |
| gorm.io/gorm + sqlite/pgsql 驱动 | ORM 与数据库访问 | MIT |
| golang-jwt/jwt | 鉴权令牌 | MIT |
| golang.org/crypto | bcrypt 等加密原语 | BSD-3-Clause |
| google/uuid | UUID | BSD-3-Clause |
| gorilla/websocket 或 nhooyr/websocket | WebSocket（以 go.mod 实际为准） | MIT/BSD |

## 实时语音

| 组件 | 用途 | 许可 |
|------|------|------|
| LiveKit（`livekit/bin/` 内置二进制） | SFU 语音房间 | Apache-2.0 |

## 内嵌服务

| 组件 | 用途 | 许可 |
|------|------|------|
| NeteaseCloudMusicApi（`services/netease-api`） | 音乐机器人音源 API（社区项目） | MIT（随上游） |
| music-bot-worker | 音乐播放工作进程 | 同本仓库 |

## 前端预构建产物（webhost/dist 与 web/）

| 组件 | 用途 | 许可 |
|------|------|------|
| React | UI 框架 | MIT |
| Radix UI | 无障碍基础组件 | MIT |
| lucide-react | 图标 | ISC |
| Vite / TypeScript 工具链 | 构建（仅开发期） | MIT / Apache-2.0 |

## 部署脚本依赖（运行环境自带）

bash / curl / git / systemd / PostgreSQL / SQLite（Public Domain）。

---

完整依赖清单与精确版本以 `server/go.mod`、`services/netease-api/package.json`
及 `web/` 各子项目 `package.json` 为准。如 claiming 遗漏，请提 Issue，我们会尽快补正。

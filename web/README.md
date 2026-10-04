# web — 网页前端（admin + shared）

> **⚠️ 网页版主应用 `voice/` 已暂停开发并归档（2026-09-09）**：本工作区 `web/voice` 的完整源码已移出，归档于根工作区 `归档数据/2026-09-09-网页端voice-归档/`（`workspace-web-voice/` 为最新版，`server-repo-web-voice/` 为服务端仓库内被删前的部署构建源快照）。恢复网页端 voice 开发时，从该归档取回源码放回 `web/voice` 即可。
>
> 本文档描述**保留部分**：管理后台 `admin/` 与跨端共享代码源 `shared/`。语音（voice 频道/模块）功能本身仍在 Windows 客户端与服务端继续开发，与网页端 voice 应用归档无关。
>
> 注：本目录 `web/voice` 对应代码在服务端仓库内的规范副本原为 `ridgericetalk/web/voice`，两处 voice 均已归档；`web/admin`、`web/shared` 在两棵目录树中都保留。

完整 Agent 行为规范见 [../AGENTS.md](file:///c:/RICETALK/AGENTS.md)，项目总览见 [../README.md](file:///c:/RICETALK/README.md)。

## 目录结构

```
web/
├── admin/                         # 管理后台前端（端口 5174）
├── shared/                        # 跨端共享代码源（唯一源，详见下文）
├── tests/                         # 前端测试、dogfood 截图、验证报告
├── dist/                          # 构建产物（不应入版本库）
├── web-win.archived-20260630/     # 已废弃的历史副本（原 web/web-win/，不再维护，勿改）
└── CHANGELOG.md                   # 本仓库变更日志
```

> 网页版 voice 源码与旧副本已于 2026-09-09 移入 `归档数据/`，不再占用本目录。

## 仓库职责定位

- `admin/`：管理后台前端（登录、频道/用户/模块/存储/运行时监控、服务器设置），端口 5174
- `shared/`：跨端共享代码**唯一源**（API 封装、类型、工具），供 admin 与 `ridgericetalk-win` 使用

两个部分各自独立，无 monorepo 工作区配置（无根 package.json），各自有独立的 `package.json`、`vite.config.ts`、`tsconfig.json`。

### `admin/` 详细结构

```
admin/
├── src/
│   ├── App.tsx                    # 管理后台主应用壳
│   ├── main.tsx                   # React 渲染入口
│   ├── styles.css                 # 全局样式
│   ├── components/                # 后台功能面板组件
│   │   ├── LoginForm.tsx          # 登录表单
│   │   ├── Sidebar.tsx            # 侧边导航
│   │   ├── ChannelsPanel.tsx      # 频道管理
│   │   ├── UsersPanel.tsx         # 用户管理
│   │   ├── ModulesPanel.tsx       # 模块启停管理
│   │   ├── StoragePanel.tsx       # 存储管理
│   │   ├── RuntimePanel.tsx       # 运行时监控
│   │   └── ServerSettingsPanel.tsx # 服务器设置
│   ├── types/admin.ts             # 后台专用类型
│   └── utils/format.ts            # 格式化工具
├── dist/                          # 构建产物
├── index.html
├── package.json                   # 依赖与脚本（name: ridge-ricetalk-admin）
├── vite.config.ts
└── tsconfig.json
```

依赖轻量（仅 React 19 + lucide-react + Vite + Vitest，无 LiveKit / Zustand / Router）。

### `shared/` 详细结构

`shared/` 是跨端共享代码**源码**（被 admin 通过相对路径引用，并被单向同步到 `ridgericetalk-win/src/shared/`）：

| 文件 | 职责 |
|------|------|
| `api-core.ts` | HTTP 请求核心封装（fetch 包装、错误处理、token 刷新、`notifyAuthExpired()` 派发 `rrt:auth-expired` 事件、音效/语音虚拟身份等 API） |
| `types.ts` | 跨端共享的类型定义（用户、频道、消息等数据契约） |
| `doudizhu-rules.ts` | 斗地主规则（联机小游戏） |
| `errors.ts` | 统一错误类型定义 |
| `useModalFocus.ts` | 模态焦点管理 Hook（无障碍/键盘焦点约束） |

## 本地开发流程

### 常用命令

| 用途 | admin 命令 |
|------|-----------|
| 安装依赖 | `cd admin && npm install` |
| 开发服务 | `cd admin && npm run dev`（端口 5174） |
| 构建 | `cd admin && npm run build` |
| 预览构建 | `cd admin && npm run preview` |
| 单元测试 | `cd admin && npm run test`（Vitest） |

### 主要依赖

- **admin**：react ^19、react-dom ^19、lucide-react（无 LiveKit / Zustand / Router）
- **开发依赖**：@testing-library/react、@vitejs/plugin-react、@vitest/coverage-v8、Vitest、Vite、TypeScript
- 网页版 voice 的技术栈（react-router / zustand / livekit-client / Playwright 等）已随归档移出，见归档 README

## 与其他仓库的依赖关系

### 跨端共享代码同步（web/shared → ridgericetalk-win/src/shared）

`shared/` 是跨端共享代码的**唯一源**。`ridgericetalk-win/src/shared/` 是从本仓库 `shared/` 同步过来的构建产物，**禁止直接修改下游**。

修改流程见 [../AGENTS.md → 跨端共享代码同步](file:///c:/RICETALK/AGENTS.md#跨端共享代码同步ridgericetalk-win)。

### 服务端对接

- admin 与（归档前的）voice 通过 HTTP REST API + WebSocket 接入 `ridgericetalk/` 服务端
- admin 构建产物部署到 `ridgericetalk/webhost/dist/admin/`，由服务端内嵌托管；`webhost/dist/voice/` 为 voice 归档前的最后构建产物，不再重建

## 测试体系

- **单元测试**：Vitest（`*.test.ts(x)` 就近放置在 `__tests__/` 或 `test/` 目录）
- **手动 QA**：`tests/manual/`、`tests/dogfood/`、`tests/verification-shots/`
- voice 的 Playwright E2E 与组件测试已随源码归档

## 变更日志

详见 [CHANGELOG.md](file:///c:/RICETALK/web/CHANGELOG.md)。

## 相关文档

- API 规范：`../ridgericetalk/openapi.yaml` 与 `../文档/03-服务端与数据库/openapi.yaml`
- WebSocket 协议：`../文档/03-服务端与数据库/websocket协议规范.md`
- 网页端 voice 归档说明：根 `归档数据/2026-09-09-网页端voice-归档/README.md`

## License

MIT

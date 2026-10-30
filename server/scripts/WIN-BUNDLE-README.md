# RidgeRiceTalk Windows 服务端部署包（{{VERSION}}）

> 版本：{{VERSION}}　修订：{{REVISION}}　构建日期：{{BUILD_DATE}}
> 目标机**无需安装 Go / Node / npm / PostgreSQL / LiveKit**，解压即用（数据库为内置 SQLite）。

## 一、最短路径（三步）

1. **解压** 本包到任意目录，例如 `D:\RidgeRiceTalk-Pkg`
   （目录名不要带空格更稳；解压后应能看到 `server\`、`migrations\`、`console\`、`start-server.bat`）
2. **右键 `start-server.bat` →「以管理员身份运行」**
   - 脚本会自动请求管理员提权；它会：解包到 `C:\RidgeRiceTalk` → 生成 `.env.production` → 放通防火墙 → 生成「启动服务端.cmd」→ 后台启动服务端 → 安装桌面控制台（桌面 + 开始菜单快捷方式）→ 就绪后弹出控制台
3. **首次使用**：控制台会内嵌打开 `http://<对外地址>:9090/admin/`；首次需要初始化 Owner，
   **引导令牌打印在服务端启动日志里**（见 `C:\RidgeRiceTalk\logs\`，或「启动服务端.cmd」窗口）。

> 想先看会发生什么而不改本机？在 PowerShell 里跑：
> ```powershell
> powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\deploy-windows.ps1 -FromBundle ".\." -PublicAddress "http://127.0.0.1:8080" -DryRun
> ```

> **⚠⚠ 升级 vs 全量部署（数据保全，必读）**
>
> - **升级（保留数据，唯一推荐）**：`.\scripts\deploy-windows.ps1 -FromBundle .\. -SkipDeploy -PublicAddress "http://<地址>"`
>   只更换程序文件不重解包，`server\storage\` 数据完全不动。
> - **全量部署（`-FromBundle` 不带 `-SkipDeploy`）仅限首次安装。** 若目标目录已存在数据库，
>   脚本会**先自动备份**（`<部署目录>\_data-backup\<UTC>\ridgericetalk.db.bak-<UTC>`）、
>   然后**直接拒绝**并中止，原始数据零改动。
> - **确需重置数据**（例如换机重建）：追加 `-AllowDataReset`。旧 `server\storage` 会整体归档到
>   `<部署目录>\_storage.bak.<时间戳>`（**不会被删除**），随后新建空库；回滚时停服 → 删除新 `storage` →
>   把归档目录改回 `server\storage` 即可。
> - **数据目录只有一个**：`<部署目录>\server\storage\`（SQLite `ridgericetalk.db` + 上传文件 + 预置音频）。
>   它**永远不会**被部署脚本清理或覆盖。

## 二、端口规划（默认）

| 用途 | 默认 | 说明 |
|---|---|---|
| API / Web（含 `/admin/` 反代入口） | 8080 | `http://<IP>:8080/api/health` 健康检查 |
| 管理后台独立端口 | 9090 | `http://<IP>:9090/admin/` |
| LiveKit 信令 WS | 7880 | 语音需要 |
| LiveKit TCP 回退 | 7881 | 语音需要 |
| LiveKit UDP | 7882 | 语音需要 |

改端口：编辑 `start-server.bat` 顶部 5 个变量，或命令行透传，例如
`start-server.bat -PortApi 18080 -PortAdmin 19090 -PortLkWs 17880 -PortLkTcp 17881 -PortLkUdp 17882`。

`-PublicAddress` 为必填参数（所有模式，含 `-SkipDeploy`）。

## 三、防火墙与公网访问

- 脚本默认用 `New-NetFirewallRule` 放通上述 5 个入站端口（规则名前缀 `RidgeRiceTalk-`）。
- 自备防火墙时加 `-NoFirewall`。
- 公网/NAT 部署还需在云安全组或路由器上做同样的端口映射；**LiveKit TCP/UDP 端口必须 1:1 映射**。
- `PUBLIC_ADDRESS`（对外地址）默认取本机第一个非回环 IPv4；公网域名场景请手动指定：
  `set PUBLIC_ADDRESS=https://talk.example.com` 后再运行，或直接调用
  `scripts\deploy-windows.ps1 -FromBundle ".\." -PublicAddress "https://talk.example.com"`。

## 四、管理员权限

- 放通防火墙、写 `C:\` 下目录、创建快捷方式都需要管理员权限；`start-server.bat` 已自带提权。
- **不加 `-NoFirewall` 时必须用管理员**：`New-NetFirewallRule` 在普通权限下会失败（部署脚本会报错退出）；
  普通权限机器请加 `-NoFirewall`（自己手动放通端口），或改用 `D:\rt` 这类非系统目录部署。
- 只用普通用户目录部署（例如 `D:\rt`）且加 `-NoFirewall` 时，普通权限也能跑，但不要混用。

## 五、语音（LiveKit）——本包 **{{LIVEKIT}}**

部署脚本按 `livekit\livekit-server.exe` 是否存在，自动决定是否随服务端拉起
（存在 → `.env.production` 写 `RRT_LIVEKIT_AUTOSTART=true`；不存在 → `false`，只 WARN 不阻断）。

- **包内已内置 LiveKit 时**（`MANIFEST.sha256` 的 `LIVEKIT=included`）：部署后 LiveKit 由服务端自动拉起，语音开箱可用。
  需要放通/映射 **TCP 7880、TCP 7881、UDP 7882**（或本机自定义的对应端口）。
- **包内不含 LiveKit 时**：文字频道、管理后台、文件、白板、日程等全部可用；**语音频道不可用**。要补语音，二选一：
  1. 把 Windows 版 `livekit-server.exe` 放到 `<部署目录>\livekit\livekit-server.exe`，
     然后在 `<部署目录>\server\.env.production` 里设 `RRT_LIVEKIT_AUTOSTART=true` 并重启服务端；
  2. 或用外部 LiveKit：把 `RRT_LIVEKIT_URL` / `RRT_LIVEKIT_PUBLIC_URL` 指向已有实例。

> **⚠ 停服会连 LiveKit 一起停**
> 控制台「停止」按钮或 `console\stop-server.ps1` 会同时结束**本部署目录下**的 `server\ridgericetalk.exe`
> 和 `livekit\livekit-server.exe`（按 `ExecutablePath` 精确匹配，绝不按镜像名批量杀，不会影响同机其它实例）。
> 若你用的是**外部 LiveKit**，本脚本不会动那台实例，不受影响。
> 部署/覆盖升级（`-Force`）与再次启动前，脚本也会先清理本目录的 LiveKit 残留，保证单实例、不撞端口。

## 六、常用操作

| 操作 | 做法 |
|---|---|
| 启动 / 重启 | 双击 `启动服务端.cmd`（关闭窗口即停止服务端）；控制台「重启」= 先停两类进程再起 |
| 停止服务 | 控制台「停止」按钮，或 `powershell -NoProfile -ExecutionPolicy Bypass -File console\stop-server.ps1`（**同时停服务端与 LiveKit**） |
| 打开控制台 | 桌面「RidgeRiceTalk 服务端控制台」图标 |
| 查看日志 | `C:\RidgeRiceTalk\logs\`（另有 `server\storage\` 数据目录） |
| 备份数据 | 停服后拷贝 `C:\RidgeRiceTalk\server\storage\`（含 SQLite 库与上传文件） |
| 升级（保留数据，推荐） | `deploy-windows.ps1 -FromBundle <新包> -SkipDeploy -PublicAddress <地址>` |
| 全量部署（仅首次安装） | `-FromBundle <包> -PublicAddress <地址>`（**有数据库时会被拒绝**，需 `-AllowDataReset` 才重置） |
| 只改端口/对外地址（不重装程序） | 用 `-SkipDeploy`（见下方注意事项，**不是只读检查**） |

> **⚠ 注意：`-SkipDeploy` 不是「纯只读检查」，也不是「什么都不动」**
>
> 它**只**表示「不重新解包、不覆盖程序文件（`ridgericetalk.exe` / 前端 `dist` / `migrations` / `console`）」。
> 部署脚本仍会照常：① 按本次传入的端口与 `-PublicAddress` **重写** `server\.env`、`server\.env.production`；
> ② **重新生成** `启动服务端.cmd`；③ **重写** `console\server-console.json` 与 `console\stop-server.ps1`；
> ④ **重启服务端进程**（PID 会变，SQLite 的 `-wal`/`-shm` 随之刷新）。
>
> 若只想「看看会发生什么、不碰任何文件」，请用 **`-DryRun`**（只打印计划、不落盘）。
> `-SkipDeploy` 适合「只改端口/对外地址」的场合，但请预期配置被覆盖与一次服务重启。

## 七、卸载

1. 关闭「启动服务端.cmd」窗口，或任务管理器结束 `ridgericetalk.exe`；
2. 删除 `C:\RidgeRiceTalk` 整个目录（**先备份 `server\storage\`**）；
3. 删除桌面与开始菜单的「RidgeRiceTalk 服务端控制台」快捷方式；
4. 如需清理防火墙规则：`Get-NetFirewallRule -DisplayName 'RidgeRiceTalk*' | Remove-NetFirewallRule`。

## 八、常见问题

| 现象 | 处理 |
|---|---|
| `start-server.bat` 一闪而过 | 用「以管理员身份运行」；或在 PowerShell 里手动跑 `scripts\deploy-windows.ps1` 看报错 |
| 控制台空白 / 打不开 | 需要 **WebView2 Runtime**（Win10/11 通常已内置；缺则装 Microsoft Edge WebView2 Runtime） |
| `/admin/` 404 | 确认包内 `server\webhost\dist\admin\index.html` 存在；本包已内嵌，不应出现 |
| 端口被占用 | 换端口（见第二节），或先停掉占用进程 `netstat -ano \| findstr :8080` |
| 部署报 `New-NetFirewallRule` 失败 / 提权失败 | 当前不是管理员：右键「以管理员身份运行」`start-server.bat`，或加 `-NoFirewall` 后手动放通端口 |
| 停服后 7880/7881/7882 仍被占用 | 正常路径请用控制台「停止」或 `console\stop-server.ps1`（会同时停 LiveKit）；若脚本提示「仍有进程未退出」，用 `netstat -ano` 找 PID 手动结束，并确认没有用外部 LiveKit |
| 健康检查一直失败 | 看 `logs\` 与常驻窗口；SQLite 目录不可写时也会失败 |
| 首次登录没令牌 | 重启服务端看启动日志里的 bootstrap token；或在控制台「日志」页查看 |

## 九、包内清单

```
ridgetalk-win-server-{{VERSION}}/
  server/ridgericetalk.exe              服务端主程序（windows/amd64，静态）
  server/webhost/dist/**                内嵌前端（admin / voice 资源）
  migrations/**                         数据库迁移 SQL
  console/ServerConsole.exe + dll       Windows 桌面控制台（WinForms + WebView2）
  scripts/deploy-windows.ps1            部署脚本（-FromBundle 模式）
  start-server.bat                      双击入口（提权 + 部署 + 启动）
  README-deploy.md                        本文件
  MANIFEST.sha256                       每文件 sha256 + 字节数
  SHA256SUMS                            可 `sha256sum -c` 校验
```

`MANIFEST.sha256` 头部字段：`VERSION` / `REVISION`（打包时 HEAD）/ `BINARY_COMMIT`（二进制实际构建提交，经 `-X ridgericetalk/core/version.Commit` 注入）/ `WORKTREE` / `BUILD_DATE` / `PLATFORM` / `LIVEKIT`。

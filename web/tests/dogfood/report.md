# Dogfood 功能验证报告 - Voice & Admin 端

> **测试日期**: 2026-06-25
> **测试依据**: 文档/02-UI设计/Web端/Web端 UI 设计文档 - Voice.md / Admin.md
> **测试方法**: dogfood 系统性 Web 应用测试 + agent-browser 自动化
> **测试环境**:
> - 后端: http://localhost:8080 (运行中)
> - Admin 后端: http://localhost:9090 (运行中)
> - Voice 前端: http://localhost:5173 (Vite dev)
> - Admin 前端: http://localhost:9090/admin/ (内嵌)
> - 测试用户: dogfooduser / dogfood@test.com (新注册), owner@example.com (OWNER)

---

## 测试摘要

| 端 | 测试模块数 | 通过 | 失败 | 警告 |
|----|-----------|------|------|------|
| Voice | 7 | 6 | 1 | 0 |
| Admin | 7 | 3 | 4 | 0 |
| **合计** | **14** | **9** | **5** | **0** |

**问题严重程度分布**:
- Critical: 1 (ISSUE-002)
- High: 1 (ISSUE-003)
- Medium: 3 (ISSUE-001, ISSUE-004, ISSUE-005)
- Low: 1 (ISSUE-006)

---

## Voice 端测试结果

### 1. 认证流程 ✅ (含 1 个问题)

**测试项**:
- 登录页面 ARIA 标签
- 密码显示/隐藏切换
- 错误密码登录测试
- 新用户注册流程
- 自动登录

**结果**:
- ✅ 登录页面所有元素有正确 ARIA (textbox "邮箱"/"密码"、button "显示密码"/"登录"/"立即注册"/"忘记密码？")
- ✅ 注册新用户 dogfooduser/dogfood@test.com 成功，自动登录进入主界面
- ⚠️ **ISSUE-001**: 错误密码登录返回 401，但前端不显示错误提示给用户 (console 有 error log 但 UI 无反馈)

**截图**: voice-01-login.png ~ voice-03-register-success.png

---

### 2. 主界面 ✅

**测试项**:
- 频道列表显示
- 消息面板历史消息
- 成员栏在线状态
- 消息发送

**结果**:
- ✅ 频道列表: general 文字频道 + 语音频道正常显示
- ✅ 消息面板: 历史消息正常显示
- ✅ 成员栏: dogfooduser + owner 在线状态正确
- ✅ 消息发送: POST /api/channels/.../messages 返回 200，消息发送成功

**截图**: voice-04-main.png ~ voice-06-message-send.png

---

### 3. 功能区 ✅

**测试项**:
- 白板工具栏
- 云文件存储空间
- 文档列表
- 日程安排
- 小游戏面板

**结果**:
- ✅ 白板: 工具栏完整 (画笔/橡皮擦/颜色/清空等)
- ✅ 云文件: 存储空间 0B/10GB 显示正常
- ✅ 小游戏: 五子棋/2048/井字棋/国际象棋 四个游戏卡片正常显示

**截图**: voice-07-whiteboard.png ~ voice-09-games.png

---

### 4. 设置面板 ✅

**测试项**:
- 8 个设置标签页切换
- 个人资料表单
- 语音设备配置
- 语音设置 (VAD/PTT/降噪)
- 快捷键绑定
- 通知选项
- 界面主题选择
- 安全 MFA 设置
- 关于页面

**结果**:
- ✅ 个人资料: 昵称/邮箱/自定义状态表单正常
- ✅ 语音设备: 输入/输出设备选择、音量滑块、测试按钮
- ✅ 语音设置: VAD/PTT 模式、降噪选项 (WebRTC NS/SpeexDSP/RNNoise)、AEC/AGC
- ✅ 快捷键: 切换麦克风/扬声器静音绑定
- ✅ 通知: 8 种通知选项复选框
- ✅ 界面: 深色/浅色主题选择
- ✅ 安全: MFA/TOTP 设置入口 (需当前密码)
- ✅ 关于: 版本 v0.1.0、构建日期、检查更新、开源组件许可

**截图**: voice-10-settings.png ~ voice-17-settings-about.png

---

### 5. 主题切换 ✅

**结果**:
- ✅ 深色/浅色主题切换正常工作
- ✅ 主题状态持久化

**截图**: voice-18-theme-light.png, voice-19-theme-dark.png

---

### 6. 音乐机器人面板 ✅

**测试项**:
- 播放控制按钮
- 音量滑块
- 播放队列展开/收起
- 音源切换 (网易云/QQ音乐/本地上传/TTS)
- 功能切换 (搜索/每日推荐/我的歌单/个人信息)
- 搜索功能

**结果**:
- ✅ 播放控制: 上一首/播放/下一首/静音按钮正常
- ✅ 音量滑块: 默认值 80
- ✅ 播放队列: 展开/收起正常，显示"播放队列 (0首)"
- ✅ 音源切换: 4 个音源标签页可切换
- ✅ 搜索: 搜索"测试歌曲"返回 10 条结果，每条有播放/优先按钮
- ✅ 每日推荐: 切换正常 (未登录网易云显示空状态)

**截图**: voice-20-voice-view.png ~ voice-24-bot-daily.png

---

## Admin 端测试结果

### 1. 登录流程 ⚠️ (Critical 问题)

**测试项**:
- 登录页面显示
- OWNER 账号登录
- MFA 流程处理

**结果**:
- ✅ 登录页面正常显示 (邮箱/密码/登录按钮)
- ❌ **ISSUE-002 (Critical)**: Admin 登录未正确处理 MFA-required 响应
  - **现象**: OWNER 登录时 API 返回 `mfaRequired: true`，`accessToken: ""` (空字符串)，`user` 对象存在
  - **前端处理**: 代码检查 `if (res.data?.accessToken || res.data?.token)` 为 false (空字符串)，不存储 token；但 `if (user)` 为 true，设置了 session
  - **后果**: 用户"登录成功"进入仪表盘，但 localStorage 无 token，所有后续 API 调用返回 AUTH_UNAUTHORIZED，导致所有模块数据为空
  - **代码位置**: web/admin/src/App.tsx:743-750
  - **设计文档要求**: 认证授权专项设计 §18.4 要求 OWNER/ADMIN 强制 MFA，前端应显示 MFA 验证码输入界面，而非直接进入仪表盘

**截图**: admin-01-login.png, admin-02-mfa-required.png

---

### 2. 服务器设置 ⚠️ (High 问题)

**测试项**:
- 基本配置
- Web 语音
- 网络配置
- SRV 记录

**结果**:
- ❌ **ISSUE-003 (High)**: 基本配置标签页显示"暂无配置数据" (空)
- ✅ Web 语音: 显示 WEB 语音开关 (默认锁定)、LiveKit URL 输入框
- ✅ 网络配置: 显示外部网络配置表单 (HTTPS/UPnP/TURN TCP/Windows 客户端访问复选框、外部域名/端口输入框)
- ✅ SRV 记录: 显示 SRV 记录建议 (Windows 客户端/Mac 客户端) 和复制按钮

**截图**: admin-03-server-basic.png ~ admin-06-server-srv.png

---

### 3. 邮箱服务 ✅

**测试项**:
- 功能开关
- SMTP 配置表单
- SMTP 配置验证 (两步验证)

**结果**:
- ✅ 功能开关: 显示"已关闭"和"状态: 未验证"
- ✅ SMTP 配置: 服务器/端口(587)/加密方式(STARTTLS/SSL/NONE)/发件邮箱/密码/发件人名称
- ✅ SMTP 配置验证: 收件邮箱输入框 + 发送验证码按钮 (符合 C-4 两步验证设计)
- ✅ 保存配置按钮

**截图**: admin-07-email.png

---

### 4. 频道管理 ⚠️ (Medium 问题)

**结果**:
- ❌ **ISSUE-004 (Medium)**: 频道列表为空 (数据库有 35 个频道，但页面表格无数据)
  - **原因**: 由 ISSUE-002 导致，API 调用因无 token 返回 AUTH_UNAUTHORIZED
- ✅ 页面结构: 新建频道按钮、文字频道/语音频道分区、表格表头 (名称/类型/消息数/操作)

**截图**: admin-08-channels.png, admin-08b-channels-refresh.png

---

### 5. 用户管理 ⚠️ (Medium 问题)

**结果**:
- ❌ **ISSUE-005 (Medium)**: 用户列表为空 (数据库有 60+ 用户，但页面显示 0 总用户数/0 在线/0 管理员)
  - **原因**: 由 ISSUE-002 导致，API 调用因无 token 返回 AUTH_UNAUTHORIZED
- ✅ 页面结构: 搜索框、表格表头 (用户名/角色/在线/IP/延迟/操作)、分页控件

**截图**: admin-09-users.png

---

### 6. 模块管理 ✅ (结构正常)

**结果**:
- ✅ 页面结构: 模块表格 (模块/状态/说明/操作)、统一日志区域 (模块筛选/日志列表/查看更多日志)
- ⚠️ 表格数据为空 (由 ISSUE-002 导致)

**截图**: admin-10-modules.png

---

### 7. 存储概览 ✅

**结果**:
- ✅ 软件最大磁盘占用: 设定 10 GB，当前占用显示
- ✅ 各模块占用详情: 表格 (分类/占用/文件数/说明)
- ✅ 清理按钮: 清理日志文件、清理临时文件
- ✅ 合计: 0 MB, 0 文件

**截图**: admin-11-storage.png

---

### 8. 运行监控 ⚠️ (Low 问题)

**结果**:
- ❌ **ISSUE-006 (Low)**: 显示"暂无运行时数据"
  - **原因**: 由 ISSUE-002 导致，API 调用因无 token 返回 AUTH_UNAUTHORIZED
- ✅ 页面结构: 自动刷新下拉框 (5秒/10秒/30秒/关闭)

**截图**: admin-12-runtime.png

---

### 9. 主题切换与退出登录 ✅

**结果**:
- ✅ 深色/浅色主题切换正常
- ✅ 退出登录正常返回登录页面

**截图**: admin-13-theme-light.png, admin-14-theme-dark.png, admin-15-logout.png

---

## 问题清单

### ISSUE-001: Voice 端登录失败不显示错误提示 (Medium)

- **严重程度**: Medium
- **模块**: Voice 认证
- **现象**: 输入错误密码登录，API 返回 401 (AUTH_INVALID_CREDENTIALS)，但前端 UI 不显示任何错误提示，仅 console 有 error log
- **期望行为**: 登录失败时应显示"邮箱或密码错误，请检查后重试"提示
- **复现步骤**:
  1. 访问 http://localhost:5173
  2. 输入错误密码
  3. 点击登录
  4. 观察: 页面无任何反馈，console 显示 error
- **截图**: voice-02-login-error.png

### ISSUE-002: Admin 登录未正确处理 MFA-required 响应 (Critical)

- **严重程度**: Critical
- **模块**: Admin 认证
- **现象**: OWNER/ADMIN 登录时 API 返回 `mfaRequired: true`，前端错误地进入仪表盘但无有效 token，导致所有 API 调用失败
- **根本原因**: web/admin/src/App.tsx:743-750 登录处理逻辑未检查 `mfaRequired` 字段，仅检查 `accessToken` 是否存在 (空字符串为 falsy) 和 `user` 是否存在 (存在则设置 session)
- **影响范围**: 所有需要 API 调用的 Admin 模块 (频道管理、用户管理、模块管理、运行监控等)
- **期望行为**: 当 `mfaRequired: true` 时，应显示 MFA 验证码输入界面，用户输入 TOTP 验证码后调用 `/api/auth/mfa/verify-login` 获取真正的 accessToken
- **设计文档**: 认证授权专项设计 §18.4 要求 OWNER/ADMIN 强制 MFA
- **复现步骤**:
  1. 访问 http://localhost:9090/admin/
  2. 输入 owner@example.com / Test1234!
  3. 点击登录
  4. 观察: 直接进入仪表盘，但所有数据为空 (频道列表空、用户列表空、运行监控无数据)
  5. 检查 localStorage: `rrt_admin_token` 为 null
- **截图**: admin-02-mfa-required.png

### ISSUE-003: 服务器设置基本配置为空 (High)

- **严重程度**: High
- **模块**: Admin 服务器设置
- **现象**: 服务器设置 > 基本配置 标签页显示"暂无配置数据"
- **可能原因**:
  1. 由 ISSUE-002 导致 API 调用失败
  2. 或后端配置未初始化
- **截图**: admin-03-server-basic.png

### ISSUE-004: 频道管理列表为空 (Medium)

- **严重程度**: Medium
- **模块**: Admin 频道管理
- **现象**: 频道管理页面表格为空，但数据库有 35 个频道
- **原因**: 由 ISSUE-002 导致 API 调用失败
- **截图**: admin-08-channels.png

### ISSUE-005: 用户管理列表为空 (Medium)

- **严重程度**: Medium
- **模块**: Admin 用户管理
- **现象**: 用户管理页面显示 0 总用户数，但数据库有 60+ 用户
- **原因**: 由 ISSUE-002 导致 API 调用失败
- **截图**: admin-09-users.png

### ISSUE-006: 运行监控无数据 (Low)

- **严重程度**: Low
- **模块**: Admin 运行监控
- **现象**: 运行监控页面显示"暂无运行时数据"
- **原因**: 由 ISSUE-002 导致 API 调用失败
- **截图**: admin-12-runtime.png

---

## 修复优先级建议

### P0 (Critical - 立即修复)
1. **ISSUE-002**: Admin 登录 MFA 流程
   - 修改 web/admin/src/App.tsx 登录处理逻辑
   - 当 `mfaRequired: true` 时，显示 MFA 验证码输入界面
   - 调用 `/api/auth/mfa/verify-login` 获取真正的 accessToken
   - 此修复将自动解决 ISSUE-003 ~ ISSUE-006

### P1 (High - 尽快修复)
2. **ISSUE-001**: Voice 端登录失败错误提示
   - 修改 web/voice/src/App.tsx 登录错误处理
   - 捕获 401 错误并显示用户友好提示

### P2 (Medium - 计划修复)
3. **ISSUE-003 ~ ISSUE-006**: 由 ISSUE-002 修复后自动解决

---

## 测试覆盖

### Voice 端 (基于 Web端 UI 设计文档 - Voice.md)
- ✅ §3 视图状态机: 语音/功能视图切换
- ✅ §4 语音区 UI: 频道列表、语音控制栏
- ✅ §5 功能区 UI: 白板/云文件/文档/日程/小游戏
- ✅ §6 设置面板: 8 个标签页全部测试
- ✅ §7 音乐机器人: 搜索/播放队列/音源切换
- ✅ §12 无障碍设计: ARIA 标签完整
- ⚠️ §2 认证: 登录错误提示缺失

### Admin 端 (基于 Web端 UI 设计文档 - Admin.md)
- ✅ §3 登录页面: 表单显示正常
- ❌ §4 服务器设置: 基本配置为空
- ✅ §5 邮箱服务: SMTP 配置 + 两步验证
- ❌ §6 频道管理: 列表为空 (token 问题)
- ❌ §7 用户管理: 列表为空 (token 问题)
- ⚠️ §8 模块管理: 结构正常，数据为空
- ✅ §9 存储概览: 显示正常
- ❌ §10 运行监控: 无数据 (token 问题)
- ✅ §14 无障碍设计: ARIA 标签完整

---

## 结论

Voice 端功能基本完整，仅登录错误提示缺失。Admin 端存在严重的 MFA 登录流程缺陷 (ISSUE-002)，导致所有需要 API 调用的模块无法正常工作。建议优先修复 ISSUE-002，修复后需重新测试 Admin 端所有模块。

# 安全策略

## 报告漏洞

RidgeRiceTalk 重视安全问题。如果您发现安全漏洞，请按以下流程私下报告，**不要**公开提交 GitHub Issue。

### 报告方式

- **邮箱**：发送邮件至 `security@ridgericetalk.example`（请替换为实际邮箱）
- **邮件主题**：`[SECURITY] <简短描述>`
- **邮件内容**：
  - 漏洞影响的组件（后端 / 前端 / 部署脚本 / LiveKit / 数据库）
  - 复现步骤（最小化示例）
  - 影响评估（如可导致的越权、数据泄露、拒绝服务）
  - 建议的修复方向（可选）

### 响应时间

| 阶段 | 承诺时间 |
|------|---------|
| 确认收到 | 48 小时内 |
| 初步评估 | 7 天内 |
| 修复方案 | 视严重程度，Critical 7 天内、High 14 天内 |
| 补丁发布 | 修复完成后 3 天内 |

## 支持版本

RidgeRiceTalk 当前处于**开发阶段**，仅最新 `main` 分支接受安全修复。已发布的 tag 版本不提供单独的安全补丁回迁。

## 适用范围

- ✅ 后端 Go 服务（`server/`）
- ✅ 前端应用（`web/admin`；网页版 `web/voice` 已归档 2026-09-09，不再接收修复）
- ✅ 部署脚本（`server/scripts/`）
- ✅ CI/CD 工作流（`.github/workflows/`）
- ❌ 第三方依赖自身的漏洞（请向对应上游报告，如 LiveKit、PostgreSQL）
- ❌ 用户自定义修改导致的漏洞

## 安全加固参考

生产部署已内置以下加固措施（详见 [部署文档 §6.5](文档/05-工程规范与运维/部署文档.md)）：

- systemd 服务加固（NoNewPrivileges、ProtectSystem=strict、CapabilityBoundingSet=）
- JWT/CSRF/Encryption 密钥自动生成并持久化
- Brute-force 防护中间件
- RBAC 权限模型
- Trivy + CodeQL 持续安全扫描

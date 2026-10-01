# RidgeRiceTalk API 请求示例

本目录提供常见 API 请求的 JSON 载荷示例，配合 `curl -d @file.json` 使用，避免命令行直接传 JSON 时因 shell 转义导致密保问题等字段不匹配。

## Owner 初始化

```bash
# 1. 复制示例文件并填入你的 bootstrap token
cp examples/bootstrap-owner.json /tmp/bootstrap-owner.json
# 编辑 /tmp/bootstrap-owner.json，替换 REPLACE_WITH_YOUR_BOOTSTRAP_TOKEN

# 2. 执行注册
curl -sk -X POST https://192.168.31.187/api/admin/bootstrap/register \
  -H 'Content-Type: application/json' \
  -d @/tmp/bootstrap-owner.json
```

## 普通用户注册

```bash
cp examples/register-user.json /tmp/register-user.json
# 按需修改邮箱、用户名、密码、密保答案

curl -sk -X POST https://192.168.31.187/api/auth/register \
  -H 'Content-Type: application/json' \
  -d @/tmp/register-user.json
```

## 登录

```bash
cat > /tmp/login.json <<'EOF'
{
  "email": "owner@example.com",
  "password": "Admin@2026!!Secure"
}
EOF

curl -sk -X POST https://192.168.31.187/api/auth/login \
  -H 'Content-Type: application/json' \
  -d @/tmp/login.json
```

## 发送文字消息

```bash
# 先通过 /api/channels 获取频道 ID，替换 <channel_id>
cat > /tmp/message.json <<'EOF'
{"content": "Hello from curl!"}
EOF

curl -sk -X POST https://192.168.31.187/api/channels/<channel_id>/messages \
  -H "Authorization: Bearer <access_token>" \
  -H 'Content-Type: application/json' \
  -d @/tmp/message.json
```

## 注意事项

- 所有 JSON 文件示例使用 `securityQuestions` 数组，必须从服务端预定义列表中选择问题：
  - 您父亲的生日是哪一天？
  - 您母亲的娘家姓是什么？
  - 您的第一只宠物叫什么名字？
  - 您小学班主任的姓氏是什么？
  - 您出生城市是哪里？
  - 您最喜欢的书是什么？
  - 您学会驾驶的年份是哪一年？
  - 您童年最要好的朋友叫什么名字？
- 直接通过命令行 `-d '{"question":"..."}'` 传递中文时，不同终端/编码可能导致全角问号被转义为半角，服务端校验失败。
- 推荐始终使用 `curl -d @file.json` 文件方式。

# TLS / 网络场景模板（G-5 网络适配收口 · 2026-10-29）

本目录存放 **部署参考模板**，不参与运行时。`deploy-baremetal.sh` 的 `--tls` 三档：

| 档位 | 脚本行为 | 对应模板 | 适用场景 |
|------|----------|----------|----------|
| `--tls none`（默认） | 不配 HTTPS，仅桌面客户端可用；网页端语音需安全上下文，会打印提示 | 无 | 内网纯桌面端试用 |
| `--tls selfsigned` | 调 `setup-lan-https.sh` 生成自签证书 + Nginx 反代 | `nginx-tls-selfsigned.conf.tmpl` | 局域网 / 内网 / 无域名；客户端需信任证书 |
| `--tls acme --domain <fqdn>` | 安装 certbot 并签发 Let's Encrypt 正式证书 | `nginx-tls-acme.conf.tmpl` | 公网域名，80/443 公网可达 |
| （脚本之外，手工） | 外部 Caddy / Traefik / 云 LB 终结 TLS，本机只跑 8080/9090/7880 | `caddy-tls.conf.tmpl` | 已有网关或多服务共用 80/443 |

## 三类场景的部署参数对照（G-5 参数）

```bash
# A. 局域网自签
sudo ./deploy-baremetal.sh --tls selfsigned \
  --public-address https://192.168.1.10

# B. 公网域名 + 正式证书
sudo ./deploy-baremetal.sh --tls acme --domain voice.example.com \
  --public-address https://voice.example.com

# C. 外部反代（不传 --tls，避免本机 Nginx 抢 80/443）
sudo ./deploy-baremetal.sh \
  --public-address https://voice.example.com \
  --public-lk-url  wss://voice.example.com/rtc \
  --nat

# D. 纯 NAT（雨云等 5 端口映射，无域名、无 TLS）
sudo ./deploy-baremetal.sh --nat --public-ip 203.0.113.10 \
  --public-address http://203.0.113.10:50100 \
  --public-admin-address http://203.0.113.10:50103 \
  --public-lk-url ws://203.0.113.10:50101 \
  --port-api 50100 --port-admin 50103 --port-lk-ws 50101

# E. IPv6（脚本自动加方括号，也可显式给）
sudo ./deploy-baremetal.sh --public-address 'http://[2001:db8::10]:8080'
```

## 硬约束（踩过的坑）

1. **LiveKit 媒体端口 7881/tcp、7882/udp 服务端固定**（`internal/config.DefaultLiveKitPorts`
   无配置面）→ NAT/安全组必须 1:1 映射，不能换外部端口。
2. **TLS 终结在本机时**不要再用外部反代接管 80/443（两者抢端口）。
3. **CORS 白名单由 `RRT_PUBLIC_ADDRESS` 推导**：反代/NAT 后必须显式传
   `--public-address`，否则默认 `http://<IP>:8080` 会让真实 origin 被 403（N27 回归点）。
4. **IPv6 地址进 URL 必须带方括号**：`--public-address 'http://[2001:db8::10]:8080'`；
   脚本内部用 `rrt_url_host` 统一加括号（G-5 修复项）。
5. 不传任何 G-5 参数 = 与改动前**逐字节一致**的生成物（可随时回滚）。

详见 `文档/05-工程规范与运维/部署文档.md` §16.12「部署脚本网络适配参数（G-5）」。

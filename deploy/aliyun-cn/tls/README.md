# CDK 两个源站域名的独立证书同步

两组身份、导出脚本、同步任务和证书目录相互独立。SG 保留现有 Certbot、ACME 账户和签发状态；本目录不修改 `lanting.live` 的任何同步身份或证书。本次安装不代表 Nginx、EdgeOne 或 DNS 已经接入新证书。

2026-11-04 已完成两域部署和首次同步，两组 timer 均 enabled/active，同步服务 Result=success。已实际验证强制命令覆盖任意远端命令、禁止 TCP 转发、不可执行另一域导出脚本或额外参数、另一域证书 SAN 被拒绝且现有链接不变。原主站导出脚本、SSH 规则、同步脚本、证书与私钥，以及 CDK 两份 renewal 配置的 SHA-256 均与部署前一致。未读取应用 `app.env`，未运行 CA 演练。

首次同步证书指纹（SHA-256）：

- `cdk.lanting.live`：`A0:01:4F:F4:39:A5:84:B7:44:16:BF:AA:F3:88:46:84:07:47:D1:D5:1B:09:CF:11:06:07:D5:6B:2F:90:C5:BC`，有效至 2027-01-20。
- `cdk-admin.lanting.live`：`8A:A7:FE:FD:D0:A6:7E:56:4C:E1:B7:C0:4F:66:C6:35:5B:EA:25:91:15:E3:83:E7:23:3C:A8:92:8B:10:86:B5`，有效至 2027-01-22。

这些是本次验收指纹，不是需要永久固定的证书指纹；正常续期后会变化。

| 项目 | 兑换站 | 管理站 |
| --- | --- | --- |
| 唯一允许的 SAN | `cdk.lanting.live` | `cdk-admin.lanting.live` |
| SG 强制导出账号 | `cdk-tls-export` | `cdk-admin-tls-export` |
| SG 无参数导出命令 | `/usr/local/libexec/cdk-tls-export.py` | `/usr/local/libexec/cdk-admin-tls-export.py` |
| Ali 身份目录 | `/var/lib/cdk-tls-sync` | `/var/lib/cdk-admin-tls-sync` |
| Ali 证书目录 | `/etc/cdk-tls/current` | `/etc/cdk-admin-tls/current` |
| Ali 同步服务/定时器 | `cdk-tls-sync.service` / `.timer` | `cdk-admin-tls-sync.service` / `.timer` |
| Ali ACME 配置片段 | `cdk-acme-proxy.conf` | `cdk-admin-acme-proxy.conf` |

每个证书目录内均为 `fullchain.pem` 和 `privkey.pem`，root 所有且 `0600`；目录及私钥身份目录为 `0700`。`current` 原子指向对应 `releases/<fullchain SHA256>`，保留旧版本。每次同步验证系统 CA 链、唯一预期 SAN、域名、至少 24 小时剩余有效期和证书/私钥公钥匹配。

两端均固定 SSH Ed25519 主机密钥；客户端拒绝密码、agent 和未知主机。每个 SG 公钥限制来源 `121.40.64.63`、固定无参数命令，并同时由 sshd Match 强制命令、禁用 TCP/Unix socket/agent/X11/TTY/tunnel 转发。sudoers 只允许对应脚本且不接受参数；服务账号不能写自己的公钥文件或脚本。

同步定时器每 15 分钟执行一次，最多附加 60 秒随机延迟。有效证书相同则不 reload；更新时仅在 Nginx 正在运行时执行配置检查及 reload，检查/reload 失败恢复旧链接。不修改 Nginx 配置，也不启动 Certbot 或 CA 演练。SSH 输出仅由同步进程接收，不打印证书私钥或请求/应用秘密。

## Nginx 集成

兑换站使用：

```nginx
ssl_certificate /etc/cdk-tls/current/fullchain.pem;
ssl_certificate_key /etc/cdk-tls/current/privkey.pem;
```

管理站使用：

```nginx
ssl_certificate /etc/cdk-admin-tls/current/fullchain.pem;
ssl_certificate_key /etc/cdk-admin-tls/current/privkey.pem;
```

将各自 ACME 片段安装为 root 所有的 Nginx snippet，并在该域名的 **HTTP 与 HTTPS server** 中各 include 一次。片段只把 `/.well-known/acme-challenge/` 代理至 SG 固定 IP 的 HTTP 端口，并保持对应域名 Host；只允许 GET/HEAD，不传递原请求头或请求体，避免把应用凭据或 EdgeOne 回源鉴权头带入该 HTTP 链路；不缓存、不重试。SG 继续从 `/var/www/letsencrypt` 提供挑战文件，不得把 SG 同一路径再转发回 Ali，避免循环。

## 检查

```sh
systemctl status cdk-tls-sync.timer cdk-admin-tls-sync.timer
systemctl show cdk-tls-sync.service cdk-admin-tls-sync.service -p Result -p ExecMainStatus
journalctl -u cdk-tls-sync.service -u cdk-admin-tls-sync.service -n 20 --no-pager
openssl x509 -in /etc/cdk-tls/current/fullchain.pem -noout -dates -ext subjectAltName
openssl x509 -in /etc/cdk-admin-tls/current/fullchain.pem -noout -dates -ext subjectAltName
```

不要输出 `app.env`、SSH 私钥、源站私钥或包含私密回源鉴权配置的 `nginx -T`。切流后完整 CA 续期演练需单独执行和记录；本次部署未运行 CA 演练。

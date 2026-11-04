# CDK ChatGPT 新加坡出口

```text
Ali CDK/admin → http://127.0.0.1:18785 → SSH → SG 127.0.0.1:18785 → chatgpt.com:443
```

专门承载 CDK 的 ChatGPT 账户、账单和 session 查询。中继只允许精确的 `chatgpt.com:443` CONNECT 目标；TLS 由应用验证，中继不解密、不记录内容。现有 Google 18783 和 Zovo 18784 白名单及服务不变。应用应仅为需要此出口的客户端设置该代理，代理不可用时不得直接回退。

## 制品与账号

| 文件 | 安装位置 |
| --- | --- |
| `gpt-connect-proxy.py` | SG `/opt/cdk-gpt-egress/gpt-connect-proxy.py`，root 所有且账号不可写 |
| `cdk-gpt-connect.service` | SG `/etc/systemd/system/`，运行账号 `cdk-gpt-connect` |
| `62-cdk-gpt-egress.conf` | SG `/etc/ssh/sshd_config.d/`，专用 SSH 账号 `cdk-gpt-egress` |
| `cdk-gpt-tunnel.service` | Ali `/etc/systemd/system/`，专用账号 `cdk-gpt-egress` |

两服务均为 `MemoryMax=32M`，只监听回环地址。SG relay 与 SSH 账号分离，均为 nologin 系统账号。SSH 仅允许 publickey、本地转发到 `127.0.0.1:18785`，禁止 shell/subsystem（`MaxSessions 0`）、TTY、agent、X11 和远程转发。

## 安装与校验

1. 通过既有管理 SSH 确认 SG `43.156.235.208`、Ali `121.40.64.63`，确认两端 18785 未被占用。将会修改的文件备份到 root 专用的 0700 目录，并记录新增路径和服务基线。
2. 创建上述系统账号。Ali 在 `/var/lib/cdk-gpt-egress/`（0700）生成独立 Ed25519 密钥，私钥 0600 且仅留 Ali。SG 公钥条目必须包含 `from="121.40.64.63",restrict,port-forwarding,permitopen="127.0.0.1:18785"`，不得复制管理员私钥。
3. 经既有可信 SG 管理连接读取 `/etc/ssh/ssh_host_ed25519_key.pub`，将核对的 IP/公钥写入 Ali 账号的 `known_hosts`（0600）。不能直接信任未经核对的 ssh-keyscan，也不能跳过主机密钥验证。
4. SG 安装制品；执行本地测试、Python 编译检查、`systemd-analyze verify`、`sshd -t` 和该账号 `sshd -T -C user=cdk-gpt-egress,addr=121.40.64.63,host=aliyun-prod`。保持管理连接，使用 reload SSH，不 restart。
5. 启用 SG `cdk-gpt-connect`，然后启用 Ali `cdk-gpt-tunnel`。检查两端监听确为 `127.0.0.1:18785`，身份、32M 内存限制、enabled/active 状态及 `NRestarts=0`。
6. 本地测试 `python3 -B test_gpt_connect_proxy.py` 不发送网络请求。部署后通过回环代理检查非法目标返回 403；在 Ali 运行 `python3 -B verify_public_tls.py` 只做公开 TLS 握手和证书验证，不发送 HTTP 请求、用户 session 或真实账户信息。
7. 对比原 Google/Zovo 及业务服务 PID，确保此次部署未重启它们。应用服务的代理配置和依赖由主迁移步骤接入；端口已监听不等同于外部 TLS 可用，需完成步骤 6。

## 停用

先停止使用此代理的应用操作，再 `systemctl disable --now cdk-gpt-tunnel`（Ali）和 `systemctl disable --now cdk-gpt-connect`（SG）。保留或按备份清理专用 SSH 配置/公钥，修改 SSH 配置后先 `sshd -t` 再 reload。不得通过放宽白名单或移除代理配置实现直连回退；不触及现有 18783/18784 服务。

## 2026-11-04 部署验收

- SG `cdk-gpt-connect.service`、Ali `cdk-gpt-tunnel.service` 已 enabled/active，均 `NRestarts=0`、`MemoryMax=33554432`。两端仅监听 `127.0.0.1:18785`。
- 本地 6 项测试通过；Ali 经隧道验证 5 个非法目标/方法均返回 403；SG 直接回环拒绝测试通过。
- Ali 经该代理对公开 `chatgpt.com` 的 TLSv1.3 握手和证书校验成功，耗时 5.15 秒；未发送 HTTP 请求、session 或真实账户数据。此次不声称已验收真实用户账户接口。
- SG 原 Google/Zovo 中继、CDK/admin、MySQL、Nginx，以及 Ali 原 Google/Zovo 隧道、commerce Web/Worker 的 PID 和状态均未变化。
- SG 受保护备份：`/var/backups/cdk-gpt-egress/deploy-20261104-xaOsxH`；Ali 受保护备份：`/var/backups/cdk-gpt-egress/deploy-20261104-k60Nlp`。目录 0700，包含修改前的账号文件、新路径清单及原服务基线，未输出私钥。

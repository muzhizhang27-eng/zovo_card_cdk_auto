# SG 最终兼容入口（切流时由主任务启用）

2026-11-04 已在 SG 安装维护 gate 与 HTTPS 本地 ACME root，没有创建维护 marker，也未停止业务。安装前两个 vhost 与保存的 SG 基线 SHA-256 一致；备份 `/var/backups/cdk-sg-compat/install-20261104-9GxURH`。reload 后两站页面 200，三个业务进程和 Nginx master PID 未变。

本目录两个配置已放在 SG `/var/tmp/cdk-sg-compat-20261104/`，root 所有；已使用实际证书和运行时秘密 header 文件通过隔离 `nginx -t`，本子任务未启用。主任务在最终数据同步后原子替换 `/etc/nginx/conf.d/` 内同名 vhost，再 `nginx -t` 和 reload。

- HTTPS 业务请求固定转发 `121.40.64.63:443`，每域使用原 Host/SNI、系统 CA、强制证书验证，禁用上游重试、缓存、store 和缓冲；原请求体、Authorization 和 Cookie 正常转发，具体路由由 Ali allowlist 检查。
- 逐域 `X-CDK-Forward-Auth` 来自 `/etc/nginx/cdk-private/{cdk,cdk-admin}-forward-header.conf`，仅 root 0600；秘密不在制品内、不输出。Client-IP、XFF、X-Real-IP 均覆写为 SG 原 TCP 对端，剥离 EO 认证头和其他伪造来源头。
- HTTP 仍将业务请求 308 到原域 HTTPS；两端口 ACME 始终从 SG 本地 `/var/www/letsencrypt` 服务，不转发 Ali，避免证书挑战循环。
- 保留 `/var/lib/cdk-migration/maintenance` 门禁。主任务控制 marker；父目录应为 root 0755，以便 Nginx worker stat。

正向 ACME 验证已从 Ali 分别固定 SG、Ali 两个 IP，对两域 HTTP/HTTPS 共 8 条路径执行请求，全部 200 且挑战正文完全一致。只创建并删除了本次随机临时挑战文件，未打印其内容，也未执行 CA renewal。

# CDK 管理后台部署

管理域名：`https://cdk-admin.lanting.live/ops/cdkeys`。兑换域名保持 `https://cdk.lanting.live/recharge`。

管理实例使用独立 Linux 用户 `cdk-admin`、systemd 单元 `cdk-admin.service`、回环端口 `127.0.0.1:9081`。版本放在 `/opt/cdk-admin/releases/`，`current` 选择版本。数据库位于 `/var/lib/cdk-admin/cdk_recharge.db`，环境文件位于 `/etc/cdk-admin/app.env`。数据库目录为 0700、文件为 0600；密钥和真实 CDK 不进入发布包。现有兑换服务继续使用自己的 9080 和数据库。

## 构建和验证

- 前端按 `.nvmrc` 使用 Node 24.19，执行 `npm ci`、`VITE_PUBLIC_SITE_URL=https://cdk.lanting.live npm run check` 和 `npm audit`。
- 后端使用 Go 1.27.3；执行 `go test ./...`、`go vet ./...`、认证相关竞态测试及 `govulncheck ./...`。
- 在 Linux amd64 上用 `scripts/build-linux-backend.sh` 构建，SQLite 必须启用 CGO。
- `nginx-https.conf.template` 的 `__SCRIPT_HASHES__` 必须替换为最终 `index.html` 内联脚本的 SHA-256 CSP 哈希；不要启用 `unsafe-eval` 或脚本 `unsafe-inline`。
- Nginx 托管静态文件。`APP_ROLE=admin` 在后端额外限制兑换、换码、安装初始化和 webhook 入口；生产认证仅允许 HTTPS 反代请求。Nginx 必须覆写转发来源和协议，不接受客户端自报 IP。
- 上游默认只允许 `https://zovocard.com`，调整须由服务器环境 `CARD_API_ALLOWED_ORIGINS` 明确放行。HTTP 重定向不携带 Key 跟随，购买在断线后不自动重发。
- 关闭网页自更新，使用独立发布流程。此部署不启用 webhook 和代理换码。

## 数据迁移

先停止本地管理操作，使用 `scripts/cdk-admin-migration.py snapshot` 创建 SQLite 在线备份，包含完整码、管理员 bcrypt 哈希、设置和历史记录。不得复制活跃数据库的主文件来替代备份 API。

通过严格主机校验的 SSH 传输至 0700 暂存目录，然后用脚本 `install-new` 安装到全新管理数据目录。它拒绝覆盖现有目录或公开兑换库。启动前精确比较原有结构和逐行摘要；启动后使用 `verify --phase running`，仅允许经过审核的会话表/触发器、追加审计和产品缓存变化。输出不包含 CDK、密码、哈希、API Key 或 Session。

管理员账号和密码原样保留。线上生成新的 JWT 签名密钥，旧本地登录会话不迁移；上线后重新登录。上线后以线上后台为管理入口，本地副本保留作迁移备份，两个实例不做双向同步。

验收使用原账号登录，核对完整码及导出、上游套餐/余额只读请求、未认证访问拒绝、CSRF 和跨来源拦截、登录限速、退出后会话撤销、入口和日志脱敏。使用合成数据测试发码；未经具体额度授权，不为验收购买或消耗真实 CDK。

## 回滚与维护

保留本地在线快照和 `/var/backups/cdk-admin/<release>/`。上线失败时仅关闭管理域名入口并停止 `cdk-admin`；不要恢复或覆盖兑换站及主域 Nginx 文件。已上线后的代码回滚只切管理实例的 `current`，先判断数据库兼容性。数据库恢复会覆盖新数据，必须明确恢复点并先保存现状。

证书使用 `/etc/letsencrypt/live/cdk-admin.lanting.live/`，ACME webroot `/var/www/letsencrypt`。修改 Nginx 前先 `nginx -t`，reload 后同时验证管理域、兑换域和主站。现有 Certbot timer 和续期 reload 钩子负责证书续期。

审查与扫描只能降低已知风险，不能保证系统永不被攻击。后续应持续应用安全更新；需要新增备份保留策略、异机存储、MFA 或访问白名单时，按具体使用要求配置。

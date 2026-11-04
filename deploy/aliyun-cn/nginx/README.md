# Ali CDK Nginx 候选与维护态

2026-11-04 已由迁移主任务审核并安装 Ali 维护态，未启动 CDK 业务，未改 DNS/EdgeOne，也未改 SG vhost。现场 `nginx -t`（包含真实私有 map）通过；两域 80/443 首页均 503，ACME 不存在路径均 404。备份：`/var/backups/cdk-cn-candidates/install-20261104-st9dkE`。

## 来自现网的安全规则

基线为 SG `/etc/nginx/conf.d/cdk.lanting.live.conf`、`cdk-admin.lanting.live.conf` 和 `/etc/nginx/snippets/cdk-admin-proxy.conf`。原文副本在本机 `/tmp/cdk-cn-nginx-review-20261104/sg-cdk.conf`、`sg-cdk-admin.conf`、`sg-proxy-and-units.txt`。

- 兑换站保留 4 个页面路由、限定静态扩展名、11 个精确 API 路由及原 GET/HEAD/POST 方法限制，其余路径 404。
- 管理站保留页面/API allowlist、登录仅 POST/16k 请求体、禁用在线更新路由、完整 CSP（含原 inline script 哈希）和所有安全响应头。
- 管理站保留登录 5r/m、burst=5；API 10r/s、burst=30/10；每客户端最多 20 个连接，超限状态 429。仅将限流键从物理连接 IP 改为下述经过认证和格式校验的有效 IP，最大 45 字符。
- 原代理超时、禁用上游重试/缓存/store/缓冲、请求体大小限制均保留。初次迁移时所有响应均为 `Cache-Control: no-store`；下述静态性能候选仅对版本化 JS/CSS 的成功响应开放缓存。
- TLS 使用 `/etc/cdk-tls/current` 和 `/etc/cdk-admin-tls/current`；每域 80/443 均包含独立 ACME 代理片段。SG 仍持有 ACME 状态，未来 SG 443 兼容代理也必须保留本地 challenge root，避免代理循环。

## 客户端身份边界

`00-cdk-client-ip.conf` 不使用不可信 XFF，也不全局改写 realip。默认以 `$realip_remote_addr`（原 TCP 对端）为客户端 IP。仅以下任一认证成功后，才读取 `X-CDK-Client-IP`：

| 路径 | 必须匹配的键 | root 私有 include 文件 |
| --- | --- | --- |
| SG → 兑换站 | `43.156.235.208:cdk.lanting.live:SG独立秘密` | `/etc/nginx/cdk-private/cdk-forward-auth.map` |
| SG → 管理站 | `43.156.235.208:cdk-admin.lanting.live:SG独立秘密` | `/etc/nginx/cdk-private/cdk-admin-forward-auth.map` |
| EdgeOne → 兑换站 | `cdk.lanting.live:EO独立秘密` | `/etc/nginx/cdk-private/cdk-origin-auth.map` |
| EdgeOne → 管理站 | `cdk-admin.lanting.live:EO独立秘密` | `/etc/nginx/cdk-private/cdk-admin-origin-auth.map` |

每个文件由主任务在运行时写入一条带引号的键及值 `1;`，秘密不进入仓库。四个秘密各自独立；SG 使用 `X-CDK-Forward-Auth`，EO 使用 `X-CDK-Origin-Auth`。EO 必须覆写客户端 IP 为其真实请求 IP。`.ma[p]` 是仅匹配精确 `.map` 文件名的 glob：文件缺失或为空时不会阻止 Nginx 加载，也不会误信任头；`.bak` 不会被加载。`map_hash_bucket_size=256` 已用实际 256-bit 秘密验证。

认证后先限制 IP 文本为 2–45 个 IP 字符，再由 `geo` 解析 IPv4/IPv6；解析失败的 `255.255.255.255` 标记明确拒绝，仍回退 TCP 对端。这一无效地址行为见 [Nginx geo 官方文档](https://nginx.org/en/docs/http/ngx_http_geo_module.html)。认证秘密本身不会进入日志或限流键。入应用前清除 `X-CDK-Forward-Auth`、`X-CDK-Origin-Auth`、`X-CDK-Client-IP` 和 `Forwarded`，覆写 XFF/X-Real-IP。

## 维护门禁与安装边界

两机统一 marker：`/var/lib/cdk-migration/maintenance`。目录必须 root:root、0755，文件可为 root:root、0644 空文件，以便 Nginx worker 能 stat；不可使用 worker 无权遍历的 0700 父目录。存在时两域 80/443 的非 ACME 请求均 503。最终数据冻结工具只读检查该门禁，移除 marker 由主迁移任务控制。

`00-cdk-client-ip.conf`、`00-cdk-maintenance.conf`、`00-cdk-static-cache.conf`、两个域名配置安装到 `/etc/nginx/conf.d/`；proxy、maintenance gate 以及 TLS 目录内两个 ACME 片段安装到 `/etc/nginx/snippets/`。SG 维护时仅使用 maintenance map/gate；Ali 的身份 maps 不能未经设计直接用于 SG。SG 兼容代理后续由主任务部署固定 Ali IP、原域名 SNI/Host、严格证书验证及禁用重试。

## 静态性能配置（2026-11-05 已部署）

`00-cdk-static-cache.conf` 以 Host、请求方法、规范化路径及响应状态联合判断：仅两 CDK 域名的 GET/HEAD、`/assets/<name>-<8位版本字符>.js` 或 `.css`，且状态为 200/206/304 时返回 `public, max-age=31536000, immutable`。HTML、API、非版本化资源、无效长度的版本名、错误响应和其他域名均保持 `no-store`。两个 vhost 的父级 `add_header Cache-Control` 引用该变量，静态 location 不新增 `add_header`，因此保留既有 CSP/HSTS 等全部安全头的继承。

gzip 仅在原静态文件 location 内开启，类型为 JavaScript/CSS，最小 1024 字节、级别 5，返回 `Vary: Accept-Encoding`；不为动态接口开启压缩。新 map 与两个 vhost 是一次配置变更的整体，必须先具备 map 再验证 vhost。

主任务审查后于 2026-11-05 00:48:59（Asia/Shanghai）部署这三个配置，重读线上基线及候选 SHA 全部匹配，备份在 `/var/backups/cdk-static-cache/20261104T164859Z`。`nginx -t` 和 reload 成功，master PID 不变，没有重启业务。公网直源 33 项验收通过：两站首页 5 个资产各从 810432 字节压缩至 240840 字节（减少 70.28%），逐项解压内容相同；HTML/API/404 保持 no-store，全部原安全头保留，主站 `/ai` 返回 200。详见 [部署与验收证据](STATIC-CACHE-PRODUCTION-20261104T164859Z.json)。此证据不代表 EdgeOne 规则或 DNS 已完成。

在 Ali 运行 `python3 nginx/verify_static_cache.py <候选目录>` 可验证实际候选 vhost。测试仅将 TLS/监听端口、文件路径和上游替换为临时 loopback Nginx、合成静态文件和 fake API；不读取生产数据库、不调用生产应用或供应商。覆盖 200/206/304、HEAD、404/405/503、HTTP 跳转、7/9 位和未版本化文件、gzip 解压一致、API/HTML 不缓存，以及两域全部原安全头。`verify_candidates.py` 仍验证完整候选语法、unit、19 组回源身份/剥离和维护门禁。

EdgeOne 已为两域 `/assets/*` 下带 8 位版本后缀的 JS/CSS 发布按源站缓存的例外，公网两轮命中与错误不缓存验收通过，详见 [EdgeOne 配置](../edgeone/README.md)。主任务检查的当前控制台明确写明**下方规则优先级更高**，故该例外应位于两条 CDK 不缓存规则之后；以当前控制台为准，不能套用旧网页文档的相反顺序。保留各域名原回源认证头与客户端 IP 规则。

## 校验与制品

`verify_candidates.py` 在 Ali 对隔离配置执行完整 `nginx -t`、systemd239 unit 校验，并启动临时回环 Nginx/假上游验证身份与头清理。19 组 SG/EO/错误秘密/跨域/非法和过长 IP 用例、3 组维护/ACME/解除维护用例通过；测试不启动业务，不调用供应商，也不使用真实秘密。

前端来自 SG `releases/20261028-upstream-1434/web`，已复制到 Ali 各自 `releases/20261104-cn-migration/web`：兑换站 114 文件、admin 74 文件，逐文件内容、大小和权限 manifest 完全一致。后端新 release 文件为：

- `/opt/cdk-recharge/releases/20261104-cn-migration/cdk-recharge`
- `/opt/cdk-admin/releases/20261104-cn-migration/cdk-admin`（同一主 binary）
- `/opt/cdk-lookup/releases/20261104-cn-migration/cdk-lookup`

主 binary SHA-256 为 `77fe9d2f80f069b86a891373fac98ecabb0f842dedf4c5a55803e2e55c6309ce`，lookup 为 `f77b8a4adc92322388e4ec63988c4ceec3f4bb63b2d5f91ab91118a983869fbe`。本子任务未改 `current` 链接；链接切换、业务启动与最终数据切换由主任务执行。安全基线及前端 manifest 摘要见 `validation-20261104.json`。

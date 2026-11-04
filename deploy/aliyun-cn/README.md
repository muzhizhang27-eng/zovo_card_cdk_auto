# CDK 杭州部署与切换

目标：`cdk.lanting.live/recharge` 与 `cdk-admin.lanting.live/ops/login` 的网页、API 和 SQLite 迁到 `aliyun-prod`；需要海外出口的 Zovo、ChatGPT 请求继续经 `sg-prod`。不修改已迁移的主站数据库或应用。

## 源码和构建基线

- GitHub upstream：`ce8a9795b55df0e2b401d26110e582c27f2c9eb4`，版本 1.4.34。
- 叠加 SG `20261028-upstream-1434` 的 75 份校验通过的线上源码，保留已有身份、会话、角色、限流、日志和只读查询隔离补丁。
- 本次新增专用代理和有界优雅停机；不从裸 upstream 覆盖线上安全补丁。
- 主二进制 SHA256：`77fe9d2f80f069b86a891373fac98ecabb0f842dedf4c5a55803e2e55c6309ce`。
- lookup 二进制 SHA256：`f77b8a4adc92322388e4ec63988c4ceec3f4bb63b2d5f91ab91118a983869fbe`。
- 全量 Go 测试、7 个包的 race、合成 SQLite/HTTP/Unix socket/SIGTERM 测试通过。测试在隔离网络中执行，不发起真实充值、开卡或用户账单查询。

## 执行顺序

1. 完整核对源代码、运行配置、两个 SQLite 库、私有 manifest、HTTPS 和只读 lookup 依赖。
2. 创建受限账号，准备两个独立数据目录，首次只读在线备份并比对全部表、schema 和类型敏感的内容指纹。
3. 准备静态 Linux AMD64 二进制与原有前端资源；配置 18784 Zovo、18785 ChatGPT 专用 SSH 出口和独立 TLS 同步。
4. 安装带维护门禁的 Nginx 候选和受资源限制的 systemd 服务。先在隔离候选上检查健康、权限、路由、TLS、出口限制和停止排空。
5. 准备 EdgeOne 的两条域名、HTTPS、逐域真实 IP 保护和缓存规则。动态接口、管理后台和 HTML 不缓存；缓存规则验证完成前不切换 DNS。
6. 冻结 SG 两个业务入口，保留 ACME；检查已接受请求、进程连接及持久化任务，确认排空后停止三个旧 CDK 单元。
7. 使用 [最终快照工具](FINAL-SNAPSHOT.md) 在两端全部停机、维护门禁生效时同步最新数据。验证源 before = backup = after = Ali；保留原 JWT、卡台配置和私有文件权限。
8. 启动 Ali 三个服务并验收，只有 Ali 可以处理业务写入。SG 改为固定 Ali HTTPS 转发，严格验证证书、禁止上游重试，保留真实客户端 IP 的鉴权保护；旧业务单元禁用并屏蔽。
9. 切换阿里云 DNS 至 EdgeOne 实际分配的 CNAME，核验中国大陆节点、证书、缓存、路由、未登录拒绝和主站健康；对比切换前后的公开页面响应时间。

## 运行边界

| 位置 | 职责 |
| --- | --- |
| Ali 9080 | CDK 兑换 API；仅回环监听 |
| Ali 9081 | 独立管理 API；仅回环监听 |
| Ali Unix lookup | 管理库只读状态查询；不共享管理写权限 |
| Ali 18784 → SG | 只允许 Zovo 的 HTTPS CONNECT |
| Ali 18785 → SG | 只允许 chatgpt.com 的 HTTPS CONNECT |
| SG Certbot → Ali TLS 同步 | SG 保持唯一签发状态；Ali 定时校验后原子更新证书 |

详见 [出口](gpt-egress/README.md)、[证书](tls/README.md)、[最终数据同步](FINAL-SNAPSHOT.md)。私钥、JWT、CDK 数据、客户 session 和回源鉴权值均不进入 Git。

## 回滚约束

Ali 尚未开始业务写入时，可保持双方维护并恢复 SG 原配置和服务。Ali 开始写入后，禁止仅切 DNS 或启动旧 SG 库；必须先冻结 Ali、确认排空，再反向同步最新两个库并逐项校验，始终保持唯一写入端。SG 兼容转发负责 DNS 缓存过渡期，不能成为第二个业务实例。

## 证书续期验证的已知限制

此前主站的 CA 演练遇到 DNS 查询超时，现有证书仍有效。CDK 的证书同步、证书链/SAN/私钥匹配与 ACME 路由须分别验收；这些检查不等同于 CA 续期演练成功。不要为通过演练关闭证书验证。

## 当前状态

2026-11-04 23:57:55（Asia/Shanghai）：正式应用与数据切换已完成，两个站点恢复服务。

- 最终数据：recharge 18 表 / 72 行，admin 18 表 / 200 行；source before = backup = after = Ali 全内容指纹、schema、journal 比较通过。SQLite 客户端版本不同，内容一致。
- 最终私有证据目录：本机 `/tmp/cdk-final-20261104-1555`；Ali `/root/cdk-final-target-8bdh97hn`。JWT、API 配置、manifest 与各自文件权限保留。
- Ali 三个 CDK 单元是唯一运行实例；健康 200、未登录管理 API 401、禁止路由 404；候选停机演练三个进程均正常排空退出。
- SG 三个旧业务单元已经 masked / inactive；两域兼容入口固定转发 Ali HTTPS，并验证证书和真实访客 IP。旧入口两个页面均 200。
- SG 回滚配置及旧 unit 留存于 `/var/backups/cdk-sg-compat/final-20261104-1558`。Ali 开始写入后仍须遵循上述反向同步回滚约束。
- 维护窗口：23:52:27 至 23:57:55。商城主站保持运行。
- 2026-11-05 00:44:36 管理站、00:56:25 充值站完成业务 CNAME 切换。EdgeOne 两域均“已生效 / 已部署”，源站为 Ali，免费边缘证书及 DNS 委派记录均已验收；详见 [EdgeOne 配置](edgeone/README.md)。
- 版本化静态资产 gzip 和源站/边缘缓存已上线。首屏五个资产由 810,432 B 降到 240,840 B（减少 70.28%）；两域公网重复请求均命中 CDN，HTML/API/错误响应仍不缓存。
- 浏览器现有管理登录态保留，管理总览和卡密列表正常加载；公开充值、批量充值、卡密查询和账单查询页面正常渲染。验收未发起真实充值、开卡或账单交易。
- 最终迁移源码已在 `/Users/nan/Workhub/personal/backups/cdk-migration-20261105` 保存工作树归档、Git 历史 bundle 和逐文件 SHA manifest；已验证归档恢复、bundle 与 Git fsck。
- 两站 CDN/DNS/功能验收通过后，已删除 SG 11 个无引用的旧 CDK 发布、源码及暂存路径，移除 699,184 KiB（约 682.8 MiB）。删除前后检查通过，三个旧业务单元继续 masked/inactive。保留 Zovo/ChatGPT/Google 出口、Certbot/证书导出、兼容 Nginx 入口和旧数据库/回滚材料。精确路径与证据见 [清理报告](SG-CLEANUP-EXECUTION-20261105.json)。SG 仍承担出口和证书职责，不能关闭整台服务器。

# SG CDK 清理前审计

审计开始时间：2026-11-05 00:29（Asia/Shanghai）。本次仅通过 `ssh -J aliyun-prod sg-prod` 只读检查；未删除文件、未修改服务或 Nginx。此文件是候选清单，不表示清理已经完成。

后续执行记录：2026-11-05 01:01（Asia/Shanghai），迁移主任务确认 CDN、DNS、浏览器/功能和持久源码验收 gate 通过后，已通过 `sg-cleanup.py --execute --acceptance-complete` 删除下表全部 11 个代码/暂存路径。执行前后检查均通过，旧服务仍 masked/inactive，保留目录 inode 不变。移除分配空间为 **699,184 KiB（682.796875 MiB）**；磁盘空闲量同期增加 **715,956,224 字节（682.7890625 MiB）**，后者可能受同期文件系统活动影响。

执行证据为 `SG-CLEANUP-EXECUTION-20261105.json`；SG 原子进度日志为 `/var/backups/cdk-sg-compat/cleanup-20261104T170145Z-259651.json`（0600）。终检见 `SG-POST-CLEANUP-VALIDATION-20261105.json` 与 `SG-CLEANUP-ACME-VALIDATION-20261105.json`：主站和两域页面正常、未登录管理接口 401、三条海外出口 CONNECT/TLS 校验通过、证书同步正常；ACME 源站和公网链路通过，公网 HTTP 的 301 跟随到同域 HTTPS 后正文匹配。两个独占命名的临时 challenge 文件均已删除，未执行 CA 续签。本文其余内容保留清理前的审计依据。

## 执行门槛

必须先完成两个域名的 EdgeOne 证书、回源认证和缓存规则、业务 CNAME 切换及功能验收，再执行下面的代码清理。清理当天再次确认旧业务均 `masked/inactive/MainPID=0`，没有构建或迁移进程引用候选路径；确认 Ali 是唯一业务写入端，本地迁移源码和当前 Ali 发布制品仍完整可用。

业务 CNAME 切换不代表 SG 可以整机下线：Zovo、ChatGPT 和主站 Google 出口仍在 SG；Ali 源站证书也仍由 SG Certbot 签发后同步。

## 当前依赖证据

- `cdk-recharge.service`、`cdk-admin.service`、`cdk-lookup.service` 均 `LoadState=masked`、`ActiveState=inactive`、`MainPID=0`。
- `/proc/*/{cwd,exe,fd}` 扫描未发现活动进程引用下面的 CDK 业务发布树、源码树或业务数据目录。该检查是审计时刻的快照，删除前须重新执行。
- 检查 `/etc/systemd/system`、Nginx、cron、Certbot renewal/hooks、`/usr/local/libexec`、`/usr/local/bin`，以及 crontab、supervisor、init 和 transient unit 路径。没有发现活动配置引用旧 CDK 业务源码/发布树。命中项是仍需保留的中继、ACME、证书导出和兼容入口。
- 两个 SG Nginx vhost 的业务 `proxy_pass` 都为 `https://121.40.64.63:443`；没有指向旧 CDK 应用的 `root` 或本机业务端口。ACME 的 `root` 为 `/var/www/letsencrypt`。
- `cdk-gpt-connect.service`、`lanting-zovo-connect.service`、`lanting-google-connect.service` 均 active；其代码分别在 `/opt/cdk-gpt-egress`、`/opt/lanting-zovo-egress`、`/opt/lanting-google-egress`。另有三个来自 Ali 的对应 SSH 会话。
- `certbot.timer` active/waiting。两个 CDK vhost 仍引用 `/etc/letsencrypt/live/<域名>/` 证书。

## 门槛通过后可清理的 CDK 代码与临时制品

下表大小来自 `du -sx`，单位 KiB；只使用完整明确路径，不使用 `cdk*` 通配删除。

| 路径 | KiB | 依据 |
| --- | ---: | --- |
| `/opt/cdk-recharge` | 89,696 | 旧 recharge 的 current 链接和 4 个 release；旧服务 masked，Nginx 已转 Ali |
| `/opt/cdk-admin` | 38,252 | 旧 admin 的 current 链接和 2 个 release；旧服务 masked，Nginx 已转 Ali |
| `/opt/cdk-lookup` | 19,188 | 旧 lookup 的 current 链接和 2 个 release；旧服务 masked |
| `/home/ubuntu/cdk-recharge-staging` | 483,024 | 4 个旧构建/恢复源码目录，含 Go 工具链；无活动运行依赖 |
| `/home/ubuntu/cdk-admin-staging` | 38,228 | 旧管理站构建目录；无活动运行依赖 |
| `/home/ubuntu/cdk-cn-build-20261104` | 28,164 | 此次迁移源码、构建脚本和 out 制品；Ali 已使用独立复制后的发布文件 |
| `/var/tmp/cdk-cn-source-20261104.bundle` | 1,196 | 为本地恢复 Git 历史产生的 bundle |
| `/var/tmp/cdk-cn-source-git-20261104` | 1,376 | 产生上述 bundle 的临时 Git 目录 |
| `/var/tmp/cdk-sg-compat-20261104` | 16 | 已安装兼容配置的暂存副本；运行配置在 `/etc/nginx/` |
| `/tmp/cdk-gpt-egress-20261104` | 16 | GPT 中继安装暂存副本；运行代码在 `/opt/cdk-gpt-egress` |
| `/tmp/cdk-sg-maintenance-20261104` | 28 | 维护配置安装暂存副本；运行配置在 `/etc/nginx/` |

合计约 **699,184 KiB / 682.8 MiB**。实际回收量以清理后磁盘统计为准。

旧 current 指向：

- `/opt/cdk-recharge/releases/20261028-upstream-1434`
- `/opt/cdk-admin/releases/20261028-upstream-1434`
- `/opt/cdk-lookup/releases/20261028-upstream-1434`

两个 `/home/ubuntu/cdk-*-staging` 源码树内未发现生产 `.env` 或 SQLite 数据库；发现的 PEM 均位于 Go 官方工具链测试数据路径。此检查基于文件名和路径，没有读取私密内容，不能替代最后一次扫描。

### 源码与制品保留证据

本地仓库为 `/Users/nan/Workhub/personal/github/zovo_card_cdk_auto`，分支 `codex/aliyun-cdk-migration`；迁移修改尚未提交，不能只依赖 GitHub upstream 恢复。

以下本地文件在本次审计中确认存在：

| 本地制品 | SHA-256 |
| --- | --- |
| `/tmp/cdk-cn-migration-20261104/candidate-verified-source.tgz` | `066e223b9b50dd03fe68da8e7bb72c75d1c834779fec741fb63ab3aa2e16acf4` |
| `/tmp/cdk-cn-migration-20261104/migration-patch-manifest.json` | `16ea8359506f1fdfdd5b25958c5db4cf2ab8bec3ada1740618ae2a676568a333` |
| `/tmp/cdk-cn-migration-20261104/source-build-validation.json` | `ebd204bc01866d74f13cfeb45e12182e138f405cd7da1c9c6ee0d0470f7b027e` |

现存 manifest 记录 75/75 SG deployed source 文件匹配、82 个最终编译源码文件与本地匹配。Go 工具链为 `go1.27.3 linux/amd64`。SG `build-final.sh` 使用 `/home/ubuntu/cdk-recharge-staging/20261022-recovery-i41tuhd/toolchain/go/bin/go`；删除 staging 后，此旧脚本不再可用，需要在未来构建环境单独安装工具链。

不要把临时目录当作长期唯一备份；在删除 SG 最后一份源码前，应将最终本地源码和非敏感部署配置保存为持久制品或提交到获授权的代码仓库。

## 必须保留的运行组件

| 组件或路径 | 保留原因 |
| --- | --- |
| `/opt/cdk-gpt-egress`、`/etc/systemd/system/cdk-gpt-connect.service` | CDK ChatGPT 出口仍运行在 SG |
| `/opt/lanting-zovo-egress`、`lanting-zovo-connect.service` | CDK 和主站所需 Zovo 出口 |
| `/opt/lanting-google-egress`、`lanting-google-connect.service` | 主站 Google 登录出口 |
| `/var/lib/cdk-gpt-egress`、`/var/lib/lanting-zovo-egress`、`/var/lib/lanting-google-egress` 及相关用户/SSH Match 配置 | Ali 到 SG 的受限 SSH 身份和隧道 |
| `/etc/letsencrypt`、`/var/lib/letsencrypt`、Certbot service/timer/hooks、`/var/www/letsencrypt` | 现有源站证书的唯一签发状态与 HTTP 验证目录 |
| `/usr/local/libexec/cdk-tls-export.py`、`cdk-admin-tls-export.py`、`/var/lib/cdk-tls-export`、`/var/lib/cdk-admin-tls-export`、对应 SSH/sudoers 配置 | Ali 两域源站证书同步依赖 |
| `/usr/local/libexec/lanting-tls-export.py`、`/var/lib/lanting-tls-export` 及相关配置 | 主站源站证书同步依赖 |
| `/etc/nginx/conf.d/cdk.lanting.live.conf`、`cdk-admin.lanting.live.conf`、`/etc/nginx/cdk-private`、维护片段 | DNS 缓存过渡入口；CDN 切换后其中 ACME 功能仍被 Ali 依赖，不可整文件删除 |
| `/var/lib/cdk-migration` | Nginx 维护标记所在目录；即使当前无 marker，目录权限仍属于运行配置 |
| 三个旧 CDK service 到 `/dev/null` 的 mask 链接 | 防止旧业务被误启动；占用可忽略，不要为清理代码解除 mask |
| `lanting-aliyun-bbr-route.service` 及 `/var/lib/lanting-aliyun-bbr-route` | 已在使用的 Ali/SG 网络配置 |

## 数据和回滚材料：不混入代码删除

以下内容不是正在运行的业务，但包含数据库、环境配置或私有回滚材料。用户本次要求清理无用代码；本清单不据此自动授权删除业务数据或所有历史备份。

| 路径 | KiB | 分类 |
| --- | ---: | --- |
| `/var/lib/cdk-recharge` | 2,548 | 已停用的 SG 旧业务数据库 |
| `/var/lib/cdk-admin` | 292 | 已停用的 SG 旧管理数据库 |
| `/var/tmp/cdk-cn-rehearsal-rs2dcoln` | 2,804 | 两个数据库快照，不能当成代码缓存 |
| `/var/tmp/cdk-final-source-kzdne3rj` | 3,360 | 最终数据快照、env、私有 manifest、bundle |
| `/var/backups/cdk-recharge` | 1,992 | 历史回滚材料 |
| `/var/backups/cdk-admin` | 284 | 历史回滚材料 |
| `/var/backups/cdk-upstream` | 1,832 | 历史回滚材料 |
| `/var/backups/cdk-sg-compat` | 92 | 当前入口安装前配置和旧 unit 归档 |
| `/var/backups/cdk-gpt-egress` | 44 | 仍运行中继的部署前账号/配置备份 |

可另行确定保留期限或将这些数据迁入受保护的归档；不得删除后再声称具备原有回滚能力。Ali 已产生业务写入后，任何回滚都必须先冻结并反向同步最新数据，不能启动 SG 旧库。

## 其他候选：本次未判定可删

- `/home/ubuntu/.cache/go-build`：774,136 KiB；`/home/ubuntu/go/pkg/mod`：599,580 KiB。它们是可重建的共享 Go 缓存，但未证明只由本项目使用，不能用广泛通配清理。
- `/opt/lanting-commerce`：2,081,308 KiB；`/opt/lanting-runtime`：240,216 KiB；以及 `lanting-*` 的 staging/source 文件属于主站。主站服务虽然 masked，仍有 `/run/systemd/transient/lanting-invoice-help-candidate.service`（failed，MainPID=0）引用 `/opt/lanting-commerce/releases/20261104-140722-invoice-help`。需要独立确认主站源码保存和运行依赖，不能因名称中出现 `cdk` 就归入本次 CDK 代码删除。
- `/home/ubuntu/lanting-users-cdk-source-20261028` 和 `/tmp/lanting-cdk-rollout-20261027` 是主站 CDK 集成改动，不是独立 CDK 服务目录。
- 不删除 `/tmp/systemd-private-*` 或 `/var/tmp/systemd-private-*`：其中有三个活动出口服务的私有临时目录。

## 清理后复核

1. 候选路径已消失，三个旧 CDK 服务仍 masked/inactive；SG 不存在旧业务进程或 9080/9081 业务监听。
2. 三个出口服务及 Ali 对应隧道持续 active，保持目标白名单和 TLS 校验；不用真实客户交易作测试。
3. SG `nginx -t`、两域 ACME 路径、Ali 源站证书同步继续正常。
4. 两域公网 CNAME、边缘证书和页面/API 访问仍正常，登录/管理鉴权和真实 IP 处理未退化。
5. 报告最终删除的精确路径、实际回收空间和保留项；不要把本次只读审计写成已执行清理。
